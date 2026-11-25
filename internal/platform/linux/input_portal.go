//go:build linux

package linux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// xdg-desktop-portal plumbing shared by the RemoteDesktop and GlobalShortcuts
// clients.

const (
	portalDest     = "org.freedesktop.portal.Desktop"
	portalPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalRequest  = "org.freedesktop.portal.Request"
	portalSession  = "org.freedesktop.portal.Session"
	portalRegistry = "org.freedesktop.host.portal.Registry"
	portalRD       = "org.freedesktop.portal.RemoteDesktop"
	portalGS       = "org.freedesktop.portal.GlobalShortcuts"
)

// portalConn is a private session-bus connection for portal calls. It is not
// the platform's shared connection, because registering an app id must be the
// first portal call a connection makes.
type portalConn struct {
	conn *dbus.Conn

	mu   sync.Mutex
	subs map[*portalSub]struct{}
}

type portalSub struct {
	match func(*dbus.Signal) bool
	ch    chan *dbus.Signal
}

// errPortalCancelled is returned when the user dismisses a portal dialog.
var errPortalCancelled = errors.New("the request was cancelled in the desktop's dialog")

// openPortal connects and registers appID with the host registry, so that
// dialogs and stored permissions name Footman (xdg-desktop-portal 1.19 and
// later; on older versions the registration fails and the portal names
// whatever started Footman instead).
func openPortal(dial func(...dbus.ConnOption) (*dbus.Conn, error), appID string) (*portalConn, error) {
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("no D-Bus session bus: %w", err)
	}
	p := &portalConn{conn: c, subs: map[*portalSub]struct{}{}}
	sigs := make(chan *dbus.Signal, 256)
	c.Signal(sigs)
	go p.dispatch(sigs)
	for _, iface := range []string{portalRequest, portalSession, portalGS} {
		if err := c.AddMatchSignal(dbus.WithMatchInterface(iface)); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	if appID != "" {
		if err := register(c, appID); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return p, nil
}

// Registration timing (ADR-0030).
var (
	// registerWait bounds retrying Register while the portal has not yet
	// noticed a .desktop file setup has just written.
	registerWait = 3 * time.Second
	// registerRetry is the pause between tries.
	registerRetry = 100 * time.Millisecond
)

// register registers appID with the portal's host registry. A portal without
// the registry (before xdg-desktop-portal 1.19) is fine: everything works,
// only attributed to whatever started Footman. Any other refusal is an error,
// because going on would bind shortcuts and grant permissions to another app
// (the terminal setup was run from, say). The portal reads .desktop files
// through a cache that takes a moment to see a new one, so a refusal is
// retried for up to registerWait.
func register(c *dbus.Conn, appID string) error {
	deadline := time.Now().Add(registerWait)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Object(portalDest, portalPath).CallWithContext(ctx, portalRegistry+".Register", 0, appID, map[string]dbus.Variant{}).Err
		cancel()
		if err == nil || noPortalMethod(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the desktop portal does not accept the app id %s (is its .desktop file installed? run footman setup): %w", appID, err)
		}
		time.Sleep(registerRetry)
	}
}

func (p *portalConn) close() error { return p.conn.Close() }

func (p *portalConn) dispatch(sigs <-chan *dbus.Signal) {
	for s := range sigs {
		p.mu.Lock()
		for sub := range p.subs {
			if sub.match(s) {
				select {
				case sub.ch <- s:
				default: // a slow subscriber loses signals rather than blocking all
				}
			}
		}
		p.mu.Unlock()
	}
	// The connection closed: end every subscription.
	p.mu.Lock()
	for sub := range p.subs {
		close(sub.ch)
		delete(p.subs, sub)
	}
	p.mu.Unlock()
}

// subscribe delivers matching signals on the returned channel until cancel is
// called or the connection closes (then the channel is closed).
func (p *portalConn) subscribe(match func(*dbus.Signal) bool, buf int) (<-chan *dbus.Signal, func()) {
	sub := &portalSub{match: match, ch: make(chan *dbus.Signal, buf)}
	p.mu.Lock()
	if !p.conn.Connected() {
		p.mu.Unlock()
		close(sub.ch)
		return sub.ch, func() {}
	}
	p.subs[sub] = struct{}{}
	p.mu.Unlock()
	return sub.ch, func() {
		p.mu.Lock()
		if _, ok := p.subs[sub]; ok {
			delete(p.subs, sub)
			close(sub.ch)
		}
		p.mu.Unlock()
	}
}

func (p *portalConn) obj() dbus.BusObject { return p.conn.Object(portalDest, portalPath) }

// version returns an interface's "version" property, or 0 if the portal does
// not have the interface.
func (p *portalConn) version(ctx context.Context, iface string) uint32 {
	var v dbus.Variant
	if err := p.obj().CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, "version").Store(&v); err != nil {
		return 0
	}
	n, _ := v.Value().(uint32)
	return n
}

func portalToken(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// request calls a portal method that answers through a Request object and
// waits for the Response. The last argument must be the options map; a
// handle_token is added to it. If ctx ends first the request (and any dialog)
// is closed.
func (p *portalConn) request(ctx context.Context, method string, args ...any) (map[string]dbus.Variant, error) {
	opts, ok := args[len(args)-1].(map[string]dbus.Variant)
	if !ok {
		return nil, errors.New("portal request: last argument must be the options")
	}
	tok := portalToken("footman")
	opts["handle_token"] = dbus.MakeVariant(tok)
	sender := strings.ReplaceAll(strings.TrimPrefix(p.conn.Names()[0], ":"), ".", "_")
	want := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + tok)
	ch, cancel := p.subscribe(func(s *dbus.Signal) bool {
		return s.Name == portalRequest+".Response"
	}, 16)
	defer cancel()

	var handle dbus.ObjectPath
	if err := p.obj().CallWithContext(ctx, method, 0, args...).Store(&handle); err != nil {
		return nil, fmt.Errorf("%s: %w", shortMethod(method), err)
	}
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("%s: the D-Bus connection closed", shortMethod(method))
			}
			if s.Path != want && s.Path != handle {
				continue
			}
			var code uint32
			var res map[string]dbus.Variant
			if err := dbus.Store(s.Body, &code, &res); err != nil {
				return nil, fmt.Errorf("%s: bad response: %w", shortMethod(method), err)
			}
			switch code {
			case 0:
				return res, nil
			case 1:
				return nil, fmt.Errorf("%s: %w", shortMethod(method), errPortalCancelled)
			default:
				return nil, fmt.Errorf("%s: the portal reported an error (response %d)", shortMethod(method), code)
			}
		case <-ctx.Done():
			_ = p.conn.Object(portalDest, handle).Call(portalRequest+".Close", 0).Err
			return nil, ctx.Err()
		}
	}
}

// createSession calls iface.CreateSession and returns the session handle.
func (p *portalConn) createSession(ctx context.Context, iface, token string) (dbus.ObjectPath, error) {
	res, err := p.request(ctx, iface+".CreateSession", map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token),
	})
	if err != nil {
		return "", err
	}
	switch h := res["session_handle"].Value().(type) {
	case string:
		return dbus.ObjectPath(h), nil
	case dbus.ObjectPath:
		return h, nil
	}
	return "", fmt.Errorf("%s.CreateSession: no session handle", iface)
}

func (p *portalConn) closeSession(s dbus.ObjectPath) {
	_ = p.conn.Object(portalDest, s).Call(portalSession+".Close", 0).Err
}

// sessionClosed returns a channel closed when the portal closes session s
// (for example when the user stops it from the tray), and a cancel func.
func (p *portalConn) sessionClosed(s dbus.ObjectPath) (<-chan struct{}, func()) {
	ch, cancel := p.subscribe(func(sig *dbus.Signal) bool {
		return sig.Path == s && sig.Name == portalSession+".Closed"
	}, 1)
	done := make(chan struct{})
	go func() {
		<-ch // a Closed signal, or the subscription ending
		close(done)
	}()
	return done, cancel
}

func shortMethod(m string) string {
	if i := strings.LastIndex(m, "."); i >= 0 {
		if j := strings.LastIndex(m[:i], "."); j >= 0 {
			return m[j+1:]
		}
	}
	return m
}

// noPortalMethod reports whether err means the portal lacks the interface.
func noPortalMethod(err error) bool {
	var e dbus.Error
	if errors.As(err, &e) {
		switch e.Name {
		case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface",
			"org.freedesktop.DBus.Error.UnknownObject":
			return true
		}
	}
	return serviceMissing(err)
}
