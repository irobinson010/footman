package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Migrations upgrade a raw decoded config from version N to N+1 before it is
// validated. Key N is the version being upgraded from. v1 is current, so the
// table is empty; future schema changes register a function here and bump
// CurrentVersion.
var Migrations = map[int64]func(raw map[string]any) error{}

// span locates one key's value in the source text.
type span struct {
	line       int // line of the key
	start, end int // byte range of the value
	inline     bool
}

// Doc is a config file's text plus an index of where each key's value sits.
// Edits are text splices, so everything outside the edited value (comments,
// order, spacing) stays byte-for-byte identical.
type Doc struct {
	src   []byte
	raw   map[string]any
	spans map[string]span
	// section[table path] is the offset just after the last line of that
	// [table] block (header or last key), where new keys are inserted.
	section map[string]int
	// lastRoot is the offset after the last root-level key/value, or -1.
	lastRoot int
}

// ParseDoc parses config text for editing. It checks TOML syntax only; use
// Parse to validate against the schema.
func ParseDoc(src []byte) (*Doc, error) { return newDoc(src) }

// Bytes returns the current text.
func (d *Doc) Bytes() []byte { return append([]byte{}, d.src...) }

func newDoc(src []byte) (*Doc, error) {
	d := &Doc{src: append([]byte{}, src...), raw: map[string]any{}, spans: map[string]span{}, section: map[string]int{}, lastRoot: -1}
	if err := toml.Unmarshal(d.src, &d.raw); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			line, _ := de.Position()
			return nil, Errors{{Line: line, Msg: de.Error()}}
		}
		return nil, Errors{{Msg: err.Error()}}
	}
	d.index()
	return d, nil
}

// index walks the syntax tree to record value spans and insertion points.
func (d *Doc) index() {
	var p unstable.Parser
	p.Reset(d.src)
	table := ""
	inArray := false
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			inArray = n.Kind == unstable.ArrayTable
			table = keyPath(n)
			if !inArray {
				d.section[table] = lineEnd(d.src, headerEnd(n))
			}
		case unstable.KeyValue:
			if inArray {
				continue
			}
			end := d.walk(&p, n, table)
			if table == "" {
				d.lastRoot = lineEnd(d.src, end)
			} else {
				d.section[table] = lineEnd(d.src, end)
			}
		}
	}
}

// walk records spans for a key/value and any inline-table members; it
// returns the end offset of the value.
func (d *Doc) walk(p *unstable.Parser, kv *unstable.Node, prefix string) int {
	path := join(prefix, keyPath(kv))
	start := int(kv.Raw.Offset)
	end := start + int(kv.Raw.Length)
	for end > start && (d.src[end-1] == ' ' || d.src[end-1] == '\t') {
		end--
	}
	// The value starts after the first '=' that follows the key.
	vs := start + bytes.IndexByte(d.src[start:end], '=') + 1
	for vs < end && (d.src[vs] == ' ' || d.src[vs] == '\t') {
		vs++
	}
	val := kv.Value()
	d.spans[path] = span{line: p.Shape(kv.Raw).Start.Line, start: vs, end: end, inline: val.Kind == unstable.InlineTable}
	if val.Kind == unstable.InlineTable {
		it := val.Children()
		for it.Next() {
			if c := it.Node(); c.Kind == unstable.KeyValue {
				d.walk(p, c, path)
			}
		}
	}
	return end
}

// headerEnd returns the offset just past a table header's last key part.
func headerEnd(n *unstable.Node) int {
	end := 0
	it := n.Key()
	for it.Next() {
		r := it.Node().Raw
		end = int(r.Offset + r.Length)
	}
	return end
}

func keyPath(n *unstable.Node) string {
	var parts []string
	it := n.Key()
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	return strings.Join(parts, ".")
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

// lineEnd returns the offset just after the newline ending the line that
// contains off-1 (or len(src) at end of file).
func lineEnd(src []byte, off int) int {
	i := bytes.IndexByte(src[off:], '\n')
	if i < 0 {
		return len(src)
	}
	return off + i + 1
}

// flatten turns the decoded tree into dotted leaf paths. Arrays are leaves.
func flatten(prefix string, m map[string]any, out map[string]any) {
	for k, v := range m {
		p := join(prefix, k)
		if sub, ok := v.(map[string]any); ok {
			flatten(p, sub, out)
		} else {
			out[p] = v
		}
	}
}

func (d *Doc) lineOf(path string) int {
	if s, ok := d.spans[path]; ok {
		return s.line
	}
	// Fall back to the nearest known parent.
	for i := strings.LastIndex(path, "."); i > 0; i = strings.LastIndex(path[:i], ".") {
		if s, ok := d.spans[path[:i]]; ok {
			return s.line
		}
	}
	return 0
}

// validate checks every value against the schema and builds a Config.
func (d *Doc) validate() (*Config, Errors) {
	raw := d.raw
	var errs Errors
	// Migrations run on the decoded data before validation.
	ver := int64(1)
	if v, ok := raw["version"]; ok {
		n, isInt := v.(int64)
		if !isInt {
			return nil, Errors{{Line: d.lineOf("version"), Key: "version", Msg: fmt.Sprintf("must be an integer, got %s", describe(v))}}
		}
		ver = n
	}
	if ver > CurrentVersion {
		return nil, Errors{{Line: d.lineOf("version"), Key: "version", Msg: fmt.Sprintf("config version %d is newer than this build supports (%d); upgrade footman", ver, CurrentVersion)}}
	}
	for ver >= 1 && ver < CurrentVersion {
		m, ok := Migrations[ver]
		if !ok {
			return nil, Errors{{Key: "version", Msg: fmt.Sprintf("no migration from config version %d", ver)}}
		}
		if err := m(raw); err != nil {
			return nil, Errors{{Key: "version", Msg: fmt.Sprintf("migrating from version %d: %v", ver, err)}}
		}
		ver++
		raw["version"] = ver
	}

	flat := map[string]any{}
	flatten("", raw, flat)
	paths := make([]string, 0, len(flat))
	for p := range flat {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	cfg := Defaults()
	for _, p := range paths {
		if isPlugin(p) {
			continue
		}
		k, ok := Lookup(p)
		if !ok {
			errs = append(errs, &Error{Line: d.lineOf(p), Key: p, Msg: unknownMsg(p)})
			continue
		}
		v, err := coerce(k, flat[p])
		if err != nil {
			errs = append(errs, &Error{Line: d.lineOf(p), Key: p, Msg: err.Error()})
			continue
		}
		cfg.values[p] = v
	}
	if pl, ok := raw[PluginsPrefix].(map[string]any); ok {
		for id, v := range pl {
			if t, ok := v.(map[string]any); ok {
				cfg.plugins[id] = t
			} else {
				errs = append(errs, &Error{Line: d.lineOf("plugins." + id), Key: "plugins." + id, Msg: "each plugin needs its own table: [plugins." + id + "]"})
			}
		}
	}
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].Line < errs[j].Line })
		return nil, errs
	}
	return cfg, nil
}

func unknownMsg(p string) string {
	top := strings.SplitN(p, ".", 2)[0]
	for _, t := range knownTables() {
		if t == top {
			return "unknown key"
		}
	}
	if top == "version" {
		return "unknown key"
	}
	return fmt.Sprintf("unknown key (known sections: %s, plugins)", strings.Join(knownTables(), ", "))
}

func describe(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case []any, []string:
		return "an array"
	case map[string]any:
		return "a table"
	}
	return fmt.Sprintf("%T", v)
}

// coerce checks v against the key's type and constraints and returns the
// typed value stored in Config.
func coerce(k Key, v any) (any, error) {
	bad := func() (any, error) {
		return nil, fmt.Errorf("must be %s, got %s", typeName(k.Type), describe(v))
	}
	switch k.Type {
	case Bool:
		b, ok := v.(bool)
		if !ok {
			return bad()
		}
		return b, nil
	case Int:
		var n int64
		switch x := v.(type) {
		case int64:
			n = x
		case int:
			n = int64(x)
		default:
			return bad()
		}
		if err := checkRange(k, float64(n)); err != nil {
			return nil, err
		}
		return n, nil
	case Float:
		var x float64
		switch t := v.(type) {
		case float64:
			x = t
		case int64:
			x = float64(t)
		case int:
			x = float64(t)
		default:
			return bad()
		}
		if err := checkRange(k, x); err != nil {
			return nil, err
		}
		return x, nil
	case String:
		s, ok := v.(string)
		if !ok {
			return bad()
		}
		if err := checkString(k, s); err != nil {
			return nil, err
		}
		return s, nil
	case Secret:
		s, ok := v.(string)
		if !ok {
			return bad()
		}
		name, isRef := strings.CutPrefix(s, "keychain:")
		if !isRef || name == "" {
			return nil, errors.New(`must be a keychain reference like "keychain:<name>"; secrets are never stored in the file`)
		}
		return SecretRef{Name: name}, nil
	case StringList:
		var list []string
		switch t := v.(type) {
		case []string:
			list = t
		case []any:
			for _, e := range t {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("must be a list of strings, found %s in it", describe(e))
				}
				list = append(list, s)
			}
		default:
			return bad()
		}
		for _, s := range list {
			if len(k.Enum) > 0 && !contains(k.Enum, s) {
				return nil, fmt.Errorf("item %q is not one of %s", s, strings.Join(k.Enum, ", "))
			}
		}
		if list == nil {
			list = []string{}
		}
		return list, nil
	}
	return nil, fmt.Errorf("unsupported type %q", k.Type)
}

func typeName(t Type) string {
	switch t {
	case Bool:
		return "true or false"
	case Int:
		return "an integer"
	case Float:
		return "a number"
	case StringList:
		return "a list of strings"
	}
	return "a string"
}

func checkRange(k Key, x float64) error {
	if k.Min != nil && x < *k.Min {
		return fmt.Errorf("must be at least %v, got %v", *k.Min, x)
	}
	if k.Max != nil && x > *k.Max {
		return fmt.Errorf("must be at most %v, got %v", *k.Max, x)
	}
	return nil
}

func checkString(k Key, s string) error {
	if len(k.Enum) > 0 && !contains(k.Enum, s) {
		return fmt.Errorf("must be one of %s, got %q", strings.Join(k.Enum, ", "), s)
	}
	switch k.Format {
	case "duration":
		if d, err := time.ParseDuration(s); err != nil || d < 0 {
			return fmt.Errorf(`must be a duration like "10m" or "30s", got %q`, s)
		}
	case "timerange":
		if s != "" && !validTimeRange(s) {
			return fmt.Errorf(`must look like "22:00-07:00" (24-hour HH:MM-HH:MM), got %q`, s)
		}
	case "hostport":
		host, port, ok := strings.Cut(s, ":")
		n, err := strconv.Atoi(port)
		if !ok || host == "" || err != nil || n < 1 || n > 65535 {
			return fmt.Errorf(`must be host:port like "127.0.0.1:7878", got %q`, s)
		}
	}
	return nil
}

func validTimeRange(s string) bool {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return false
	}
	for _, t := range []string{a, b} {
		if _, err := time.Parse("15:04", t); err != nil || len(t) != 5 {
			return false
		}
	}
	return true
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// Set changes one key, keeping all comments, ordering and formatting. value
// must have the Go type for the key (string, bool, int/int64, float64,
// []string; secrets are given as "keychain:<name>"). Plugin keys
// (plugins.<id>.<key>) accept any TOML scalar or string list. If the key is
// invalid, or the result would not validate, the Doc is left unchanged.
func (d *Doc) Set(key string, value any) error {
	var lit string
	if isPlugin(key) {
		if strings.Count(key, ".") < 2 {
			return &Error{Key: key, Msg: "plugin keys look like plugins.<id>.<key>"}
		}
		l, err := literal(value)
		if err != nil {
			return &Error{Key: key, Msg: err.Error()}
		}
		lit = l
	} else {
		k, ok := Lookup(key)
		if !ok {
			return &Error{Key: key, Msg: unknownMsg(key)}
		}
		v, err := coerce(k, value)
		if err != nil {
			return &Error{Key: key, Msg: err.Error()}
		}
		if s, isRef := v.(SecretRef); isRef {
			v = s.String()
		}
		l, err := literal(v)
		if err != nil {
			return &Error{Key: key, Msg: err.Error()}
		}
		lit = l
	}

	if sp, ok := d.spans[key]; ok && d.sameValue(key, sp, lit) {
		return nil
	}
	out, err := d.splice(key, lit)
	if err != nil {
		return err
	}
	nd, err := newDoc(out)
	if err != nil {
		return fmt.Errorf("edit would produce invalid TOML: %w", err)
	}
	if _, errs := nd.validate(); len(errs) > 0 {
		return fmt.Errorf("edit would make the config invalid: %w", errs)
	}
	*d = *nd
	return nil
}

// sameValue reports whether the value already in the file means the same as
// lit, so an unchanged Set keeps the user's own formatting (quotes, 10 vs
// 10.0, spacing) byte for byte.
func (d *Doc) sameValue(key string, sp span, lit string) bool {
	var oldV, newV map[string]any
	if toml.Unmarshal([]byte("v = "+string(d.src[sp.start:sp.end])), &oldV) != nil ||
		toml.Unmarshal([]byte("v = "+lit), &newV) != nil {
		return false
	}
	norm := func(v any) any {
		if s, ok := v.([]any); ok && len(s) == 0 {
			return []any{}
		}
		if n, ok := v.(int64); ok {
			if k, found := Lookup(key); found && k.Type == Float {
				return float64(n)
			}
		}
		return v
	}
	return reflect.DeepEqual(norm(oldV["v"]), norm(newV["v"]))
}

// literal renders a value as TOML text, with double-quoted strings.
func literal(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quote(x), nil
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = quote(s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			l, err := literal(e)
			if err != nil {
				return "", err
			}
			parts[i] = l
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case bool, int, int64, float64:
		b, err := toml.Marshal(map[string]any{"v": x})
		if err != nil {
			return "", err
		}
		return strings.TrimPrefix(strings.TrimSpace(string(b)), "v = "), nil
	}
	return "", fmt.Errorf("unsupported value type %T", v)
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (d *Doc) splice(key, lit string) ([]byte, error) {
	src := d.src
	if sp, ok := d.spans[key]; ok {
		return replace(src, sp.start, sp.end, lit), nil
	}
	// Missing inside an existing inline table: add a member before '}'.
	for p := key; strings.Contains(p, "."); {
		p = p[:strings.LastIndex(p, ".")]
		if sp, ok := d.spans[p]; ok && sp.inline {
			rest := key[len(p)+1:]
			at := sp.end - 1 // the closing brace
			for src[at-1] == ' ' || src[at-1] == '\t' {
				at--
			}
			add := ", " + rest + " = " + lit
			if src[at-1] == '{' {
				add = " " + rest + " = " + lit + " "
			}
			return replace(src, at, at, add), nil
		}
	}
	// Otherwise add "name = value" under [table] (or at the root).
	i := strings.LastIndex(key, ".")
	table, name := "", key
	if i >= 0 {
		table, name = key[:i], key[i+1:]
	}
	line := name + " = " + lit + "\n"
	if table == "" {
		if d.lastRoot >= 0 {
			return insertAt(src, d.lastRoot, line), nil
		}
		if len(src) > 0 {
			line += "\n"
		}
		return replace(src, 0, 0, line), nil
	}
	if at, ok := d.section[table]; ok {
		return insertAt(src, at, line), nil
	}
	out := ensureNL(src)
	if len(out) > 0 {
		out = append(out, '\n')
	}
	out = append(out, "["+table+"]\n"...)
	return append(out, line...), nil
}

// insertAt inserts whole lines at a line boundary, adding the missing final
// newline first when inserting at the end of an unterminated last line.
func insertAt(src []byte, at int, text string) []byte {
	if at == len(src) && len(src) > 0 && src[len(src)-1] != '\n' {
		text = "\n" + text
	}
	return replace(src, at, at, text)
}

// ensureNL makes sure src ends with a newline (when non-empty).
func ensureNL(src []byte) []byte {
	out := append([]byte{}, src...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out
}

func replace(src []byte, start, end int, s string) []byte {
	out := make([]byte, 0, len(src)+len(s))
	out = append(out, src[:start]...)
	out = append(out, s...)
	return append(out, src[end:]...)
}

// SetFile edits a config file in place, atomically. A missing file is
// created. It is safe to call while Watch is running.
func SetFile(path, key string, value any) error {
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := ParseDoc(src)
	if err != nil {
		return tagFile(err, path)
	}
	if err := d.Set(key, value); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(d.src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
