package permissions

import (
	"sort"
	"strings"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
)

// ConfigChanges lists the settings that differ between two configs, as
// SettingChange events without Actor, How or Reason. Plugin settings are
// included under "plugins.<id>.<key>".
func ConfigChanges(old, cur *config.Config) []core.SettingChange {
	a, b := flatten(old), flatten(cur)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var out []core.SettingChange
	for k := range keys {
		if a[k] != b[k] {
			out = append(out, core.SettingChange{Key: k, Old: a[k], New: b[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// PublishConfigChanges publishes a SettingChanged event for each setting that
// differs between old and cur, with the given actor, how and reason.
func PublishConfigChanges(b *bus.Bus, old, cur *config.Config, actor, how, reason string) {
	for _, c := range ConfigChanges(old, cur) {
		c.Actor, c.How, c.Reason = actor, how, reason
		bus.Publish(b, core.SettingChanged, c)
	}
}

// flatten maps every set value in cfg, by dotted key, to its file form.
func flatten(cfg *config.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil {
		return out
	}
	for _, k := range config.Schema() {
		prefix, suffix, wild := strings.Cut(k.Path, ".*.")
		if !wild {
			if v := cfg.Get(k.Path); v != nil {
				out[k.Path] = core.ValueText(v)
			}
			continue
		}
		for _, name := range cfg.Names(prefix) {
			p := prefix + "." + name + "." + suffix
			if v := cfg.Get(p); v != nil {
				out[p] = core.ValueText(v)
			}
		}
	}
	for _, id := range cfg.PluginIDs() {
		for k, v := range cfg.Plugin(id) {
			out[config.PluginsPrefix+"."+id+"."+k] = core.ValueText(v)
		}
	}
	return out
}

// secretSetting reports whether key holds a secret reference (config type
// Secret), whose value the action log never records.
func secretSetting(key string) bool {
	k, ok := config.Lookup(key)
	return ok && k.Type == config.Secret
}
