package modem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/AndreaF17/esimctl/internal/textutil"
)

// SMS and calls go through ModemManager's D-Bus API directly: mmcli's
// key=value syntax can't carry arbitrary message text, and new messages and
// calls are announced by signals.

const (
	mmService   = "org.freedesktop.ModemManager1"
	ifMessaging = mmService + ".Modem.Messaging"
	ifSMS       = mmService + ".Sms"
	ifVoice     = mmService + ".Modem.Voice"
	ifCall      = mmService + ".Call"
)

// Message is one SMS stored on the modem or SIM.
type Message struct {
	ID       string    `json:"id"`
	Number   string    `json:"number"`
	Text     string    `json:"text"`
	Time     time.Time `json:"time,omitempty"`
	Incoming bool      `json:"incoming"`
	State    string    `json:"state"`
	Read     bool      `json:"read"`
	// OnModem is false for messages only esimctl's archive still has:
	// MBIM modems drop stored SMS when they restart.
	OnModem bool `json:"on_modem"`
	path    dbus.ObjectPath
}

// Call is a voice call known to the modem.
type Call struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	State    string `json:"state"`
	Incoming bool   `json:"incoming"`
	path     dbus.ObjectPath
}

var smsStates = map[uint32]string{0: "unknown", 1: "stored", 2: "receiving", 3: "received", 4: "sending", 5: "sent"}
var callStates = map[int32]string{0: "unknown", 1: "dialing", 2: "ringing-out", 3: "ringing-in", 4: "active", 5: "held", 6: "waiting", 7: "terminated"}

func systemBus() (*dbus.Conn, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	return c, nil
}

// modemObject returns the first modem's D-Bus object.
func modemObject() (dbus.BusObject, *dbus.Conn, error) {
	st, err := Get()
	if err != nil {
		return nil, nil, err
	}
	c, err := systemBus()
	if err != nil {
		return nil, nil, err
	}
	return c.Object(mmService, dbus.ObjectPath(st.path)), c, nil
}

func idOf(p dbus.ObjectPath) string { return path.Base(string(p)) }

func props(c *dbus.Conn, p dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var m map[string]dbus.Variant
	err := c.Object(mmService, p).Call("org.freedesktop.DBus.Properties.GetAll", 0, iface).Store(&m)
	return m, err
}

func str(m map[string]dbus.Variant, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.Value().(string); ok {
			return s
		}
	}
	return ""
}

func u32(m map[string]dbus.Variant, k string) uint32 {
	if v, ok := m[k]; ok {
		switch x := v.Value().(type) {
		case uint32:
			return x
		case int32:
			return uint32(x)
		}
	}
	return 0
}

func readMessage(c *dbus.Conn, p dbus.ObjectPath) (Message, error) {
	m, err := props(c, p, ifSMS)
	if err != nil {
		return Message{}, err
	}
	text := textutil.Clean(str(m, "Text"))
	if strings.TrimSpace(text) == "" {
		// Service messages (voicemail indicators, binary SMS) carry no text.
		text = "(no text)"
	}
	msg := Message{
		ID: idOf(p), path: p, Number: textutil.Line(str(m, "Number")), Text: text,
		State: smsStates[u32(m, "State")],
		// PduType 1 is "deliver": a message received from the network.
		Incoming: u32(m, "PduType") == 1,
	}
	if ts := str(m, "Timestamp"); ts != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07", "2006-01-02T15:04:05Z07"} {
			if t, err := time.Parse(layout, ts); err == nil {
				msg.Time = t
				break
			}
		}
	}
	return msg, nil
}

// Messages lists stored SMS, newest first.
func Messages() ([]Message, error) {
	obj, c, err := modemObject()
	if err != nil {
		return nil, err
	}
	var paths []dbus.ObjectPath
	if err := obj.Call(ifMessaging+".List", 0).Store(&paths); err != nil {
		return nil, cleanDBus(err)
	}
	read := loadRead()
	arch := loadArchive()
	out := []Message{}
	live := map[string]bool{}
	changed := false
	for _, p := range paths {
		m, err := readMessage(c, p)
		if err != nil || m.State == "receiving" {
			continue
		}
		m.OnModem = true
		m.Read = !m.Incoming || read[m.key()]
		live[m.key()] = true
		if _, ok := arch[m.key()]; !ok {
			arch[m.key()] = m
			changed = true
		}
		out = append(out, m)
	}
	if changed {
		saveArchive(arch)
	}
	for k, m := range arch {
		if live[k] {
			continue
		}
		m.ID = "a" + k[:8]
		m.OnModem = false
		m.Read = !m.Incoming || read[k]
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Archive stores a message in esimctl's local archive.
func Archive(m Message) {
	arch := loadArchive()
	if _, ok := arch[m.key()]; !ok {
		arch[m.key()] = m
		saveArchive(arch)
	}
}

func archivePath() string { return filepath.Join(filepath.Dir(readPath()), "sms-archive.json") }

func loadArchive() map[string]Message {
	m := map[string]Message{}
	if b, err := os.ReadFile(archivePath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveArchive(m map[string]Message) {
	p := archivePath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.Marshal(m)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// Unread counts received messages not yet opened.
func Unread(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		if !m.Read {
			n++
		}
	}
	return n
}

// FindMessage returns a stored message by ID.
func FindMessage(id string) (Message, error) {
	msgs, err := Messages()
	if err != nil {
		return Message{}, err
	}
	for _, m := range msgs {
		if m.ID == id {
			return m, nil
		}
	}
	return Message{}, fmt.Errorf("no message %s", id)
}

// SendMessage sends an SMS.
func SendMessage(number, text string) error {
	number = strings.TrimSpace(number)
	if number == "" || strings.TrimSpace(text) == "" {
		return errors.New("number and text are required")
	}
	obj, c, err := modemObject()
	if err != nil {
		return err
	}
	var p dbus.ObjectPath
	err = obj.Call(ifMessaging+".Create", 0, map[string]dbus.Variant{
		"number": dbus.MakeVariant(number),
		"text":   dbus.MakeVariant(text),
	}).Store(&p)
	if err != nil {
		return cleanDBus(err)
	}
	if err := c.Object(mmService, p).Call(ifSMS+".Send", 0).Err; err != nil {
		_ = obj.Call(ifMessaging+".Delete", 0, p).Err
		return cleanDBus(err)
	}
	return nil
}

// DeleteMessage removes a stored SMS.
func DeleteMessage(id string) error {
	m, err := FindMessage(id)
	if err != nil {
		return err
	}
	if m.OnModem {
		obj, _, err := modemObject()
		if err != nil {
			return err
		}
		if err := obj.Call(ifMessaging+".Delete", 0, m.path).Err; err != nil {
			return cleanDBus(err)
		}
	}
	arch := loadArchive()
	delete(arch, m.key())
	saveArchive(arch)
	read := loadRead()
	delete(read, m.key())
	saveRead(read)
	return nil
}

// MarkRead records that the user has seen messages.
func MarkRead(msgs ...Message) {
	read := loadRead()
	for _, m := range msgs {
		read[m.key()] = true
	}
	saveRead(read)
}

// key identifies a message across ModemManager restarts, which renumber the
// SMS objects.
func (m Message) key() string {
	h := sha256.Sum256([]byte(m.Number + "\x00" + m.Time.UTC().Format(time.RFC3339) + "\x00" + m.Text))
	return hex.EncodeToString(h[:8])
}

func readPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".local/state")
	}
	return filepath.Join(dir, "esimctl", "sms-read.json")
}

func loadRead() map[string]bool {
	m := map[string]bool{}
	if b, err := os.ReadFile(readPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveRead(m map[string]bool) {
	p := readPath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.Marshal(m)
	_ = os.WriteFile(p, b, 0o600)
}

func readCall(c *dbus.Conn, p dbus.ObjectPath) (Call, error) {
	m, err := props(c, p, ifCall)
	if err != nil {
		return Call{}, err
	}
	return Call{ID: idOf(p), path: p, Number: textutil.Line(str(m, "Number")),
		State: callStates[int32(u32(m, "State"))], Incoming: u32(m, "Direction") == 1}, nil
}

// Calls lists the modem's calls.
func Calls() ([]Call, error) {
	obj, c, err := modemObject()
	if err != nil {
		return nil, err
	}
	var paths []dbus.ObjectPath
	if err := obj.Call(ifVoice+".ListCalls", 0).Store(&paths); err != nil {
		return nil, cleanDBus(err)
	}
	out := []Call{}
	for _, p := range paths {
		if call, err := readCall(c, p); err == nil {
			out = append(out, call)
		}
	}
	return out, nil
}

// Hangup rejects or ends a call.
func Hangup(id string) error {
	calls, err := Calls()
	if err != nil {
		return err
	}
	for _, call := range calls {
		if call.ID == id {
			c, err := systemBus()
			if err != nil {
				return err
			}
			return cleanDBus(c.Object(mmService, call.path).Call(ifCall+".Hangup", 0).Err)
		}
	}
	return fmt.Errorf("no call %s", id)
}

func cleanDBus(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(cleanError(err.Error()))
}

// Events reports what Watch observes.
type Events struct {
	Message   func(Message)
	Call      func(Call) // a new incoming call is ringing
	CallEnded func(Call)
}

// Watch delivers new incoming messages and calls until ctx ends. It listens
// to every modem, so it survives ModemManager re-probing the modem under a
// new object path.
func Watch(ctx context.Context, ev Events) error {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer c.Close()
	for _, m := range [][]dbus.MatchOption{
		{dbus.WithMatchInterface(ifMessaging), dbus.WithMatchMember("Added")},
		{dbus.WithMatchInterface(ifVoice), dbus.WithMatchMember("CallAdded")},
		{dbus.WithMatchInterface(ifCall), dbus.WithMatchMember("StateChanged")},
	} {
		if err := c.AddMatchSignal(m...); err != nil {
			return err
		}
	}
	sig := make(chan *dbus.Signal, 32)
	c.Signal(sig)
	for {
		select {
		case <-ctx.Done():
			return nil
		case s := <-sig:
			switch s.Name {
			case ifMessaging + ".Added":
				if len(s.Body) < 2 {
					continue
				}
				p, _ := s.Body[0].(dbus.ObjectPath)
				received, _ := s.Body[1].(bool)
				if received && ev.Message != nil {
					go deliverMessage(c, p, ev.Message)
				}
			case ifVoice + ".CallAdded":
				p, _ := s.Body[0].(dbus.ObjectPath)
				if call, err := readCall(c, p); err == nil && call.Incoming && ev.Call != nil {
					ev.Call(call)
				}
			case ifCall + ".StateChanged":
				if len(s.Body) >= 2 && ev.CallEnded != nil {
					if state, _ := s.Body[1].(int32); callStates[state] == "terminated" {
						call, _ := readCall(c, s.Path)
						call.ID, call.path = idOf(s.Path), s.Path
						ev.CallEnded(call)
					}
				}
			}
		}
	}
}

// deliverMessage waits for multi-part messages to finish arriving.
func deliverMessage(c *dbus.Conn, p dbus.ObjectPath, f func(Message)) {
	for i := 0; i < 20; i++ {
		m, err := readMessage(c, p)
		if err == nil && m.State != "receiving" {
			m.OnModem = true
			Archive(m)
			f(m)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
