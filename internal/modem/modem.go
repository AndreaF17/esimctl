// Package modem drives the cellular modem through ModemManager (mmcli) and
// its data connection through NetworkManager (nmcli).
package modem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Slot is one SIM slot of the modem.
type Slot struct {
	Index    int    `json:"index"`
	Present  bool   `json:"present"`
	Active   bool   `json:"active"`
	Type     string `json:"type,omitempty"` // "physical" or "esim"
	ICCID    string `json:"iccid,omitempty"`
	IMSI     string `json:"imsi,omitempty"`
	Operator string `json:"operator,omitempty"`
	HomeCode string `json:"home_code,omitempty"` // MCCMNC of the SIM's home network
	EID      string `json:"eid,omitempty"`
	path     string
}

// Status is a snapshot of the modem.
type Status struct {
	Present        bool     `json:"present"`
	Manufacturer   string   `json:"manufacturer,omitempty"`
	Model          string   `json:"model,omitempty"`
	Firmware       string   `json:"firmware,omitempty"`
	IMEI           string   `json:"imei,omitempty"`
	State          string   `json:"state"` // ModemManager state
	FailedReason   string   `json:"failed_reason,omitempty"`
	Power          string   `json:"power,omitempty"` // on, low, off
	Signal         int      `json:"signal"`          // percent
	AccessTech     []string `json:"access_tech"`
	Operator       string   `json:"operator,omitempty"`
	OperatorCode   string   `json:"operator_code,omitempty"`
	Registration   string   `json:"registration,omitempty"`
	Roaming        bool     `json:"roaming"`
	UnlockRequired string   `json:"unlock_required,omitempty"` // e.g. "sim-pin"
	UnlockRetries  int      `json:"unlock_retries,omitempty"`
	PINLock        bool     `json:"pin_lock"` // PIN requested at power-on
	PrimarySlot    int      `json:"primary_slot"`
	Slots          []Slot   `json:"slots"`
	OwnNumbers     []string `json:"own_numbers,omitempty"`

	path    string
	simPath string
}

// ErrNoModem is returned when ModemManager knows no modem.
var ErrNoModem = errors.New("no modem found (is ModemManager running?)")

func run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(cleanError(msg))
	}
	return out, nil
}

// cleanError strips D-Bus noise from mmcli/nmcli errors.
func cleanError(msg string) string {
	msg = strings.TrimPrefix(msg, "error: ")
	if i := strings.LastIndex(msg, "GDBus.Error:"); i >= 0 {
		rest := msg[i+len("GDBus.Error:"):]
		if j := strings.Index(rest, ": "); j >= 0 {
			rest = rest[j+2:]
		}
		msg = rest
	}
	return strings.TrimSuffix(strings.TrimSpace(msg), "'")
}

func mmcliJSON(v any, args ...string) error {
	out, err := run("mmcli", append([]string{"-J"}, args...)...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func val(s string) string {
	if s == "--" {
		return ""
	}
	return s
}

func atoi(s string) int {
	n, _ := strconv.Atoi(val(s))
	return n
}

// Get returns the state of the first modem.
func Get() (*Status, error) {
	var list struct {
		ModemList []string `json:"modem-list"`
	}
	if err := mmcliJSON(&list, "-L"); err != nil {
		return nil, err
	}
	if len(list.ModemList) == 0 {
		return nil, ErrNoModem
	}
	var m struct {
		Modem struct {
			DBusPath string `json:"dbus-path"`
			Generic  struct {
				AccessTech      []string               `json:"access-technologies"`
				Manufacturer    string                 `json:"manufacturer"`
				Model           string                 `json:"model"`
				Revision        string                 `json:"revision"`
				EquipmentID     string                 `json:"equipment-identifier"`
				OwnNumbers      []string               `json:"own-numbers"`
				PowerState      string                 `json:"power-state"`
				PrimarySimSlot  string                 `json:"primary-sim-slot"`
				SignalQuality   struct{ Value string } `json:"signal-quality"`
				Sim             string                 `json:"sim"`
				SimSlots        []string               `json:"sim-slots"`
				State           string                 `json:"state"`
				StateFailReason string                 `json:"state-failed-reason"`
				UnlockRequired  string                 `json:"unlock-required"`
				UnlockRetries   []string               `json:"unlock-retries"`
			} `json:"generic"`
			GPP struct {
				EnabledLocks []string `json:"enabled-locks"`
				IMEI         string   `json:"imei"`
				OperatorCode string   `json:"operator-code"`
				OperatorName string   `json:"operator-name"`
				Registration string   `json:"registration-state"`
			} `json:"3gpp"`
		} `json:"modem"`
	}
	if err := mmcliJSON(&m, "-m", list.ModemList[0]); err != nil {
		return nil, err
	}
	g, gp := m.Modem.Generic, m.Modem.GPP
	st := &Status{
		Present: true, path: m.Modem.DBusPath, simPath: val(g.Sim),
		Manufacturer: val(g.Manufacturer), Model: strings.ReplaceAll(val(g.Model), "_", "-"),
		Firmware: val(g.Revision), IMEI: val(gp.IMEI), State: val(g.State),
		FailedReason: val(g.StateFailReason), Power: val(g.PowerState),
		Signal: atoi(g.SignalQuality.Value), AccessTech: g.AccessTech,
		Operator: val(gp.OperatorName), OperatorCode: val(gp.OperatorCode),
		Registration: val(gp.Registration), Roaming: val(gp.Registration) == "roaming",
		PrimarySlot: atoi(g.PrimarySimSlot), OwnNumbers: g.OwnNumbers,
	}
	for _, l := range gp.EnabledLocks {
		st.PINLock = st.PINLock || l == "sim" || l == "sim-pin"
	}
	if st.IMEI == "" {
		st.IMEI = val(g.EquipmentID)
	}
	if u := val(g.UnlockRequired); u != "none" {
		st.UnlockRequired = u
	}
	for _, r := range g.UnlockRetries { // "sim-pin (3)"
		if st.UnlockRequired != "" && strings.HasPrefix(r, st.UnlockRequired+" (") {
			st.UnlockRetries = atoi(strings.TrimSuffix(strings.TrimPrefix(r, st.UnlockRequired+" ("), ")"))
		}
	}
	if st.AccessTech == nil {
		st.AccessTech = []string{}
	}

	slots := g.SimSlots
	if len(slots) == 0 && st.simPath != "" {
		slots = []string{st.simPath}
	}
	for i, p := range slots {
		s := Slot{Index: i + 1, path: p}
		if p != "/" && p != "" {
			s.Present = true
			s.Active = p == st.simPath
			fillSIM(&s)
		}
		st.Slots = append(st.Slots, s)
	}
	if st.PrimarySlot == 0 && len(st.Slots) == 1 {
		st.PrimarySlot = 1
	}
	return st, nil
}

func fillSIM(s *Slot) {
	var r struct {
		SIM struct {
			Properties struct {
				Active       string `json:"active"`
				EID          string `json:"eid"`
				ICCID        string `json:"iccid"`
				IMSI         string `json:"imsi"`
				OperatorName string `json:"operator-name"`
				OperatorCode string `json:"operator-code"`
				SimType      string `json:"sim-type"`
			} `json:"properties"`
		} `json:"sim"`
	}
	if mmcliJSON(&r, "-i", s.path) != nil {
		return
	}
	p := r.SIM.Properties
	s.Active = s.Active || p.Active == "yes"
	s.EID, s.ICCID, s.IMSI, s.Operator = val(p.EID), val(p.ICCID), val(p.IMSI), val(p.OperatorName)
	s.HomeCode = val(p.OperatorCode)
	s.Type = val(p.SimType)
	if s.Type == "" {
		s.Type = "physical"
	}
}

// MCCMNC returns the home network code of the active SIM. The IMSI prefix
// is the fallback: it doesn't say whether the MNC has 2 or 3 digits, which
// LookupAPNs accounts for.
func (st *Status) MCCMNC() string {
	s, ok := st.ActiveSlot()
	switch {
	case !ok:
		return ""
	case s.HomeCode != "":
		return s.HomeCode
	case len(s.IMSI) >= 6:
		return s.IMSI[:6]
	}
	return ""
}

// ActiveSlot returns the slot in use, if any.
func (st *Status) ActiveSlot() (Slot, bool) {
	for _, s := range st.Slots {
		if s.Active {
			return s, true
		}
	}
	return Slot{}, false
}

func (st *Status) modemArg() []string { return []string{"-m", st.path} }

// SetEnabled turns the radio on (registering on the network) or off.
func (st *Status) SetEnabled(on bool) error {
	flag := "-d"
	if on {
		flag = "-e"
	}
	_, err := run("mmcli", append(st.modemArg(), flag)...)
	return err
}

// SetPrimarySlot switches the active SIM slot. The modem re-initializes.
func (st *Status) SetPrimarySlot(n int) error {
	_, err := run("mmcli", append(st.modemArg(), fmt.Sprintf("--set-primary-sim-slot=%d", n))...)
	return err
}

func (st *Status) sim() (string, error) {
	if st.simPath == "" {
		return "", errors.New("no SIM in the active slot")
	}
	return st.simPath, nil
}

// SendPIN unlocks the SIM.
func (st *Status) SendPIN(pin string) error {
	sim, err := st.sim()
	if err != nil {
		return err
	}
	_, err = run("mmcli", "-i", sim, "--pin="+pin)
	return err
}

// SendPUK unblocks the SIM and sets a new PIN.
func (st *Status) SendPUK(puk, newPIN string) error {
	sim, err := st.sim()
	if err != nil {
		return err
	}
	_, err = run("mmcli", "-i", sim, "--puk="+puk, "--pin="+newPIN)
	return err
}

// SetPINLock enables or disables the PIN request at power-on.
func (st *Status) SetPINLock(pin string, on bool) error {
	sim, err := st.sim()
	if err != nil {
		return err
	}
	flag := "--disable-pin"
	if on {
		flag = "--enable-pin"
	}
	_, err = run("mmcli", "-i", sim, "--pin="+pin, flag)
	return err
}

// ChangePIN changes the SIM PIN.
func (st *Status) ChangePIN(oldPIN, newPIN string) error {
	sim, err := st.sim()
	if err != nil {
		return err
	}
	_, err = run("mmcli", "-i", sim, "--pin="+oldPIN, "--change-pin="+newPIN)
	return err
}

// Signal holds detailed radio measurements.
type Signal struct {
	Tech string  `json:"tech"`
	RSSI float64 `json:"rssi,omitempty"`
	RSRP float64 `json:"rsrp,omitempty"`
	RSRQ float64 `json:"rsrq,omitempty"`
	SNR  float64 `json:"snr,omitempty"`
}

// SignalDetails returns detailed measurements, enabling periodic sampling if needed.
func (st *Status) SignalDetails() (Signal, error) {
	get := func() (map[string]map[string]string, error) {
		var r struct {
			Modem struct {
				Signal map[string]json.RawMessage `json:"signal"`
			} `json:"modem"`
		}
		if err := mmcliJSON(&r, append(st.modemArg(), "--signal-get")...); err != nil {
			return nil, err
		}
		out := map[string]map[string]string{}
		for k, raw := range r.Modem.Signal {
			m := map[string]string{}
			if json.Unmarshal(raw, &m) == nil {
				out[k] = m
			}
		}
		return out, nil
	}
	data, err := get()
	if err != nil {
		return Signal{}, err
	}
	if rate := data["refresh"]["rate"]; rate == "" || rate == "--" || rate == "0" {
		if _, err := run("mmcli", append(st.modemArg(), "--signal-setup=5")...); err == nil {
			time.Sleep(1500 * time.Millisecond)
			if d, err := get(); err == nil {
				data = d
			}
		}
	}
	for _, tech := range []string{"5g", "lte", "umts", "gsm"} {
		m := data[tech]
		if m == nil {
			continue
		}
		f := func(k string) float64 { x, _ := strconv.ParseFloat(val(m[k]), 64); return x }
		s := Signal{Tech: tech, RSSI: f("rssi"), RSRP: f("rsrp"), RSRQ: f("rsrq"), SNR: f("snr")}
		if s.RSSI != 0 || s.RSRP != 0 {
			return s, nil
		}
	}
	return Signal{}, nil
}
