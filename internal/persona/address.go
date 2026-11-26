package persona

import "strings"

// Settings is the part of the config the persona package reads;
// *config.Config satisfies it.
type Settings interface {
	String(path string) string
}

// Address returns how the persona addresses the user: the address form filled
// from user.name and the title from personas.overrides.<id>.title (the pack's
// own title when there is no override). Until a name is set it returns the
// pack's fallback ("sir").
func (p *Pack) Address(cfg Settings) string {
	name := strings.TrimSpace(cfg.String("user.name"))
	if name == "" {
		return p.AddressForm.Fallback
	}
	title := p.AddressForm.Title
	if t := strings.TrimSpace(cfg.String("personas.overrides." + p.ID + ".title")); t != "" {
		title = t
	}
	out := strings.NewReplacer("{title}", title, "{name}", name).Replace(p.AddressForm.Form)
	return strings.Join(strings.Fields(out), " ")
}

// Quips returns how often the persona quips: the user's
// personas.overrides.<id>.quips if valid, else the pack's own setting.
func (p *Pack) Quips(cfg Settings) string {
	if q := cfg.String("personas.overrides." + p.ID + ".quips"); contains(QuipLevels, q) {
		return q
	}
	return p.Style.Quips
}
