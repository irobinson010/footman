//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

const (
	ssDest       = "org.freedesktop.secrets"
	ssPath       = "/org/freedesktop/secrets"
	ssService    = "org.freedesktop.Secret.Service"
	ssCollection = "org.freedesktop.Secret.Collection"
	ssItem       = "org.freedesktop.Secret.Item"
	ssPrompt     = "org.freedesktop.Secret.Prompt"
	ssDefault    = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	appAttr      = "footman"
)

// ErrNoKeyring is returned (wrapped, along with platform.ErrUnsupported) when
// no Secret Service is running.
var ErrNoKeyring = errors.New("no keyring: no Secret Service (gnome-keyring, KWallet) is running")

// secret is the Secret Service "(oayays)" struct.
type secret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// Secrets stores secrets in the Secret Service keyring (KWallet and
// gnome-keyring both provide it). Each secret is an item with attributes
// application=footman and key=<key>.
type Secrets struct {
	bus *bus
}

func (s *Secrets) attrs(key string) map[string]string {
	return map[string]string{"application": appAttr, "key": key}
}

// open returns the connection and a plain-text session.
func (s *Secrets) open(ctx context.Context) (*dbus.Conn, dbus.ObjectPath, error) {
	conn, err := s.bus.get()
	if err != nil {
		return nil, "", fmt.Errorf("linux secrets: %w: %w", ErrNoKeyring, err)
	}
	var out dbus.Variant
	var sess dbus.ObjectPath
	err = conn.Object(ssDest, ssPath).CallWithContext(ctx, ssService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &sess)
	if err != nil {
		return nil, "", s.wrap(err)
	}
	return conn, sess, nil
}

func (s *Secrets) wrap(err error) error {
	if serviceMissing(err) {
		return fmt.Errorf("linux secrets: %w: %w", ErrNoKeyring, platform.ErrUnsupported)
	}
	return fmt.Errorf("linux secrets: %w", err)
}

// find returns the item for key, unlocking it if needed, or "" if absent.
func (s *Secrets) find(ctx context.Context, conn *dbus.Conn, key string) (dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	err := conn.Object(ssDest, ssPath).CallWithContext(ctx, ssService+".SearchItems", 0, s.attrs(key)).Store(&unlocked, &locked)
	if err != nil {
		return "", s.wrap(err)
	}
	if len(unlocked) > 0 {
		return unlocked[0], nil
	}
	if len(locked) == 0 {
		return "", nil
	}
	var got []dbus.ObjectPath
	var prompt dbus.ObjectPath
	err = conn.Object(ssDest, ssPath).CallWithContext(ctx, ssService+".Unlock", 0, locked[:1]).Store(&got, &prompt)
	if err != nil {
		return "", s.wrap(err)
	}
	if prompt != "/" {
		if err := s.prompt(ctx, conn, prompt); err != nil {
			return "", err
		}
		return locked[0], nil
	}
	if len(got) > 0 {
		return got[0], nil
	}
	return locked[0], nil
}

// prompt runs a Secret Service prompt (such as an unlock dialog) to the end.
func (s *Secrets) prompt(ctx context.Context, conn *dbus.Conn, p dbus.ObjectPath) error {
	if p == "/" {
		return nil
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)
	opts := []dbus.MatchOption{dbus.WithMatchObjectPath(p), dbus.WithMatchInterface(ssPrompt)}
	if err := conn.AddMatchSignal(opts...); err != nil {
		return s.wrap(err)
	}
	defer func() { _ = conn.RemoveMatchSignal(opts...) }()
	if err := conn.Object(ssDest, p).CallWithContext(ctx, ssPrompt+".Prompt", 0, "").Err; err != nil {
		return s.wrap(err)
	}
	for {
		select {
		case sig := <-ch:
			if sig.Path != p || sig.Name != ssPrompt+".Completed" || len(sig.Body) < 1 {
				continue
			}
			if dismissed, _ := sig.Body[0].(bool); dismissed {
				return errors.New("linux secrets: the keyring prompt was dismissed")
			}
			return nil
		case <-ctx.Done():
			_ = conn.Object(ssDest, p).Call(ssPrompt+".Dismiss", 0).Err
			return ctx.Err()
		}
	}
}

// Get returns the secret stored under key, or an error wrapping
// platform.ErrNotFound.
func (s *Secrets) Get(ctx context.Context, key string) (string, error) {
	conn, sess, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	item, err := s.find(ctx, conn, key)
	if err != nil {
		return "", err
	}
	if item == "" {
		return "", fmt.Errorf("linux secrets: %q: %w", key, platform.ErrNotFound)
	}
	var sec secret
	if err := conn.Object(ssDest, item).CallWithContext(ctx, ssItem+".GetSecret", 0, sess).Store(&sec); err != nil {
		return "", s.wrap(err)
	}
	return string(sec.Value), nil
}

// Set stores value under key, replacing any previous value.
func (s *Secrets) Set(ctx context.Context, key, value string) error {
	conn, sess, err := s.open(ctx)
	if err != nil {
		return err
	}
	props := map[string]dbus.Variant{
		ssItem + ".Label":      dbus.MakeVariant("Footman: " + key),
		ssItem + ".Attributes": dbus.MakeVariant(s.attrs(key)),
	}
	sec := secret{Session: sess, Parameters: []byte{}, Value: []byte(value), ContentType: "text/plain"}
	var item, prompt dbus.ObjectPath
	err = conn.Object(ssDest, ssDefault).CallWithContext(ctx, ssCollection+".CreateItem", 0, props, sec, true).Store(&item, &prompt)
	if err != nil {
		return s.wrap(err)
	}
	return s.prompt(ctx, conn, prompt)
}

// Delete removes key. Deleting a missing key is not an error.
func (s *Secrets) Delete(ctx context.Context, key string) error {
	conn, _, err := s.open(ctx)
	if err != nil {
		return err
	}
	item, err := s.find(ctx, conn, key)
	if err != nil || item == "" {
		return err
	}
	var prompt dbus.ObjectPath
	if err := conn.Object(ssDest, item).CallWithContext(ctx, ssItem+".Delete", 0).Store(&prompt); err != nil {
		return s.wrap(err)
	}
	return s.prompt(ctx, conn, prompt)
}
