package fake

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/platform"
)

// Input records typed and pasted text and lets a test press hotkeys.
type Input struct {
	mu     sync.Mutex
	typed  []string
	pasted []string
	subs   []*hotkeySub
	bound  []string

	setupBinary string
	prepared    int
	// Err, if set, is returned by Type, Paste, SetupInput, BindHotkeys and
	// PrepareInsert.
	Err error
	// Files is what SetupInput reports.
	Files platform.InputFiles
}

var _ platform.InputSetup = (*Input)(nil)

type hotkeySub struct {
	mu     sync.Mutex // held while sending, and to close ch
	closed bool
	ctx    context.Context
	combos []string
	ch     chan platform.HotkeyEvent
}

// Hotkeys implements platform.Input.
func (in *Input) Hotkeys(ctx context.Context, combos []string) (<-chan platform.HotkeyEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &hotkeySub{ctx: ctx, combos: slices.Clone(combos), ch: make(chan platform.HotkeyEvent)}
	in.mu.Lock()
	in.subs = append(in.subs, s)
	in.mu.Unlock()
	go func() {
		<-ctx.Done()
		in.mu.Lock()
		in.subs = slices.DeleteFunc(in.subs, func(x *hotkeySub) bool { return x == s })
		in.mu.Unlock()
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	}()
	return s.ch, nil
}

// Press delivers a press (pressed=true) or release of combo to every active
// Hotkeys registration that includes it, blocking until each has received it
// or ended. It reports how many registrations received the event.
func (in *Input) Press(combo string, pressed bool) int {
	in.mu.Lock()
	var subs []*hotkeySub
	for _, s := range in.subs {
		if slices.Contains(s.combos, combo) {
			subs = append(subs, s)
		}
	}
	in.mu.Unlock()
	n := 0
	ev := platform.HotkeyEvent{Hotkey: combo, Pressed: pressed, At: time.Now()}
	for _, s := range subs {
		s.mu.Lock()
		if !s.closed {
			select {
			case s.ch <- ev:
				n++
			case <-s.ctx.Done():
			}
		}
		s.mu.Unlock()
	}
	return n
}

// Type implements platform.Input by recording text.
func (in *Input) Type(ctx context.Context, text string) error {
	return in.record(ctx, &in.typed, text)
}

// Paste implements platform.Input by recording text.
func (in *Input) Paste(ctx context.Context, text string) error {
	return in.record(ctx, &in.pasted, text)
}

func (in *Input) record(ctx context.Context, into *[]string, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.Err != nil {
		return in.Err
	}
	*into = append(*into, text)
	return nil
}

// SetupInput implements platform.InputSetup: it records binary and returns
// Files.
func (in *Input) SetupInput(ctx context.Context, binary string) (platform.InputFiles, error) {
	if err := ctx.Err(); err != nil {
		return platform.InputFiles{}, err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.setupBinary = binary
	return in.Files, in.Err
}

// BindHotkeys implements platform.InputSetup: every combination is bound to
// itself, and recorded.
func (in *Input) BindHotkeys(ctx context.Context, combos []string) ([]platform.HotkeyBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.Err != nil {
		return nil, in.Err
	}
	out := make([]platform.HotkeyBinding, 0, len(combos))
	for _, c := range combos {
		in.bound = append(in.bound, c)
		out = append(out, platform.HotkeyBinding{Hotkey: c, Trigger: c})
	}
	return out, nil
}

// PrepareInsert implements platform.InputSetup: it counts the calls.
func (in *Input) PrepareInsert(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.Err != nil {
		return in.Err
	}
	in.prepared++
	return nil
}

// Prepared returns how many times PrepareInsert succeeded.
func (in *Input) Prepared() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.prepared
}

// Bound returns every combination passed to BindHotkeys, in order.
func (in *Input) Bound() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.bound)
}

// SetupBinary returns the binary last passed to SetupInput.
func (in *Input) SetupBinary() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.setupBinary
}

// Typed returns every string passed to Type, in order.
func (in *Input) Typed() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.typed)
}

// Pasted returns every string passed to Paste, in order.
func (in *Input) Pasted() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.pasted)
}

// Windows holds a scripted list of windows; Focus changes the active one.
type Windows struct {
	mu      sync.Mutex
	windows []platform.Window
	active  string
}

// Set replaces the window list and makes the window with ID active active.
func (w *Windows) Set(windows []platform.Window, active string) {
	w.mu.Lock()
	w.windows, w.active = slices.Clone(windows), active
	w.mu.Unlock()
}

// Active implements platform.Windows.
func (w *Windows) Active(ctx context.Context) (platform.Window, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.find(w.active)
}

// List implements platform.Windows.
func (w *Windows) List(ctx context.Context) ([]platform.Window, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.windows), nil
}

// Focus implements platform.Windows.
func (w *Windows) Focus(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.find(id); err != nil {
		return err
	}
	w.active = id
	return nil
}

func (w *Windows) find(id string) (platform.Window, error) {
	for _, win := range w.windows {
		if win.ID == id {
			return win, nil
		}
	}
	return platform.Window{}, fmt.Errorf("window %q: %w", id, platform.ErrNotFound)
}

// Opened is one call to the Launcher.
type Opened struct {
	// Kind is "url", "app" or "file".
	Kind string
	// Target is the URL, app or path.
	Target string
}

// Launcher records what it was asked to open.
type Launcher struct {
	mu     sync.Mutex
	opened []Opened
	// Err, if set, is returned by every method.
	Err error
}

// OpenURL implements platform.Launcher.
func (l *Launcher) OpenURL(ctx context.Context, url string) error { return l.open(ctx, "url", url) }

// OpenApp implements platform.Launcher.
func (l *Launcher) OpenApp(ctx context.Context, app string) error { return l.open(ctx, "app", app) }

// OpenFile implements platform.Launcher.
func (l *Launcher) OpenFile(ctx context.Context, path string) error {
	return l.open(ctx, "file", path)
}

func (l *Launcher) open(ctx context.Context, kind, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return l.Err
	}
	l.opened = append(l.opened, Opened{Kind: kind, Target: target})
	return nil
}

// Opened returns every call so far, in order.
func (l *Launcher) Opened() []Opened {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.opened)
}

// URLs returns every URL opened so far, in order.
func (l *Launcher) URLs() []string {
	var urls []string
	for _, o := range l.Opened() {
		if o.Kind == "url" {
			urls = append(urls, o.Target)
		}
	}
	return urls
}

// Notifier records notifications and tray changes, and lets a test click a
// notification's action.
type Notifier struct {
	mu    sync.Mutex
	shown []platform.Notification
	open  []*openNote
	tray  []platform.TrayStatus
}

type openNote struct {
	ch   chan string
	once sync.Once
}

func (o *openNote) finish(action string) {
	o.once.Do(func() {
		if action != "" {
			o.ch <- action // buffered, never blocks
		}
		close(o.ch)
	})
}

// Notify implements platform.Notifier.
func (n *Notifier) Notify(ctx context.Context, note platform.Notification) (<-chan string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o := &openNote{ch: make(chan string, 1)}
	n.mu.Lock()
	n.shown = append(n.shown, note)
	n.open = append(n.open, o)
	n.mu.Unlock()
	go func() {
		<-ctx.Done()
		o.finish("")
	}()
	return o.ch, nil
}

// Click simulates the user clicking action on the i-th notification shown
// (0-based); an empty action closes it without a click. It reports false if
// there is no such notification.
func (n *Notifier) Click(i int, action string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if i < 0 || i >= len(n.open) {
		return false
	}
	n.open[i].finish(action)
	return true
}

// Shown returns every notification so far, in order.
func (n *Notifier) Shown() []platform.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.shown)
}

// SetTray implements platform.Notifier.
func (n *Notifier) SetTray(ctx context.Context, s platform.TrayStatus) error {
	n.mu.Lock()
	n.tray = append(n.tray, s)
	n.mu.Unlock()
	return nil
}

// Tray returns every tray status set so far, in order.
func (n *Notifier) Tray() []platform.TrayStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.tray)
}

// Clipboard is an in-memory clipboard.
type Clipboard struct {
	mu   sync.Mutex
	text string
}

// ReadText implements platform.Clipboard.
func (c *Clipboard) ReadText(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text, nil
}

// WriteText implements platform.Clipboard.
func (c *Clipboard) WriteText(ctx context.Context, text string) error {
	c.mu.Lock()
	c.text = text
	c.mu.Unlock()
	return nil
}

// Audio has one default input and output device. Capture sends the queued
// frames and then closes its channel, as if the microphone was unplugged
// (or, after KeepOpen, keeps it open until its context ends); Play records
// what it played.
type Audio struct {
	mu       sync.Mutex
	frames   []audio.Frame
	played   []audio.Clip
	keepOpen bool
	opened   int
	// queued and delivered count frames queued and taken (or dropped) by
	// a capture, so a test can wait until the microphone has been read.
	queued, delivered int
}

// Undelivered returns how many queued frames no capture's reader has taken
// yet.
func (a *Audio) Undelivered() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queued - a.delivered
}

// KeepOpen makes later Captures keep their channel open after the queued
// frames, until their context ends, as a real microphone does.
func (a *Audio) KeepOpen() {
	a.mu.Lock()
	a.keepOpen = true
	a.mu.Unlock()
}

// Opened returns how many times Capture has opened the microphone.
func (a *Audio) Opened() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opened
}

var (
	defaultIn  = platform.Device{ID: "fake-mic", Name: "Fake microphone", Default: true}
	defaultOut = platform.Device{ID: "fake-speaker", Name: "Fake speaker", Default: true}
)

// QueueCapture adds frames for the next Capture to send.
func (a *Audio) QueueCapture(frames ...audio.Frame) {
	a.mu.Lock()
	a.frames = append(a.frames, frames...)
	a.queued += len(frames)
	a.mu.Unlock()
}

// Inputs implements platform.Audio.
func (a *Audio) Inputs(ctx context.Context) ([]platform.Device, error) {
	return []platform.Device{defaultIn}, nil
}

// Outputs implements platform.Audio.
func (a *Audio) Outputs(ctx context.Context) ([]platform.Device, error) {
	return []platform.Device{defaultOut}, nil
}

// Capture implements platform.Audio. Frames keep the format they were queued
// with; f only has to be valid.
func (a *Audio) Capture(ctx context.Context, device string, f audio.Format) (<-chan audio.Frame, error) {
	if device != "" && device != defaultIn.ID {
		return nil, fmt.Errorf("input %q: %w", device, platform.ErrNotFound)
	}
	if !f.Valid() {
		return nil, fmt.Errorf("fake audio: invalid format %+v", f)
	}
	a.mu.Lock()
	frames := a.frames
	a.frames = nil
	a.opened++
	keep := a.keepOpen
	a.mu.Unlock()
	ch := make(chan audio.Frame)
	go func() {
		defer close(ch)
		for i, f := range frames {
			select {
			case ch <- f:
				a.mu.Lock()
				a.delivered++
				a.mu.Unlock()
			case <-ctx.Done():
				// Closed before they were read: they are gone, not pending.
				a.mu.Lock()
				a.delivered += len(frames) - i
				a.mu.Unlock()
				return
			}
		}
		if keep {
			<-ctx.Done()
		}
	}()
	return ch, nil
}

// Play implements platform.Audio.
func (a *Audio) Play(ctx context.Context, device string, s audio.Stream) error {
	if device != "" && device != defaultOut.ID {
		_ = s.Close()
		return fmt.Errorf("output %q: %w", device, platform.ErrNotFound)
	}
	clip, err := audio.ReadAll(s)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.played = append(a.played, clip)
	a.mu.Unlock()
	return ctx.Err()
}

// Played returns every clip played so far, in order.
func (a *Audio) Played() []audio.Clip {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.played)
}

// Service records whether it is installed.
type Service struct {
	mu        sync.Mutex
	installed bool
}

// Install implements platform.Service.
func (s *Service) Install(ctx context.Context) error { return s.set(true) }

// Uninstall implements platform.Service.
func (s *Service) Uninstall(ctx context.Context) error { return s.set(false) }

func (s *Service) set(v bool) error {
	s.mu.Lock()
	s.installed = v
	s.mu.Unlock()
	return nil
}

// Installed implements platform.Service.
func (s *Service) Installed(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed, nil
}

// Secrets is an in-memory keychain.
type Secrets struct {
	mu sync.Mutex
	m  map[string]string
}

// Get implements platform.Secrets.
func (s *Secrets) Get(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return "", fmt.Errorf("secret %q: %w", key, platform.ErrNotFound)
	}
	return v, nil
}

// Set implements platform.Secrets.
func (s *Secrets) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	s.m[key] = value
	s.mu.Unlock()
	return nil
}

// Delete implements platform.Secrets.
func (s *Secrets) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
	return nil
}

// Paths reports directories under Root. Nothing is created on disk.
type Paths struct {
	// Root is the parent of every directory.
	Root string
}

// Config implements platform.Paths.
func (p *Paths) Config() string { return filepath.Join(p.Root, "config") }

// Data implements platform.Paths.
func (p *Paths) Data() string { return filepath.Join(p.Root, "data") }

// Cache implements platform.Paths.
func (p *Paths) Cache() string { return filepath.Join(p.Root, "cache") }

// Runtime implements platform.Paths.
func (p *Paths) Runtime() string { return filepath.Join(p.Root, "runtime") }
