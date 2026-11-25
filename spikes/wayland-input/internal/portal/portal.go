// Package portal holds the xdg-desktop-portal request/response plumbing shared
// by the spike programs (pure Go, godbus).
package portal

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	Dest = "org.freedesktop.portal.Desktop"
	Path = dbus.ObjectPath("/org/freedesktop/portal/desktop")
)

type Portal struct {
	Conn *dbus.Conn
	Obj  dbus.BusObject
	sigs chan *dbus.Signal
}

func Connect() (*Portal, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	p := &Portal{Conn: c, Obj: c.Object(Dest, Path), sigs: make(chan *dbus.Signal, 64)}
	c.Signal(p.sigs)
	return p, nil
}

// Register tells xdg-desktop-portal which app id this unsandboxed process is
// (org.freedesktop.host.portal.Registry, xdp >= 1.19). It must be the first
// portal call on the connection, and a matching .desktop file must exist.
func (p *Portal) Register(appID string) error {
	return p.Obj.Call("org.freedesktop.host.portal.Registry.Register", 0, appID, map[string]dbus.Variant{}).Err
}

func Token(prefix string) string { return fmt.Sprintf("%s%d", prefix, rand.Uint32()) }

func (p *Portal) requestPath(token string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(p.Conn.Names()[0], ":"), ".", "_")
	return dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/portal/desktop/request/%s/%s", sender, token))
}

// Request calls a portal method that returns a Request handle and waits for
// its Response. opts must be the last argument; a handle_token is added.
func (p *Portal) Request(method string, timeout time.Duration, args ...any) (uint32, map[string]dbus.Variant, error) {
	opts := args[len(args)-1].(map[string]dbus.Variant)
	tok := Token("footman")
	opts["handle_token"] = dbus.MakeVariant(tok)
	path := p.requestPath(tok)
	if err := p.Conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response")); err != nil {
		return 0, nil, err
	}
	defer p.Conn.RemoveMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response"))
	var got dbus.ObjectPath
	if err := p.Obj.Call(method, 0, args...).Store(&got); err != nil {
		return 0, nil, err
	}
	deadline := time.After(timeout)
	for {
		select {
		case s := <-p.sigs:
			if s.Path == path && s.Name == "org.freedesktop.portal.Request.Response" {
				return s.Body[0].(uint32), s.Body[1].(map[string]dbus.Variant), nil
			}
			p.Other(s)
		case <-deadline:
			return 0, nil, fmt.Errorf("%s: no response within %v (dialog waiting?)", method, timeout)
		}
	}
}

// Signals exposes signals not consumed by Request (e.g. GlobalShortcuts).
func (p *Portal) Signals() <-chan *dbus.Signal { return p.sigs }

// Other is called for signals that arrive while waiting on a Request.
var Other = func(*dbus.Signal) {}

func (p *Portal) Other(s *dbus.Signal) { Other(s) }

// StartRemoteDesktop creates, configures and starts a keyboard-only
// RemoteDesktop session. If tokenFile is set, the restore token is read from
// and written back to it (tokens are single use; each Start returns a new one).
func (p *Portal) StartRemoteDesktop(tokenFile string, timeout time.Duration) (dbus.ObjectPath, map[string]dbus.Variant, error) {
	const rd = "org.freedesktop.portal.RemoteDesktop"
	code, res, err := p.Request(rd+".CreateSession", 10*time.Second, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(Token("s")),
	})
	if err != nil || code != 0 {
		return "", nil, fmt.Errorf("CreateSession: code %d err %v", code, err)
	}
	session := dbus.ObjectPath(res["session_handle"].Value().(string))
	sel := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // keyboard
		"persist_mode": dbus.MakeVariant(uint32(2)), // until revoked
	}
	if tokenFile != "" {
		if b, err := os.ReadFile(tokenFile); err == nil && len(b) > 0 {
			sel["restore_token"] = dbus.MakeVariant(strings.TrimSpace(string(b)))
		}
	}
	if code, _, err = p.Request(rd+".SelectDevices", 10*time.Second, session, sel); err != nil || code != 0 {
		return "", nil, fmt.Errorf("SelectDevices: code %d err %v", code, err)
	}
	code, res, err = p.Request(rd+".Start", timeout, session, "", map[string]dbus.Variant{})
	if err != nil || code != 0 {
		return "", nil, fmt.Errorf("Start: code %d (1 = denied) err %v", code, err)
	}
	if tok, ok := res["restore_token"]; ok && tokenFile != "" {
		_ = os.WriteFile(tokenFile, []byte(tok.Value().(string)), 0o600)
	}
	return session, res, nil
}
