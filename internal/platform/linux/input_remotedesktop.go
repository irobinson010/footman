//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// remoteDesktop holds one keyboard-only RemoteDesktop portal session open
// while Footman runs, and sends key events through it. The first session
// asks the user for permission; the portal then returns a restore token,
// saved in the data folder, which brings later sessions back without asking.
type remoteDesktop struct {
	portal    func() (*portalConn, error)
	tokenFile string

	mu      sync.Mutex
	session dbus.ObjectPath
	closed  <-chan struct{}
	stop    func()
}

// Key states for NotifyKeyboard*.
const (
	keyReleased uint32 = 0
	keyPressed  uint32 = 1
)

// ensure returns an open session, starting one if needed.
func (r *remoteDesktop) ensure(ctx context.Context) (*portalConn, dbus.ObjectPath, error) {
	p, err := r.portal()
	if err != nil {
		return nil, "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != "" {
		select {
		case <-r.closed:
			r.stop()
			r.session = ""
		default:
			return p, r.session, nil
		}
	}
	s, err := r.start(ctx, p)
	if err != nil {
		return nil, "", err
	}
	r.session = s
	r.closed, r.stop = p.sessionClosed(s)
	return p, s, nil
}

func (r *remoteDesktop) start(ctx context.Context, p *portalConn) (dbus.ObjectPath, error) {
	s, err := p.createSession(ctx, portalRD, portalToken("footman_rd"))
	if noPortalMethod(err) {
		return "", fmt.Errorf("linux input: %w: the desktop portal has no RemoteDesktop interface", platform.ErrUnsupported)
	}
	if err != nil {
		return "", fmt.Errorf("linux input: %w", err)
	}
	sel := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // keyboard
		"persist_mode": dbus.MakeVariant(uint32(2)), // until the user revokes it
	}
	if tok := r.loadToken(); tok != "" {
		sel["restore_token"] = dbus.MakeVariant(tok)
	}
	if _, err := p.request(ctx, portalRD+".SelectDevices", s, sel); err != nil {
		p.closeSession(s)
		return "", fmt.Errorf("linux input: %w", err)
	}
	res, err := p.request(ctx, portalRD+".Start", s, "", map[string]dbus.Variant{})
	if err != nil {
		p.closeSession(s)
		if errors.Is(err, errPortalCancelled) {
			return "", fmt.Errorf("linux input: permission to type was not given: %w", err)
		}
		return "", fmt.Errorf("linux input: %w", err)
	}
	if v, ok := res["devices"]; ok {
		if d, _ := v.Value().(uint32); d&1 == 0 {
			p.closeSession(s)
			return "", errors.New("linux input: the portal did not grant keyboard access")
		}
	}
	// Tokens are single use: every Start returns the next one.
	if v, ok := res["restore_token"]; ok {
		if tok, _ := v.Value().(string); tok != "" {
			r.saveToken(tok)
		}
	}
	return s, nil
}

func (r *remoteDesktop) loadToken() string {
	if r.tokenFile == "" {
		return ""
	}
	b, err := os.ReadFile(r.tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (r *remoteDesktop) saveToken(tok string) {
	if r.tokenFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.tokenFile), 0o700); err != nil {
		return
	}
	tmp := r.tokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, r.tokenFile)
}

// keysym presses or releases a keysym.
func (r *remoteDesktop) keysym(ctx context.Context, p *portalConn, s dbus.ObjectPath, sym uint32, state uint32) error {
	err := p.obj().CallWithContext(ctx, portalRD+".NotifyKeyboardKeysym", 0, s, map[string]dbus.Variant{}, int32(sym), state).Err
	if err != nil {
		r.forget(s)
		return fmt.Errorf("linux input: sending a key: %w", err)
	}
	return nil
}

// tap presses and releases a keysym.
func (r *remoteDesktop) tap(ctx context.Context, p *portalConn, s dbus.ObjectPath, sym uint32) error {
	if err := r.keysym(ctx, p, s, sym, keyPressed); err != nil {
		return err
	}
	return r.keysym(ctx, p, s, sym, keyReleased)
}

// combo presses the keysyms in order and releases them in reverse. Keys
// already pressed are released even if a later call fails.
func (r *remoteDesktop) combo(ctx context.Context, p *portalConn, s dbus.ObjectPath, syms []uint32) error {
	var err error
	n := 0
	for _, k := range syms {
		if err = r.keysym(ctx, p, s, k, keyPressed); err != nil {
			break
		}
		n++
	}
	rctx := context.WithoutCancel(ctx)
	for i := n - 1; i >= 0; i-- {
		err = errors.Join(err, r.keysym(rctx, p, s, syms[i], keyReleased))
	}
	return err
}

// forget drops session s after an error, so the next call starts afresh.
func (r *remoteDesktop) forget(s dbus.ObjectPath) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == s && s != "" {
		r.stop()
		r.session = ""
	}
}

// close ends the session.
func (r *remoteDesktop) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == "" {
		return
	}
	if p, err := r.portal(); err == nil {
		p.closeSession(r.session)
	}
	r.stop()
	r.session = ""
}
