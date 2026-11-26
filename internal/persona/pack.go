package persona

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// Limits on what a pack may contain. Packs come from the internet.
const (
	// MaxManifestBytes caps persona.toml.
	MaxManifestBytes = 64 << 10
	// MaxStyleBytes caps style.md.
	MaxStyleBytes = 8 << 10
	// MaxLinesBytes caps one lines.<lang>.toml.
	MaxLinesBytes = 64 << 10
	// MaxLineRunes caps one spoken line.
	MaxLineRunes = 240
	// MaxLinesPerKind caps the lines of one kind.
	MaxLinesPerKind = 64
	// MaxPackBytes caps the whole pack, voice weights included.
	MaxPackBytes = 512 << 20
	// MaxPackFiles caps the number of files in a pack.
	MaxPackFiles = 2000
)

// Manifest and style file names.
const (
	ManifestFile = "persona.toml"
	DefaultStyle = "style.md"
)

var (
	idRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	langRe  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	enginRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// catalogRe matches a models-catalog ID, which has no path separator or
	// file extension.
	catalogRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// Pack is a loaded, validated persona pack. It is read-only once loaded.
type Pack struct {
	ID          string
	Name        string
	Description string
	Version     string
	License     string
	Languages   []string

	Wake        Wake
	AddressForm AddressSpec
	Voice       Voice
	Style       Style

	styleText string
	lines     map[string]*lineSet // by language
	fsys      fs.FS
}

// Wake is how the persona is called.
type Wake struct {
	// Words are the phrases that wake this persona ("ashford", "hey ashford").
	Words []string
	// Model is an optional pack-relative wake-word model.
	Model string
	// Sensitivity is 0..1; 0.5 when unset.
	Sensitivity float64
}

// AddressSpec is how the persona addresses the user (design §27).
type AddressSpec struct {
	// Form is the template, "{title} {name}" by default.
	Form string
	// Title is the default title ("Master"); users override it in config.
	Title string
	// Fallback is used until the user has set a name.
	Fallback string
}

// Voice names the persona's voice as engine + model + speaker. The field names
// match what the TTS engines (internal/speech/sherpa) read.
type Voice struct {
	// Engine is the local TTS engine: "kokoro" or "piper" at launch. The
	// loader accepts any lowercase identifier so new engines need no change
	// here; the speech layer rejects ones it lacks.
	Engine string
	// Model is a pack-relative file ("voice/ashford.onnx") or, when it has no
	// slash and no extension, a models-catalog ID ("kokoro-multi-lang-v1_0")
	// so a stock interim voice needs no file in the pack. See ModelIsPath.
	Model string
	// Speaker picks a voice inside a multi-speaker model, by name or index.
	// Empty means the model's default.
	Speaker string
	// Speed scales the speaking rate; 1.0 when unset.
	Speed float64
	// Cloud is an optional premium cloud voice.
	Cloud *CloudVoice
}

// CloudVoice is an optional premium voice from a cloud provider.
type CloudVoice struct {
	Provider string
	VoiceID  string
}

// ModelIsPath reports whether Model names a file in the pack rather than a
// catalog ID.
func (v Voice) ModelIsPath() bool { return strings.Contains(v.Model, "/") || path.Ext(v.Model) != "" }

// Style is the persona's manner, as settings plus the text of style.md.
type Style struct {
	// Formality and Humor are 0..1.
	Formality float64
	Humor     float64
	// Verbosity is brief, normal or chatty.
	Verbosity string
	// Quips is never, sometimes or often (user-overridable).
	Quips string
}

// Open opens a file inside the pack by its pack-relative name, for the voice
// model, wake model and clips. It refuses names that escape the pack.
func (p *Pack) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, fmt.Errorf("persona %s: path %q is not inside the pack", p.ID, name)
	}
	return p.fsys.Open(name)
}

// FS returns the pack's files, rooted at the pack folder.
func (p *Pack) FS() fs.FS { return p.fsys }

// ValidID reports whether id is usable as a persona ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

// A ValidationError lists everything wrong with a pack.
type ValidationError struct {
	Pack   string
	Issues []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("persona pack %s is invalid: %s", e.Pack, strings.Join(e.Issues, "; "))
}

type issues struct{ list []string }

func (i *issues) addf(format string, a ...any) { i.list = append(i.list, fmt.Sprintf(format, a...)) }

func (i *issues) err(pack string) error {
	if len(i.list) == 0 {
		return nil
	}
	return &ValidationError{Pack: pack, Issues: i.list}
}

// packPath checks a pack-relative path from a manifest.
func packPath(is *issues, field, p string) bool {
	if p == "" {
		return false
	}
	if !fs.ValidPath(p) || p == "." || strings.Contains(p, `\`) {
		is.addf("%s: %q must be a relative path inside the pack", field, p)
		return false
	}
	return true
}
