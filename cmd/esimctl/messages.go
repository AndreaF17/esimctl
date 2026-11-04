package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/AndreaF17/esimctl/internal/modem"
	"github.com/AndreaF17/esimctl/internal/notify"
)

func cmdSMS(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		msgs, err := modem.Messages()
		if err != nil {
			return err
		}
		return output(msgs, func() {
			if len(msgs) == 0 {
				fmt.Println("no messages")
			}
			for _, m := range msgs {
				mark := "  "
				if !m.Read {
					mark = "● "
				}
				dir := "from"
				if !m.Incoming {
					dir = "to  "
				}
				fmt.Printf("%s%-4s %s %-16s %s  %s\n", mark, m.ID, dir, m.Number, when(m.Time), firstLine(m.Text, 50))
			}
		})
	case "read", "show":
		if len(args) != 2 {
			return errors.New("sms read: expected a message id")
		}
		m, err := modem.FindMessage(args[1])
		if err != nil {
			return err
		}
		modem.MarkRead(m)
		m.Read = true
		return output(m, func() {
			fmt.Printf("%s  %s\n\n%s\n", m.Number, when(m.Time), m.Text)
		})
	case "send":
		if len(args) < 3 {
			return errors.New("sms send: expected a number and the text")
		}
		if err := modem.SendMessage(args[1], strings.Join(args[2:], " ")); err != nil {
			return err
		}
		return done("message sent to " + args[1])
	case "delete", "rm":
		if len(args) != 2 {
			return errors.New("sms delete: expected a message id")
		}
		if err := modem.DeleteMessage(args[1]); err != nil {
			return err
		}
		return done("message deleted")
	case "mark-read":
		msgs, err := modem.Messages()
		if err != nil {
			return err
		}
		modem.MarkRead(msgs...)
		return done("all messages marked as read")
	}
	return fmt.Errorf("sms: unknown subcommand %q (list, read, send, delete, mark-read)", sub)
}

func cmdCalls(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		calls, err := modem.Calls()
		if err != nil {
			return err
		}
		return output(calls, func() {
			if len(calls) == 0 {
				fmt.Println("no calls")
			}
			for _, c := range calls {
				fmt.Printf("%-4s %-16s %s\n", c.ID, c.Number, c.State)
			}
		})
	case "hangup", "reject":
		if len(args) != 2 {
			return errors.New("calls hangup: expected a call id")
		}
		if err := modem.Hangup(args[1]); err != nil {
			return err
		}
		return done("call ended")
	}
	return fmt.Errorf("calls: unknown subcommand %q (list, hangup)", sub)
}

// cmdWatch runs until interrupted, turning incoming messages and calls into
// desktop notifications. It is meant to run as a user service.
func cmdWatch(ctx context.Context) error {
	n, err := notify.New()
	if err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	var mu sync.Mutex
	ringing := map[string]uint32{} // call id -> notification id
	rejected := map[string]bool{}

	openMessages := func() {
		_ = exec.Command("omarchy-launch-or-focus-tui", "--app-id=org.omarchy.esimctl", self(), "tui", "--tab", "messages").Start()
	}
	ev := modem.Events{
		Message: func(m modem.Message) {
			_, _ = n.Show(notify.Notification{
				App: "Messages", Icon: "mail-message-new",
				Summary: "SMS from " + m.Number, Body: html.EscapeString(m.Text),
				Actions: []string{"default", "Open", "open", "Open", "read", "Mark as read"},
				Urgency: 1, Timeout: -1,
				OnAction: func(a string) {
					switch a {
					case "default", "open":
						openMessages()
					case "read":
						modem.MarkRead(m)
					}
				},
			})
		},
		Call: func(c modem.Call) {
			id, err := n.Show(notify.Notification{
				App: "Phone", Icon: "call-start",
				Summary: "Incoming call",
				Body:    html.EscapeString(orDash(c.Number)) + "\nThis laptop can't carry call audio.",
				Actions: []string{"reject", "Reject"},
				Urgency: 2, Timeout: 0,
				OnAction: func(a string) {
					if a == "reject" {
						mu.Lock()
						rejected[c.ID] = true
						mu.Unlock()
						_ = modem.Hangup(c.ID)
					}
				},
			})
			if err == nil {
				mu.Lock()
				ringing[c.ID] = id
				mu.Unlock()
			}
		},
		CallEnded: func(c modem.Call) {
			mu.Lock()
			id, wasRinging := ringing[c.ID]
			wasRejected := rejected[c.ID]
			delete(ringing, c.ID)
			delete(rejected, c.ID)
			mu.Unlock()
			if !wasRinging {
				return
			}
			n.Close(id)
			if !wasRejected {
				_, _ = n.Show(notify.Notification{
					App: "Phone", Icon: "call-missed",
					Summary: "Missed call", Body: html.EscapeString(orDash(c.Number)) + " · " + time.Now().Format("15:04"),
					Urgency: 1, Timeout: -1,
				})
			}
		},
	}
	go watchUsage(ctx, n)

	// ModemManager may be restarting or the modem re-probing: keep trying.
	for ctx.Err() == nil {
		if err := modem.Watch(ctx, ev); err != nil && !g.quiet {
			fmt.Fprintln(os.Stderr, "watch:", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}

// watchUsage counts mobile data every 30 seconds and enforces the data cap:
// a notification at the warning threshold, another at the limit, and with
// action "stop" mobile data is disconnected (again after a reboot, too).
func watchUsage(ctx context.Context, n *notify.Notifier) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		if u, err := modem.SampleUsage(time.Now()); err == nil && u.Limit.Bytes > 0 {
			enforceLimit(u, n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// shower is the part of notify.Notifier enforceLimit needs (fake in tests).
type shower interface {
	Show(notify.Notification) (uint32, error)
}

// Overridable in tests.
var (
	saveUsageFlags   = modem.UpdateUsageFlags
	mobileDataActive = func() bool { c, err := modem.GetConnection(); return err == nil && c.Active }
	stopMobileData   = modem.Disconnect
)

func enforceLimit(u modem.Usage, n shower) {
	used := modem.FormatSize(u.Bytes) + " of " + modem.FormatSize(u.Limit.Bytes)
	warned, stopped := u.Warned, u.Stopped
	if u.Percent >= float64(u.Limit.WarnPercent) && !warned {
		warned = true
		_, _ = n.Show(notify.Notification{
			App: "Mobile data", Icon: "network-cellular-signal-good",
			Summary: fmt.Sprintf("%.0f%% of your mobile data used", u.Percent),
			Body:    used + " · resets " + u.NextReset.Format("02 Jan"),
			Urgency: 1, Timeout: -1,
		})
	}
	if u.Bytes >= u.Limit.Bytes && !stopped {
		stopped = true
		body := used + ". "
		if u.Limit.Action == "stop" {
			body += "Mobile data has been turned off until " + u.NextReset.Format("02 Jan") + "."
		} else {
			body += "Mobile data stays on."
		}
		_, _ = n.Show(notify.Notification{
			App: "Mobile data", Icon: "network-cellular-offline",
			Summary: "Mobile data limit reached", Body: body,
			Urgency: 2, Timeout: -1,
		})
	}
	if warned != u.Warned || stopped != u.Stopped {
		_ = saveUsageFlags(warned, stopped)
	}
	if limitBlocks(u) && mobileDataActive() {
		_ = stopMobileData()
	}
}

// self returns the path of the running esimctl binary.
func self() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "esimctl"
}

func when(t time.Time) string {
	if t.IsZero() {
		return "        "
	}
	t = t.Local()
	if y, m, d := time.Now().Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return t.Format("15:04")
	}
	return t.Format("02 Jan 15:04")
}

func firstLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
