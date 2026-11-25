package platform

import (
	"context"
	"errors"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
)

// Platform is everything Footman needs from the operating system. Each method
// returns a long-lived sub-interface; implementations may return the same
// value every time.
type Platform interface {
	// Input covers hotkeys, push-to-talk and typing or pasting at the cursor.
	Input() Input
	// Windows covers the active app and listing or focusing windows.
	Windows() Windows
	// Launcher opens URLs, apps and files.
	Launcher() Launcher
	// Notifier shows notifications and the tray status.
	Notifier() Notifier
	// Clipboard reads and writes the clipboard.
	Clipboard() Clipboard
	// Audio lists and opens microphone and speaker devices.
	Audio() Audio
	// Service installs or removes the per-user background service.
	Service() Service
	// Secrets stores secrets in the OS keychain.
	Secrets() Secrets
	// Paths returns the per-user config, data, cache and runtime directories.
	Paths() Paths
}

// Platforms holds the platform implementations, keyed by name ("linux",
// "windows", "darwin", "fake"). Only the current OS's implementation is
// compiled in, plus fake in tests.
var Platforms = registry.New[Platform]("platform")

// ErrUnsupported is returned (wrapped) by a method the current platform or
// desktop cannot do, such as listing windows on a compositor that hides them.
var ErrUnsupported = errors.New("platform: not supported here")

// ErrNotFound is returned (wrapped) when a named secret, window or device
// does not exist.
var ErrNotFound = errors.New("platform: not found")

// HotkeyEvent reports a registered hotkey being pressed or released.
type HotkeyEvent struct {
	// Hotkey is the combination as registered, such as "Super+Space".
	Hotkey string
	// Pressed is true on press and false on release, so push-to-talk can
	// hold while the key is down.
	Pressed bool
	// At is when it happened.
	At time.Time
}

// Input covers hotkeys and inserting text at the cursor. Only dictation
// inserts text; everything else acts through a target that does not touch the
// user's focus (design §7).
type Input interface {
	// Hotkeys registers the given global combinations (such as
	// "Super+Space") and sends their presses and releases on the returned
	// channel until ctx is done, when they are unregistered and the channel
	// is closed.
	Hotkeys(ctx context.Context, combos []string) (<-chan HotkeyEvent, error)
	// Type inserts text at the cursor by simulating key presses.
	Type(ctx context.Context, text string) error
	// Paste inserts text at the cursor through the clipboard, restoring the
	// previous clipboard contents afterwards.
	Paste(ctx context.Context, text string) error
}

// InputSetup is an optional extension of Input for desktops that need a
// one-time setup step. Setup checks for it with a type assertion.
type InputSetup interface {
	// SetupInput installs what the desktop needs to identify Footman, such
	// as a .desktop file naming binary on Linux, and returns the files Input
	// creates now or later, for the install receipt.
	SetupInput(ctx context.Context, binary string) (InputFiles, error)
	// BindHotkeys binds each combination to exactly that key, replacing any
	// earlier binding, and reports what the desktop says each is bound to.
	// Setup calls it after asking the user for their key, and to rebind.
	BindHotkeys(ctx context.Context, combos []string) ([]HotkeyBinding, error)
	// PrepareInsert gets the desktop's permission to insert text, and opens
	// what inserting needs, so the first dictation neither asks nor waits.
	// Setup calls it so that a permission dialog appears then (on Linux,
	// the RemoteDesktop portal's); the daemon calls it when it starts.
	PrepareInsert(ctx context.Context) error
}

// InputFiles lists files an Input installs or writes.
type InputFiles struct {
	// Program files identify Footman to the desktop, such as a .desktop
	// file. They go when the program is uninstalled.
	Program []string
	// State files are written while Footman runs, such as a saved
	// permission token. They go with the data.
	State []string
}

// HotkeyBinding is what the desktop bound a hotkey to.
type HotkeyBinding struct {
	// Hotkey is the combination as asked for, such as "F8".
	Hotkey string
	// Trigger is the desktop's description of the key it bound, such as
	// "F8", or "" if the hotkey has no key.
	Trigger string
}

// Window is one top-level window.
type Window struct {
	// ID identifies the window to Focus. Its format is platform-specific.
	ID string
	// Title is the window title.
	Title string
	// App is the application identifier, such as "org.kde.konsole".
	App string
	// PID is the owning process, or 0 if unknown.
	PID int
}

// Windows covers the active window and listing or focusing windows.
type Windows interface {
	// Active returns the window that has focus.
	Active(ctx context.Context) (Window, error)
	// List returns the open top-level windows.
	List(ctx context.Context) ([]Window, error)
	// Focus raises and focuses the window with the given ID.
	Focus(ctx context.Context, id string) error
}

// Launcher opens things with the user's default handlers.
type Launcher interface {
	// OpenURL opens a URL in the default browser or handler.
	OpenURL(ctx context.Context, url string) error
	// OpenApp starts an application by its desktop ID or name.
	OpenApp(ctx context.Context, app string) error
	// OpenFile opens a file or folder with its default application.
	OpenFile(ctx context.Context, path string) error
}

// Action is a button on a notification.
type Action struct {
	// ID is reported back when the button is clicked.
	ID string
	// Label is the button text.
	Label string
}

// Notification is a desktop notification.
type Notification struct {
	// Title is the summary line.
	Title string
	// Body is the detail text.
	Body string
	// Actions are buttons. A click on one is a physical confirmation (design
	// §29).
	Actions []Action
}

// TrayStatus is what the tray icon shows, so the user can always see whether
// the microphone is live (design §29).
type TrayStatus int

// Tray statuses.
const (
	// MicOff means the microphone is closed.
	MicOff TrayStatus = iota + 1
	// WakeListening means only the on-device wake-word detector is listening.
	WakeListening
	// Recording means speech is being captured.
	Recording
)

// Notifier shows notifications and the tray status.
type Notifier interface {
	// Notify shows n. The returned channel receives the ID of the action the
	// user clicked, if any, and is closed when the notification is closed,
	// clicked or ctx is done.
	Notify(ctx context.Context, n Notification) (<-chan string, error)
	// SetTray updates the tray icon.
	SetTray(ctx context.Context, s TrayStatus) error
}

// Clipboard reads and writes the text clipboard.
type Clipboard interface {
	// ReadText returns the clipboard text.
	ReadText(ctx context.Context) (string, error)
	// WriteText replaces the clipboard contents with text.
	WriteText(ctx context.Context, text string) error
}

// Device is an audio input or output device.
type Device struct {
	// ID identifies the device to Capture or Play; "" means the default.
	ID string
	// Name is the human-readable name.
	Name string
	// Default marks the system default device.
	Default bool
}

// Audio lists and opens audio devices.
type Audio interface {
	// Inputs lists microphones.
	Inputs(ctx context.Context) ([]Device, error)
	// Outputs lists speakers.
	Outputs(ctx context.Context) ([]Device, error)
	// Capture opens a microphone ("" for the default) and sends frames in
	// format f until ctx is done, when it closes the device and the channel.
	Capture(ctx context.Context, device string, f audio.Format) (<-chan audio.Frame, error)
	// Play plays s on a speaker ("" for the default) and returns when it has
	// finished, failed or ctx is done. It closes s.
	Play(ctx context.Context, device string, s audio.Stream) error
}

// Service installs and removes the per-user background service (a systemd
// user unit on Linux) that runs `footman daemon` at login.
type Service interface {
	// Install installs and enables the service. It is idempotent.
	Install(ctx context.Context) error
	// Uninstall stops and removes the service. It is idempotent.
	Uninstall(ctx context.Context) error
	// Installed reports whether the service is installed.
	Installed(ctx context.Context) (bool, error)
}

// Secrets stores secrets in the OS keychain. Keys are the names used in
// config references, such as "claude" in `api_key = "keychain:claude"`.
type Secrets interface {
	// Get returns the secret stored under key, or an error wrapping
	// ErrNotFound.
	Get(ctx context.Context, key string) (string, error)
	// Set stores value under key, replacing any previous value.
	Set(ctx context.Context, key, value string) error
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
}

// Paths returns the per-user directories Footman uses (design §10). They are
// absolute and may not exist yet.
type Paths interface {
	// Config holds config.toml.
	Config() string
	// Data holds the database, plugins, personas and models.
	Data() string
	// Cache holds data that can be rebuilt, such as rendered voice clips.
	Cache() string
	// Runtime holds the API socket; it is private to the user.
	Runtime() string
}
