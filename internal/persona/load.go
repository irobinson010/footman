package persona

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	toml "github.com/pelletier/go-toml/v2"
)

// manifest mirrors persona.toml. Decoding is strict, so a key that is not
// listed here (a "tools" or "permissions" table, say) is an error.
type manifest struct {
	Persona struct {
		ID          string   `toml:"id"`
		Name        string   `toml:"name"`
		Description string   `toml:"description"`
		Version     string   `toml:"version"`
		License     string   `toml:"license"`
		Languages   []string `toml:"languages"`
	} `toml:"persona"`
	Wake struct {
		Words       []string `toml:"words"`
		Model       string   `toml:"model"`
		Sensitivity *float64 `toml:"sensitivity"`
	} `toml:"wake"`
	Address struct {
		Form     string `toml:"form"`
		Title    string `toml:"title"`
		Fallback string `toml:"fallback"`
	} `toml:"address"`
	Voice struct {
		Engine  string   `toml:"engine"`
		Model   string   `toml:"model"`
		Speaker any      `toml:"speaker"`
		Speed   *float64 `toml:"speed"`
		Cloud   *struct {
			Provider string `toml:"provider"`
			VoiceID  string `toml:"voice_id"`
		} `toml:"cloud"`
	} `toml:"voice"`
	Style struct {
		Prompt    string   `toml:"prompt"`
		Formality *float64 `toml:"formality"`
		Humor     *float64 `toml:"humor"`
		Verbosity string   `toml:"verbosity"`
		Quips     string   `toml:"quips"`
	} `toml:"style"`
}

// LoadDir loads and validates the pack in dir. Symbolic links anywhere in the
// pack are refused, so a pack cannot point outside itself.
func LoadDir(dir string) (*Pack, error) {
	st, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("persona pack %s is not a folder", dir)
	}
	return Load(os.DirFS(dir), dir)
}

// Load loads and validates the pack rooted at fsys. name labels errors.
func Load(fsys fs.FS, name string) (*Pack, error) {
	is := &issues{}
	if err := checkTree(fsys); err != nil {
		return nil, &ValidationError{Pack: name, Issues: []string{err.Error()}}
	}
	raw, err := readLimited(fsys, ManifestFile, MaxManifestBytes)
	if err != nil {
		return nil, &ValidationError{Pack: name, Issues: []string{err.Error()}}
	}
	var m manifest
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		var se *toml.StrictMissingError
		if asStrict(err, &se) {
			for _, d := range se.Errors {
				row, col := d.Position()
				is.addf("persona.toml:%d:%d: unknown key %s (a pack may only use the documented fields)", row, col, strings.Join(d.Key(), "."))
			}
			return nil, is.err(name)
		}
		return nil, &ValidationError{Pack: name, Issues: []string{"persona.toml: " + err.Error()}}
	}
	p := &Pack{fsys: fsys, lines: map[string]*lineSet{}}
	p.fill(&m, is)

	// style.md
	stylePath := m.Style.Prompt
	if stylePath == "" {
		stylePath = DefaultStyle
	}
	if packPath(is, "style.prompt", stylePath) {
		b, err := readLimited(fsys, stylePath, MaxStyleBytes)
		switch {
		case err != nil:
			is.addf("style: %v", err)
		case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
			is.addf("%s: must be UTF-8 text", stylePath)
		case strings.TrimSpace(string(b)) == "":
			is.addf("%s: is empty", stylePath)
		default:
			p.styleText = strings.TrimSpace(string(b))
		}
	}

	// lines.<lang>.toml
	for _, lang := range p.Languages {
		b, err := readLimited(fsys, "lines."+lang+".toml", MaxLinesBytes)
		if err != nil {
			is.addf("lines.%s.toml: %v", lang, err)
			continue
		}
		ls, errs := parseLines(b)
		for _, e := range errs {
			is.addf("lines.%s.toml: %s", lang, e)
		}
		if ls != nil {
			p.lines[lang] = ls
		}
	}
	// An English file is the fallback for every other language.
	if _, ok := p.lines["en"]; !ok && len(p.Languages) > 0 && !contains(p.Languages, "en") {
		is.addf("languages must include \"en\", the fallback language")
	}

	// Files named in the manifest must exist.
	for _, f := range []struct{ field, p string }{
		{"wake.model", p.Wake.Model},
		{"voice.model (path)", pathModel(p.Voice)},
	} {
		if f.p == "" {
			continue
		}
		if packPath(is, f.field, f.p) {
			if st, err := fs.Stat(fsys, f.p); err != nil || st.IsDir() {
				is.addf("%s: %q does not exist in the pack", f.field, f.p)
			}
		}
	}
	if err := is.err(name); err != nil {
		return nil, err
	}
	return p, nil
}

func pathModel(v Voice) string {
	if v.ModelIsPath() {
		return v.Model
	}
	return ""
}

func asStrict(err error, target **toml.StrictMissingError) bool {
	return errors.As(err, target)
}

func (p *Pack) fill(m *manifest, is *issues) {
	pp := m.Persona
	p.ID, p.Name, p.Description, p.Version, p.License = pp.ID, strings.TrimSpace(pp.Name), strings.TrimSpace(pp.Description), pp.Version, pp.License
	if !idRe.MatchString(p.ID) {
		is.addf("persona.id: %q must be 1-32 lowercase letters, digits or hyphens, starting with a letter", p.ID)
	}
	if p.Name == "" || len([]rune(p.Name)) > 64 {
		is.addf("persona.name: required, at most 64 characters")
	}
	if len([]rune(p.Description)) > 300 {
		is.addf("persona.description: at most 300 characters")
	}
	if p.Version == "" {
		is.addf("persona.version: required")
	}
	if p.License == "" {
		is.addf("persona.license: required")
	}
	p.Languages = pp.Languages
	if len(p.Languages) == 0 {
		p.Languages = []string{"en"}
	}
	seen := map[string]bool{}
	for _, l := range p.Languages {
		if !langRe.MatchString(l) {
			is.addf("persona.languages: %q is not a language code", l)
		}
		if seen[l] {
			is.addf("persona.languages: %q is listed twice", l)
		}
		seen[l] = true
	}

	// wake
	p.Wake = Wake{Words: m.Wake.Words, Model: m.Wake.Model, Sensitivity: 0.5}
	if m.Wake.Sensitivity != nil {
		p.Wake.Sensitivity = *m.Wake.Sensitivity
	}
	if n := len(p.Wake.Words); n == 0 || n > 8 {
		is.addf("wake.words: give between 1 and 8 phrases")
	}
	for i, w := range p.Wake.Words {
		w = strings.ToLower(strings.TrimSpace(w))
		p.Wake.Words[i] = w
		if w == "" || len([]rune(w)) > 40 {
			is.addf("wake.words: each phrase must be 1-40 characters")
		}
	}
	if p.Wake.Sensitivity < 0 || p.Wake.Sensitivity > 1 {
		is.addf("wake.sensitivity: %v must be between 0 and 1", p.Wake.Sensitivity)
	}

	// address
	a := m.Address
	p.AddressForm = AddressSpec{Form: a.Form, Title: strings.TrimSpace(a.Title), Fallback: strings.TrimSpace(a.Fallback)}
	if p.AddressForm.Form == "" {
		p.AddressForm.Form = "{title} {name}"
	}
	if rest := stripPlaceholders(p.AddressForm.Form, "title", "name"); strings.ContainsAny(rest, "{}") {
		is.addf("address.form: %q may use only {title} and {name}", p.AddressForm.Form)
	}
	if !strings.Contains(p.AddressForm.Form, "{name}") {
		is.addf("address.form: %q must contain {name}", p.AddressForm.Form)
	}
	if p.AddressForm.Fallback == "" {
		is.addf("address.fallback: required (used until the user sets a name)")
	}
	if len([]rune(p.AddressForm.Title)) > 32 || len([]rune(p.AddressForm.Fallback)) > 32 || len([]rune(p.AddressForm.Form)) > 64 {
		is.addf("address: title, fallback and form are too long")
	}

	// voice
	v := m.Voice
	p.Voice = Voice{Engine: v.Engine, Model: v.Model, Speed: 1}
	if !enginRe.MatchString(v.Engine) {
		is.addf("voice.engine: %q must be a lowercase engine name such as \"kokoro\" or \"piper\"", v.Engine)
	}
	if v.Model == "" {
		is.addf("voice.model: required (a pack-relative path or a models-catalog ID)")
	} else if !p.Voice.ModelIsPath() && !catalogRe.MatchString(v.Model) {
		is.addf("voice.model: %q is neither a pack path nor a catalog ID", v.Model)
	}
	switch s := v.Speaker.(type) {
	case nil:
	case string:
		p.Voice.Speaker = strings.TrimSpace(s)
		if len([]rune(p.Voice.Speaker)) > 64 {
			is.addf("voice.speaker: too long")
		}
	case int64:
		if s < 0 || s > 10000 {
			is.addf("voice.speaker: index %d out of range", s)
		}
		p.Voice.Speaker = fmt.Sprint(s)
	default:
		is.addf("voice.speaker: must be a name or an index")
	}
	if v.Speed != nil {
		p.Voice.Speed = *v.Speed
	}
	if p.Voice.Speed < 0.5 || p.Voice.Speed > 2 {
		is.addf("voice.speed: %v must be between 0.5 and 2", p.Voice.Speed)
	}
	if v.Cloud != nil {
		p.Voice.Cloud = &CloudVoice{Provider: v.Cloud.Provider, VoiceID: v.Cloud.VoiceID}
		if !enginRe.MatchString(v.Cloud.Provider) {
			is.addf("voice.cloud.provider: %q must be a lowercase provider name", v.Cloud.Provider)
		}
	}

	// style settings
	s := m.Style
	p.Style = Style{Formality: 0.5, Humor: 0.3, Verbosity: "brief", Quips: "sometimes"}
	if s.Formality != nil {
		p.Style.Formality = *s.Formality
	}
	if s.Humor != nil {
		p.Style.Humor = *s.Humor
	}
	if p.Style.Formality < 0 || p.Style.Formality > 1 || p.Style.Humor < 0 || p.Style.Humor > 1 {
		is.addf("style.formality and style.humor must be between 0 and 1")
	}
	if s.Verbosity != "" {
		p.Style.Verbosity = s.Verbosity
	}
	if !contains([]string{"brief", "normal", "chatty"}, p.Style.Verbosity) {
		is.addf("style.verbosity: %q must be brief, normal or chatty", p.Style.Verbosity)
	}
	if s.Quips != "" {
		p.Style.Quips = s.Quips
	}
	if !contains(QuipLevels, p.Style.Quips) {
		is.addf("style.quips: %q must be never, sometimes or often", p.Style.Quips)
	}
}

// QuipLevels are the valid values of style.quips and of the
// personas.overrides.<id>.quips setting.
var QuipLevels = []string{"never", "sometimes", "often"}

// stripPlaceholders removes the named {placeholders} from s.
func stripPlaceholders(s string, names ...string) string {
	for _, n := range names {
		s = strings.ReplaceAll(s, "{"+n+"}", "")
	}
	return s
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// readLimited reads a regular file of at most max bytes.
func readLimited(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is missing", name)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", name, st.Size(), max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return b, nil
}

// checkTree refuses links and special files and caps the pack's size.
func checkTree(fsys fs.FS) error {
	var total int64
	files := 0
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; packs may not contain links", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		files++
		if files > MaxPackFiles {
			return fmt.Errorf("pack has more than %d files", MaxPackFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > MaxPackBytes {
			return fmt.Errorf("pack is larger than %d bytes", int64(MaxPackBytes))
		}
		return nil
	})
}

// Files lists the pack's files, sorted, for display and copying.
func (p *Pack) Files() []string {
	var out []string
	_ = fs.WalkDir(p.fsys, ".", func(n string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, path.Clean(n))
		}
		return nil
	})
	sort.Strings(out)
	return out
}
