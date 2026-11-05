package modem

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ConnectionName is the NetworkManager profile esimctl creates and manages.
const ConnectionName = "Mobile broadband"

// Connection describes the NetworkManager side of mobile data.
type Connection struct {
	Exists      bool   `json:"exists"`
	Active      bool   `json:"active"`
	State       string `json:"state"` // NM device state
	Device      string `json:"device,omitempty"`
	AutoAPN     bool   `json:"auto_apn"`
	APN         string `json:"apn,omitempty"` // manual APN, or the one in use
	Autoconnect bool   `json:"autoconnect"`
	Roaming     bool   `json:"roaming"` // data allowed while roaming
	IPv4        string `json:"ipv4,omitempty"`
	IPv6        string `json:"ipv6,omitempty"`
}

// nmFields runs nmcli in terse mode and returns key→value pairs.
func nmFields(args ...string) (map[string]string, error) {
	out, err := run("nmcli", append([]string{"-t", "-m", "multiline"}, args...)...)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			if _, dup := m[k]; !dup {
				m[k] = v
			}
		}
	}
	return m, nil
}

func gsmDevice() string {
	out, err := run("nmcli", "-t", "-f", "DEVICE,TYPE", "device")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if dev, typ, ok := strings.Cut(line, ":"); ok && typ == "gsm" {
			return dev
		}
	}
	return ""
}

// GetConnection reports the state of mobile data.
func GetConnection() (Connection, error) {
	c := Connection{Device: gsmDevice()}
	if s, err := nmFields("-f", "connection.id,connection.autoconnect,gsm.auto-config,gsm.apn,gsm.home-only",
		"connection", "show", ConnectionName); err == nil {
		c.Exists = true
		c.Autoconnect = s["connection.autoconnect"] == "yes"
		c.AutoAPN = s["gsm.auto-config"] == "yes"
		c.APN = s["gsm.apn"]
		c.Roaming = s["gsm.home-only"] != "yes"
	}
	if c.Device == "" {
		c.State = "unavailable"
		return c, nil
	}
	d, err := nmFields("-f", "GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS,IP6.ADDRESS", "device", "show", c.Device)
	if err != nil {
		return c, err
	}
	state := d["GENERAL.STATE"] // "100 (connected)"
	if i := strings.Index(state, "("); i >= 0 {
		state = strings.TrimSuffix(state[i+1:], ")")
	}
	c.State = state
	c.Active = state == "connected" && d["GENERAL.CONNECTION"] == ConnectionName
	if c.Active {
		c.IPv4 = strings.Split(d["IP4.ADDRESS[1]"], "/")[0]
		c.IPv6 = strings.Split(d["IP6.ADDRESS[1]"], "/")[0]
	}
	return c, nil
}

// Settings are the user's mobile data preferences.
type Settings struct {
	AutoAPN     bool   `json:"auto_apn"`
	APN         string `json:"apn,omitempty"`
	User        string `json:"user,omitempty"`
	Password    string `json:"password,omitempty"`
	Autoconnect bool   `json:"autoconnect"`
	Roaming     bool   `json:"roaming"`
	Limit       Limit  `json:"limit"`
	// ShowEmptySlots lists SIM slots with nothing in them; by default an
	// empty card slot is hidden and only the eSIM shows.
	ShowEmptySlots bool `json:"show_empty_slots"`
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "esimctl", "data.json")
}

// LoadSettings reads the preferences, defaulting to automatic APN,
// autoconnect on and no roaming.
func LoadSettings() Settings {
	s := Settings{AutoAPN: true, Autoconnect: true}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func SaveSettings(s Settings) error {
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func boolYN(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Resolved describes the APN Apply chose.
type Resolved struct {
	APN      string `json:"apn,omitempty"`
	Provider string `json:"provider,omitempty"`
	Source   string `json:"source"` // "manual", "database", "networkmanager"
}

// Apply writes the settings to the NetworkManager profile, creating it if
// needed. With AutoAPN the APN is looked up for the SIM currently active.
func Apply(st *Status, set Settings) (Resolved, error) {
	r := Resolved{Source: "manual", APN: set.APN}
	apn, user, pass := set.APN, set.User, set.Password
	if set.AutoAPN {
		r = Resolved{Source: "networkmanager"}
		apn, user, pass = "", "", ""
		if st != nil {
			slot, _ := st.ActiveSlot()
			if a, ok := BestAPN(st.MCCMNC(), slot.Operator); ok {
				r = Resolved{APN: a.Name, Provider: a.Provider, Source: "database"}
				apn, user, pass = a.Name, a.User, a.Password
			}
		}
	}
	props := []string{
		"connection.autoconnect", boolYN(set.Autoconnect),
		"gsm.home-only", boolYN(!set.Roaming),
		"gsm.auto-config", boolYN(apn == ""),
		"gsm.apn", apn,
		"gsm.username", user,
		"gsm.password", pass,
		"gsm.password-flags", "0",
	}
	if _, err := nmFields("-f", "connection.id", "connection", "show", ConnectionName); err == nil {
		_, err := run("nmcli", append([]string{"connection", "modify", ConnectionName}, props...)...)
		return r, err
	}
	args := append([]string{"connection", "add", "type", "gsm", "ifname", "*", "con-name", ConnectionName,
		"ipv4.method", "auto", "ipv6.method", "auto"}, props...)
	_, err := run("nmcli", args...)
	return r, err
}

// Connect applies the settings for the active SIM and activates mobile data.
func Connect(st *Status) (Resolved, error) {
	r, err := Apply(st, LoadSettings())
	if err != nil {
		return r, err
	}
	dev := gsmDevice()
	if dev == "" {
		return r, errors.New("NetworkManager doesn't see the modem")
	}
	_, err = run("nmcli", "connection", "up", ConnectionName, "ifname", dev)
	return r, err
}

// Disconnect deactivates mobile data. NetworkManager won't autoconnect a
// manually disconnected device until it is connected again.
func Disconnect() error {
	dev := gsmDevice()
	if dev == "" {
		return errors.New("NetworkManager doesn't see the modem")
	}
	_, err := run("nmcli", "device", "disconnect", dev)
	return err
}
