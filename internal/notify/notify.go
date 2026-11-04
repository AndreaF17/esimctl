// Package notify shows desktop notifications through the freedesktop
// Notifications D-Bus API, with actions the caller is told about.
package notify

import (
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	service = "org.freedesktop.Notifications"
	objPath = "/org/freedesktop/Notifications"
)

// Notifier sends notifications and dispatches their action callbacks.
type Notifier struct {
	conn    *dbus.Conn
	mu      sync.Mutex
	actions map[uint32]func(action string)
}

// New connects to the session bus.
func New() (*Notifier, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	n := &Notifier{conn: c, actions: map[uint32]func(string){}}
	if err := c.AddMatchSignal(dbus.WithMatchInterface(service), dbus.WithMatchObjectPath(objPath)); err != nil {
		c.Close()
		return nil, err
	}
	sig := make(chan *dbus.Signal, 16)
	c.Signal(sig)
	go n.dispatch(sig)
	return n, nil
}

func (n *Notifier) dispatch(sig chan *dbus.Signal) {
	for s := range sig {
		if len(s.Body) < 2 {
			continue
		}
		id, _ := s.Body[0].(uint32)
		n.mu.Lock()
		f := n.actions[id]
		if s.Name == service+".NotificationClosed" {
			delete(n.actions, id)
		}
		n.mu.Unlock()
		if s.Name == service+".ActionInvoked" && f != nil {
			action, _ := s.Body[1].(string)
			go f(action)
		}
	}
}

// Notification describes one notification. Actions are key/label pairs.
type Notification struct {
	App, Icon, Summary, Body string
	Actions                  []string
	Urgency                  byte // 0 low, 1 normal, 2 critical
	Timeout                  int32
	Replaces                 uint32
	OnAction                 func(action string)
}

// Show displays a notification and returns its ID.
func (n *Notifier) Show(x Notification) (uint32, error) {
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(x.Urgency)}
	if x.Actions == nil {
		x.Actions = []string{}
	}
	var id uint32
	err := n.conn.Object(service, objPath).Call(service+".Notify", 0,
		x.App, x.Replaces, x.Icon, x.Summary, x.Body, x.Actions, hints, x.Timeout).Store(&id)
	if err == nil && x.OnAction != nil {
		n.mu.Lock()
		n.actions[id] = x.OnAction
		n.mu.Unlock()
	}
	return id, err
}

// Close withdraws a notification.
func (n *Notifier) Close(id uint32) {
	_ = n.conn.Object(service, objPath).Call(service+".CloseNotification", 0, id).Err
}
