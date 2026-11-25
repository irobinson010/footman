package install

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Prompter asks questions on a terminal. An empty answer (or the end of input)
// takes the default.
type Prompter struct {
	In  *bufio.Reader
	Out io.Writer
	// ReadSecret reads an answer without echoing it. Nil falls back to a
	// plain line.
	ReadSecret func() (string, error)
}

// NewPrompter reads answers from in and writes questions to out.
func NewPrompter(in io.Reader, out io.Writer, readSecret func() (string, error)) *Prompter {
	return &Prompter{In: bufio.NewReader(in), Out: out, ReadSecret: readSecret}
}

func (p *Prompter) line() (string, error) {
	s, err := p.In.ReadString('\n')
	if err == io.EOF {
		err = nil
	}
	return strings.TrimSpace(s), err
}

// Ask prints question with its default in brackets and returns the answer.
func (p *Prompter) Ask(question, def string) (string, error) {
	if def != "" {
		_, _ = fmt.Fprintf(p.Out, "%s [%s]: ", question, def)
	} else {
		_, _ = fmt.Fprintf(p.Out, "%s: ", question)
	}
	s, err := p.line()
	if s == "" {
		s = def
	}
	return s, err
}

// Confirm asks a yes/no question. Anything but y or yes is no unless def is
// true and the answer is empty.
func (p *Prompter) Confirm(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	_, _ = fmt.Fprintf(p.Out, "%s [%s]: ", question, hint)
	s, err := p.line()
	switch strings.ToLower(s) {
	case "":
		return def, err
	case "y", "yes":
		return true, err
	}
	return false, err
}

func (p *Prompter) secret(question string) (string, error) {
	_, _ = fmt.Fprintf(p.Out, "%s: ", question)
	if p.ReadSecret == nil {
		return p.line()
	}
	s, err := p.ReadSecret()
	_, _ = fmt.Fprintln(p.Out)
	return strings.TrimSpace(s), err
}

// AskSetup fills in the options that are still empty: user name, default
// persona, provider and API key (stored in the keychain), and the push-to-talk
// key, suggesting F8 (ADR-0022).
func (p *Prompter) AskSetup(o *SetupOptions) error {
	var err error
	if o.Name == "" {
		if o.Name, err = p.Ask("What should the assistant call you", ""); err != nil {
			return err
		}
	}
	if o.Persona == "" {
		if o.Persona, err = p.Ask("Default persona", "ashford"); err != nil {
			return err
		}
	}
	if o.Provider == "" {
		if o.Provider, err = p.Ask("AI provider ("+strings.Join(Providers, ", ")+")", Providers[0]); err != nil {
			return err
		}
	}
	if o.APIKey == "" && o.Provider != "ollama" {
		if o.APIKey, err = p.secret("API key for " + o.Provider + " (kept in the OS keychain; blank to add it later)"); err != nil {
			return err
		}
	}
	if o.Hotkey == "" {
		if o.Hotkey, err = p.Ask("Push-to-talk key", DefaultHotkey); err != nil {
			return err
		}
	}
	return nil
}

// Keep choices from the uninstall menu (design §11 step 3).
const (
	RemoveEverything  = 1
	KeepConfigAndData = 2
	KeepConfigOnly    = 3
)

// AskUninstall shows the listing and asks what to keep, then asks separately
// whether to delete the keychain secrets, then for a final go-ahead. Options
// already chosen by flags are not asked again. It returns false if the user
// declined to go on.
func (p *Prompter) AskUninstall(pl *Plan, o *UninstallOptions, decided bool) (bool, error) {
	_, _ = fmt.Fprintln(p.Out, "Footman will remove:")
	pl.Listing(p.Out)
	_, _ = fmt.Fprintln(p.Out)

	if !decided {
		_, _ = fmt.Fprint(p.Out, `What should be kept?
  1) Remove everything
  2) Keep config and data (reinstalling picks up where you left off)
  3) Keep config only
`)
		choice, err := p.Ask("Choice", "2")
		if err != nil {
			return false, err
		}
		switch choice {
		case "1": // remove everything: keep nothing
		case "2":
			o.KeepConfig, o.KeepData = true, true
		case "3":
			o.KeepConfig = true
		default:
			return false, fmt.Errorf("install: %q is not a choice (1, 2 or 3)", choice)
		}
	}
	if n := pl.Count(GroupSecrets, KindSecret); n > 0 && !o.Purge && !o.RemoveSecrets {
		ok, err := p.Confirm(fmt.Sprintf("Also delete the %d API key(s) in the OS keychain? This cannot be undone", n), false)
		if err != nil {
			return false, err
		}
		o.RemoveSecrets = ok
	}
	if o.DryRun {
		return true, nil
	}
	return p.Confirm("Go ahead", false)
}
