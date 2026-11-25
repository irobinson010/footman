//go:build linux

package linux

import (
	"context"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

const (
	notifDest  = "org.freedesktop.Notifications"
	notifPath  = "/org/freedesktop/Notifications"
	notifIface = "org.freedesktop.Notifications"
)

// Notifier shows desktop notifications over org.freedesktop.Notifications.
// Action buttons need a server that supports them (KDE and GNOME do).
type Notifier struct {
	bus *bus

	mu      sync.Mutex
	started bool
	conn    *dbus.Conn
	waiters map[uint32]*waiter
	tray    platform.TrayStatus
}

type waiter struct {
	ch   chan string
	done bool
}

func (w *waiter) close() {
	if !w.done {
		w.done = true
		close(w.ch)
	}
}

// Notify shows n. The returned channel receives the ID of a clicked action and
// is closed when the notification is closed, clicked or ctx is done.
func (nt *Notifier) Notify(ctx context.Context, n platform.Notification) (<-chan string, error) {
	conn, err := nt.bus.get()
	if err != nil {
		return nil, fmt.Errorf("linux notifier: %w", err)
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()
	if !nt.started || nt.conn != conn {
		if err := nt.listen(conn); err != nil {
			return nil, err
		}
	}
	var actions []string
	for _, a := range n.Actions {
		actions = append(actions, a.ID, a.Label)
	}
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("footman")}
	call := conn.Object(notifDest, notifPath).CallWithContext(ctx, notifIface+".Notify", 0,
		"Footman", uint32(0), "", n.Title, n.Body, actions, hints, int32(-1))
	if call.Err != nil {
		if serviceMissing(call.Err) {
			return nil, fmt.Errorf("linux notifier: %w: no notification service on the session bus", platform.ErrUnsupported)
		}
		return nil, fmt.Errorf("linux notifier: %w", call.Err)
	}
	var id uint32
	if err := call.Store(&id); err != nil {
		return nil, fmt.Errorf("linux notifier: %w", err)
	}
	w := &waiter{ch: make(chan string, 1)}
	nt.waiters[id] = w
	go func() {
		<-ctx.Done()
		nt.mu.Lock()
		defer nt.mu.Unlock()
		if nt.waiters[id] == w {
			delete(nt.waiters, id)
		}
		w.close()
	}()
	return w.ch, nil
}

// listen subscribes to the notification signals. nt.mu must be held.
func (nt *Notifier) listen(conn *dbus.Conn) error {
	for _, m := range []string{"ActionInvoked", "NotificationClosed"} {
		if err := conn.AddMatchSignal(dbus.WithMatchInterface(notifIface), dbus.WithMatchMember(m)); err != nil {
			return fmt.Errorf("linux notifier: %w", err)
		}
	}
	ch := make(chan *dbus.Signal, 32)
	conn.Signal(ch)
	nt.conn, nt.started = conn, true
	go func() {
		for s := range ch {
			nt.dispatch(s)
		}
		nt.mu.Lock()
		if nt.conn == conn {
			nt.started = false
			for id, w := range nt.waiters {
				delete(nt.waiters, id)
				w.close()
			}
		}
		nt.mu.Unlock()
	}()
	return nil
}

func (nt *Notifier) dispatch(s *dbus.Signal) {
	if s.Path != notifPath {
		return
	}
	nt.mu.Lock()
	defer nt.mu.Unlock()
	switch s.Name {
	case notifIface + ".ActionInvoked":
		if len(s.Body) < 2 {
			return
		}
		id, ok1 := s.Body[0].(uint32)
		key, ok2 := s.Body[1].(string)
		if w := nt.waiters[id]; ok1 && ok2 && w != nil {
			select {
			case w.ch <- key:
			default:
			}
		}
	case notifIface + ".NotificationClosed":
		if len(s.Body) < 1 {
			return
		}
		if id, ok := s.Body[0].(uint32); ok {
			if w := nt.waiters[id]; w != nil {
				delete(nt.waiters, id)
				w.close()
			}
		}
	}
}

// SetTray records the status. A tray icon needs a StatusNotifierItem, which is
// a later task; for now this is a stub that does not fail.
func (nt *Notifier) SetTray(_ context.Context, s platform.TrayStatus) error {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.tray = s
	return nil
}
