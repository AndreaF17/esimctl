package modem

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limit is the user's mobile data cap. Sizes are decimal (1 GB = 10^9 bytes),
// as carriers count them.
type Limit struct {
	Bytes       int64  `json:"bytes"`        // 0: no limit
	WarnPercent int    `json:"warn_percent"` // notify at this share of the limit
	ResetDay    int    `json:"reset_day"`    // day of the month the count restarts (1-28)
	Action      string `json:"action"`       // "warn" or "stop" (disconnect at the limit)
}

func (l Limit) normalized() Limit {
	if l.WarnPercent <= 0 || l.WarnPercent >= 100 {
		l.WarnPercent = 80
	}
	if l.ResetDay < 1 || l.ResetDay > 28 {
		l.ResetDay = 1
	}
	if l.Action != "stop" {
		l.Action = "warn"
	}
	return l
}

// Usage is the mobile data used in the current period.
type Usage struct {
	Bytes       int64     `json:"bytes"`
	PeriodStart time.Time `json:"period_start"`
	NextReset   time.Time `json:"next_reset"`
	Limit       Limit     `json:"limit"`
	Percent     float64   `json:"percent"` // of the limit, 0 without one
	Warned      bool      `json:"warned"`
	Stopped     bool      `json:"stopped"` // data was cut off at the limit

	// Last kernel counters, to add up deltas across samples.
	LastCounter uint64 `json:"last_counter"`
	Iface       string `json:"iface"`
}

// periodStart returns the start of the billing period containing t.
func periodStart(t time.Time, resetDay int) time.Time {
	y, m, d := t.Date()
	start := time.Date(y, m, resetDay, 0, 0, 0, 0, t.Location())
	if d < resetDay {
		start = start.AddDate(0, -1, 0)
	}
	return start
}

func usagePath() string { return filepath.Join(filepath.Dir(readPath()), "data-usage.json") }

// lockUsage serializes the watcher and CLI updating the usage file.
func lockUsage() (func(), error) {
	p := usagePath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// mobileIface returns the kernel network interface of the modem.
func mobileIface() string {
	m, _ := filepath.Glob("/sys/class/net/wwan*")
	if len(m) == 0 {
		return ""
	}
	return filepath.Base(m[0])
}

func readCounter(iface string) (uint64, bool) {
	var total uint64
	for _, k := range []string{"rx_bytes", "tx_bytes"} {
		b, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", k))
		if err != nil {
			return 0, false
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return 0, false
		}
		total += v
	}
	return total, true
}

// addSample folds a new counter reading into u. Kernel counters restart at
// zero when the modem's interface is recreated; a lower reading is then all
// new traffic.
func (u *Usage) addSample(counter uint64) {
	switch {
	case u.LastCounter == 0 && u.Bytes == 0:
		// First sample ever: start counting from here, the past is unknown.
	case counter >= u.LastCounter:
		u.Bytes += int64(counter - u.LastCounter)
	default:
		u.Bytes += int64(counter)
	}
	u.LastCounter = counter
}

// SampleUsage reads the interface counters, adds the traffic since the last
// sample and returns the updated usage for the current period.
func SampleUsage(now time.Time) (Usage, error) {
	unlock, err := lockUsage()
	if err != nil {
		return Usage{}, err
	}
	defer unlock()

	var u Usage
	if b, err := os.ReadFile(usagePath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	u.Limit = LoadSettings().Limit.normalized()
	start := periodStart(now, u.Limit.ResetDay)
	if !u.PeriodStart.Equal(start) {
		// New period (or a different reset day): the count starts over.
		u.PeriodStart, u.Bytes, u.Warned, u.Stopped = start, 0, false, false
	}
	u.NextReset = start.AddDate(0, 1, 0)

	if iface := mobileIface(); iface != "" {
		if iface != u.Iface {
			u.Iface, u.LastCounter = iface, 0
		}
		if c, ok := readCounter(iface); ok {
			if u.LastCounter == 0 && u.Bytes > 0 {
				u.LastCounter = c // interface reappeared: count from here
			}
			u.addSample(c)
		}
	}
	if u.Limit.Bytes > 0 {
		u.Percent = math.Round(float64(u.Bytes)/float64(u.Limit.Bytes)*1000) / 10
	} else {
		u.Percent = 0
	}
	return u, saveUsage(u)
}

// UpdateUsageFlags records that the warning was shown or data was stopped.
func UpdateUsageFlags(warned, stopped bool) error {
	unlock, err := lockUsage()
	if err != nil {
		return err
	}
	defer unlock()
	var u Usage
	if b, err := os.ReadFile(usagePath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	u.Warned, u.Stopped = warned, stopped
	return saveUsage(u)
}

// ResetUsage starts the count of the current period over.
func ResetUsage() error {
	unlock, err := lockUsage()
	if err != nil {
		return err
	}
	defer unlock()
	var u Usage
	if b, err := os.ReadFile(usagePath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	u.Bytes, u.Warned, u.Stopped = 0, false, false
	return saveUsage(u)
}

func saveUsage(u Usage) error {
	b, _ := json.Marshal(u)
	tmp := usagePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, usagePath())
}

// ParseSize reads sizes like "10GB", "500 MB", "1.5G" (decimal units).
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	mult := 1.0
	for _, u := range []struct {
		suffix string
		mult   float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"K", 1e3}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0, errors.New("invalid size (examples: 10GB, 500MB)")
	}
	return int64(v * mult), nil
}

// FormatSize renders bytes in decimal units.
func FormatSize(b int64) string {
	switch {
	case b >= 1e9:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(b)/1e9), "0"), ".") + " GB"
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%.0f KB", float64(b)/1e3)
	}
	return fmt.Sprintf("%d B", b)
}
