// Package clipbank is the persona's bank of short spoken lines: the
// acknowledgements, status lines and the like (design §27 "Clips"). Each
// line is rendered once through the text-to-speech engine in the persona's
// voice, cached on disk as a WAV file named by a hash of everything that
// affects its sound, and played from memory, so an acknowledgement is
// instant while the fast path or the AI works.
//
// Lines the pack ships as clips are used as they are. Lines with {address}
// are rendered with the user's address; they are re-rendered in the
// background when the address, the persona or the voice changes, and until a
// clip is ready the lines without {address} are used, so an acknowledgement
// is never late.
//
// The daemon builds one Bank, calls Set with the active persona, voice and
// address (again whenever the configuration changes), and hands the Bank to
// the pipeline as its acknowledgement source (it implements Ack).
package clipbank

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/internal/speech"
)

// Kinds are the kinds of line the bank holds: the short ones. Quips are
// longer and are spoken through the TTS when wanted.
var Kinds = []persona.Kind{persona.Ack, persona.Working, persona.Done, persona.Confirm, persona.Error}

// maxClip caps how long a rendered or shipped clip may be.
const maxClip = 15 * time.Second

// ShippedName is the pack-relative name of a shipped clip for a line: the
// language and kind of the lines file and the line's index in it, such as
// "clips/en.ack.0.wav". Only lines without {address} can ship.
func ShippedName(lang string, kind persona.Kind, index int) string {
	return fmt.Sprintf("clips/%s.%s.%d.wav", lang, kind, index)
}

// Options configures a Bank.
type Options struct {
	// Dir is the folder the clips are cached in, such as <cache>/clips. The
	// bank keeps one subfolder per persona. It is created on first use.
	Dir string
	// TTS renders lines. Nil means only shipped clips and cached ones are
	// used. It must run on the CPU: the bank never needs the GPU.
	TTS speech.TTS
	// Rand picks among clips: Rand(n) is in [0, n). Default math/rand/v2.
	Rand func(n int) int
	// Log receives render problems. Default slog.Default.
	Log *slog.Logger
}

// Target is what the bank should hold clips for.
type Target struct {
	// Pack is the persona.
	Pack *persona.Pack
	// Voice is how to speak, built from the pack's voice section.
	Voice speech.Voice
	// Address is how the persona addresses the user (Pack.Address).
	Address string
	// Lang is the language of the lines; empty means "en".
	Lang string
	// Salt is mixed into every clip's name, so a change the bank cannot
	// see (a new voice model file at the same path) forces a re-render.
	Salt string
}

// State is how ready a clip is.
type State string

// Clip states.
const (
	// Pending clips are waiting to be rendered.
	Pending State = "pending"
	// Cached clips were found on disk or have been rendered.
	Cached State = "cached"
	// Shipped clips come from the pack.
	Shipped State = "shipped"
	// Failed clips could not be rendered; Entry.Err says why.
	Failed State = "failed"
)

// Entry describes one line of the bank.
type Entry struct {
	Kind persona.Kind
	// Index is the line's index within its kind.
	Index int
	// Text is the line as it is spoken, with {address} filled.
	Text string
	// Personalized is true for a line that has {address}.
	Personalized bool
	State        State
	// Path is the cache file, or the pack file for a shipped clip.
	Path string
	Err  string
}

// Ready reports whether the clip can play.
func (e Entry) Ready() bool { return e.State == Cached || e.State == Shipped }

type slot struct {
	Entry
	key   string
	clip  *audio.Clip // held in memory (always for acknowledgements)
	fsys  fs.FS       // set for a shipped clip
	voice speech.Voice
}

// Bank holds the clips. It is safe for concurrent use.
type Bank struct {
	o Options

	mu     sync.Mutex
	gen    int
	cancel context.CancelFunc
	done   chan struct{} // closed when the current background pass ends
	t      Target
	slots  []*slot
	last   map[persona.Kind]string
}

// New returns an empty Bank. Call Set to give it a persona.
func New(o Options) *Bank {
	if o.Rand == nil {
		o.Rand = rand.IntN
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Bank{o: o, last: map[persona.Kind]string{}}
}

// Set points the bank at a persona, voice and address, and starts loading and
// rendering its clips in the background. It returns at once, and cancels any
// earlier pass. Clips that are already on disk are reused; only the ones whose
// text, voice, speed or persona changed are rendered. Until a line is ready
// the bank plays what it has.
func (b *Bank) Set(t Target) {
	if t.Pack == nil {
		return
	}
	if t.Lang == "" {
		t.Lang = "en"
	}
	slots := b.plan(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	prev := b.done
	b.gen++
	gen := b.gen
	b.cancel, b.done, b.t, b.slots = cancel, done, t, slots
	b.mu.Unlock()

	go func() {
		defer close(done)
		if prev != nil {
			<-prev // passes never overlap, so the TTS is used by one at a time
		}
		b.work(ctx, gen, t, slots)
	}()
}

// Close stops the background pass.
func (b *Bank) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
}

// Wait blocks until the current background pass has loaded and rendered
// everything it can, or ctx ends.
func (b *Bank) Wait(ctx context.Context) error {
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	if done == nil {
		return errors.New("clipbank: no persona set")
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Render waits for the current pass and returns an error naming the lines
// that could not be rendered, or nil when every line is ready. It is for the
// command that pre-renders the bank.
func (b *Bank) Render(ctx context.Context) error {
	if err := b.Wait(ctx); err != nil {
		return err
	}
	var bad []string
	for _, e := range b.Entries() {
		if !e.Ready() {
			bad = append(bad, fmt.Sprintf("%s[%d] (%s): %s", e.Kind, e.Index, e.Text, e.Err))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("clipbank: %d lines not ready: %s", len(bad), strings.Join(bad, "; "))
	}
	return nil
}

// Entries lists every line of the bank in kind and line order.
func (b *Bank) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, len(b.slots))
	for i, s := range b.slots {
		out[i] = s.Entry
	}
	return out
}

// Dir returns the folder holding the clips of the current persona, or "".
func (b *Bank) Dir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.t.Pack == nil {
		return ""
	}
	return b.personaDir(b.t.Pack)
}

func (b *Bank) personaDir(p *persona.Pack) string { return filepath.Join(b.o.Dir, p.ID) }

// plan lists the slots for t without touching the disk.
func (b *Bank) plan(t Target) []*slot {
	var out []*slot
	for _, k := range Kinds {
		for _, l := range t.Pack.Lines(k, t.Lang) {
			text := l.Render(t.Address)
			s := &slot{Entry: Entry{Kind: k, Index: l.Index, Text: text, Personalized: l.HasAddress(), State: Pending}, voice: t.Voice}
			s.key = key(t, text)
			s.Path = filepath.Join(b.personaDir(t.Pack), s.key+".wav")
			out = append(out, s)
		}
	}
	return out
}

// key names a clip by everything that changes how it sounds.
func key(t Target, text string) string {
	h := sha256.New()
	for _, s := range []string{
		"clipbank-v1", text, t.Pack.ID, t.Pack.Version, t.Voice.ID, t.Voice.Speaker,
		t.Voice.Language, strconv.FormatFloat(float64(t.Voice.Speed), 'g', -1, 32), t.Salt,
	} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// update changes slot s under the lock if the pass is still current.
func (b *Bank) update(gen int, s *slot, f func(*slot)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gen == gen {
		f(s)
	}
}

func (b *Bank) work(ctx context.Context, gen int, t Target, slots []*slot) {
	dir := b.personaDir(t.Pack)
	langs := []string{strings.ToLower(t.Lang), "en"}
	if base, _, ok := strings.Cut(langs[0], "-"); ok {
		langs = []string{langs[0], base, "en"}
	}

	// First pass: what is already there. Shipped clips, then the cache, so
	// acknowledgements are ready before anything is rendered.
	for _, s := range slots {
		if ctx.Err() != nil {
			return
		}
		if !s.Personalized {
			if c, name, ok := shipped(t.Pack, langs, s.Kind, s.Index); ok {
				b.update(gen, s, func(s *slot) {
					s.State, s.Path, s.fsys = Shipped, name, t.Pack.FS()
					if s.Kind == persona.Ack {
						s.clip = &c
					}
				})
				continue
			}
		}
		if c, err := audio.ReadWAVFile(s.Path); err == nil && validClip(c) {
			b.update(gen, s, func(s *slot) {
				s.State = Cached
				if s.Kind == persona.Ack {
					s.clip = &c
				}
			})
		}
	}

	// Second pass: render what is missing. Static lines first within a kind
	// (they never need re-rendering), acknowledgements before the rest.
	complete := true
	for _, s := range slots {
		if ctx.Err() != nil {
			return
		}
		if s.State != Pending && s.State != Failed {
			continue
		}
		if b.o.TTS == nil {
			complete = false
			b.update(gen, s, func(s *slot) { s.State, s.Err = Failed, "no text-to-speech engine" })
			continue
		}
		c, err := b.render(ctx, s.Text, t.Voice)
		if err == nil {
			err = writeClip(s.Path, c)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			complete = false
			b.o.Log.Warn("clip bank: render failed", "persona", t.Pack.ID, "kind", s.Kind, "err", err)
			b.update(gen, s, func(s *slot) { s.State, s.Err = Failed, err.Error() })
			continue
		}
		b.update(gen, s, func(s *slot) {
			s.State, s.Err = Cached, ""
			if s.Kind == persona.Ack {
				s.clip = &c
			}
		})
	}
	if complete && ctx.Err() == nil {
		prune(dir, slots)
	}
}

func shipped(p *persona.Pack, langs []string, kind persona.Kind, index int) (audio.Clip, string, bool) {
	for _, l := range langs {
		name := ShippedName(l, kind, index)
		f, err := p.Open(name)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, 16<<20))
		_ = f.Close()
		if err != nil {
			continue
		}
		c, err := audio.DecodeWAV(bytes.NewReader(b))
		if err != nil || !validClip(c) {
			continue
		}
		return c, name, true
	}
	return audio.Clip{}, "", false
}

func validClip(c audio.Clip) bool {
	return len(c.Samples) > 0 && c.Format.Valid() && c.Duration() <= maxClip
}

// render speaks text and returns the audio with leading and trailing silence
// trimmed (a clip that starts with silence is late).
func (b *Bank) render(ctx context.Context, text string, v speech.Voice) (audio.Clip, error) {
	st, err := b.o.TTS.Speak(ctx, text, v)
	if err != nil {
		return audio.Clip{}, err
	}
	defer func() { _ = st.Close() }()
	c := audio.Clip{Format: st.Format()}
	for {
		f, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return audio.Clip{}, err
		}
		c.Samples = append(c.Samples, f.Samples...)
		if c.Duration() > maxClip {
			return audio.Clip{}, fmt.Errorf("rendered clip is longer than %v", maxClip)
		}
	}
	c = trim(c)
	if !validClip(c) {
		return audio.Clip{}, errors.New("the voice produced no audio")
	}
	return c, nil
}

// trim drops near-silence from both ends, keeping a short margin.
func trim(c audio.Clip) audio.Clip {
	const floor = 0.003
	keep := c.Format.Samples(15 * time.Millisecond)
	first, last := -1, -1
	for i, s := range c.Samples {
		if s > floor || s < -floor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return audio.Clip{Format: c.Format}
	}
	first = max(0, first-keep)
	last = min(len(c.Samples), last+keep)
	c.Samples = c.Samples[first:last]
	return c
}

func writeClip(path string, c audio.Clip) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".clip-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = audio.EncodeWAV(f, c)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// prune removes clips of this persona that no line names any more (an old
// address, voice or text), once every line is ready.
func prune(dir string, slots []*slot) {
	want := map[string]bool{}
	for _, s := range slots {
		want[filepath.Base(s.Path)] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && len(n) == 36 && strings.HasSuffix(n, ".wav") && !want[n] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// Pick returns a random ready clip of kind, never the same line twice in a row
// when there is a choice, and the text it says. It does not block on rendering;
// false means nothing of that kind is ready. Lines with {address} are
// included once they have been rendered with the user's address.
func (b *Bank) Pick(kind persona.Kind) (audio.Clip, string, bool) {
	b.mu.Lock()
	var cands []*slot
	for _, s := range b.slots {
		if s.Kind == kind && s.Ready() {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		b.mu.Unlock()
		return audio.Clip{}, "", false
	}
	if len(cands) > 1 {
		var rest []*slot
		for _, s := range cands {
			if s.Text != b.last[kind] {
				rest = append(rest, s)
			}
		}
		cands = rest
	}
	s := cands[b.o.Rand(len(cands))]
	b.last[kind] = s.Text
	clip, fsys, path := s.clip, s.fsys, s.Path
	b.mu.Unlock()

	if clip != nil {
		return *clip, s.Text, true
	}
	var c audio.Clip
	var err error
	if fsys != nil {
		var f fs.File
		if f, err = fsys.Open(path); err == nil {
			c, err = audio.DecodeWAV(f)
			_ = f.Close()
		}
	} else {
		c, err = audio.ReadWAVFile(path)
	}
	if err != nil || !validClip(c) {
		return audio.Clip{}, "", false
	}
	return c, s.Text, true
}

// Ack returns a random acknowledgement clip; it implements core.AckSource.
func (b *Bank) Ack() (audio.Clip, bool) {
	c, _, ok := b.Pick(persona.Ack)
	return c, ok
}
