package main

import (
	"testing"
	"time"

	"github.com/AndreaF17/esimctl/internal/modem"
	"github.com/AndreaF17/esimctl/internal/notify"
)

type fakeShower struct{ shown []notify.Notification }

func (f *fakeShower) Show(n notify.Notification) (uint32, error) {
	f.shown = append(f.shown, n)
	return uint32(len(f.shown)), nil
}

func TestEnforceLimit(t *testing.T) {
	var flags [2]bool
	active, stops := true, 0
	saveUsageFlags = func(w, s bool) error { flags = [2]bool{w, s}; return nil }
	mobileDataActive = func() bool { return active }
	stopMobileData = func() error { stops++; active = false; return nil }

	limit := modem.Limit{Bytes: 10e9, WarnPercent: 80, ResetDay: 1, Action: "stop"}
	u := modem.Usage{Limit: limit, NextReset: time.Now().AddDate(0, 1, 0)}

	n := &fakeShower{}
	u.Bytes, u.Percent = 5e9, 50
	enforceLimit(u, n)
	if len(n.shown) != 0 || stops != 0 {
		t.Fatalf("below the threshold: %d notifications, %d stops", len(n.shown), stops)
	}

	u.Bytes, u.Percent = 8.5e9, 85
	enforceLimit(u, n)
	if len(n.shown) != 1 || flags != [2]bool{true, false} || stops != 0 {
		t.Fatalf("warning: %d notifications, flags %v, %d stops", len(n.shown), flags, stops)
	}
	u.Warned = true
	enforceLimit(u, n) // no repeat
	if len(n.shown) != 1 {
		t.Fatal("warning repeated")
	}

	u.Bytes, u.Percent = 10.2e9, 102
	enforceLimit(u, n)
	if len(n.shown) != 2 || n.shown[1].Summary != "Mobile data limit reached" || stops != 1 || flags != [2]bool{true, true} {
		t.Fatalf("limit: %d notifications, %d stops, flags %v", len(n.shown), stops, flags)
	}

	// After a reboot NetworkManager reconnects: the watcher cuts it again,
	// without notifying twice.
	u.Stopped, active = true, true
	enforceLimit(u, n)
	if stops != 2 || len(n.shown) != 2 {
		t.Fatalf("re-enforce: %d stops, %d notifications", stops, len(n.shown))
	}

	// With action "warn" data is never cut.
	u2 := modem.Usage{Limit: modem.Limit{Bytes: 1e9, WarnPercent: 80, Action: "warn"}, Bytes: 2e9, Percent: 200}
	active = true
	enforceLimit(u2, n)
	if stops != 2 {
		t.Fatal("warn-only limit stopped data")
	}
}
