package gnss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/at"
)

// XTRA is Qualcomm's assistance data (predicted satellite orbits). Injected
// into the modem it shortens the time to a first fix from many minutes to
// seconds and helps with weak signal.
var xtraURLs = []string{
	"https://xtrapath1.izatcloud.net/xtra3grc.bin",
	"https://xtrapath2.izatcloud.net/xtra3grc.bin",
	"https://xtrapath3.izatcloud.net/xtra3grc.bin",
}

const xtraFile = "UFS:xtra3grc.bin"

// DownloadXTRA fetches current assistance data.
func DownloadXTRA(ctx context.Context) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr error
	for _, u := range xtraURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || len(data) < 1024 {
			lastErr = fmt.Errorf("XTRA download from %s failed (HTTP %d)", u, res.StatusCode)
			continue
		}
		return data, nil
	}
	return nil, lastErr
}

// XTRAStatus reports when the injected assistance data starts and how many
// minutes it stays valid (zero when none was ever injected).
func XTRAStatus(p *at.Port) (start time.Time, validMinutes int, err error) {
	v, err := p.Query("AT+QGPSXTRADATA?", "+QGPSXTRADATA:")
	if err != nil {
		return time.Time{}, 0, err
	}
	dur, date, _ := strings.Cut(v, ",")
	validMinutes, _ = strconv.Atoi(strings.TrimSpace(dur))
	start, _ = time.Parse("2006/01/02,15:04:05", strings.Trim(strings.TrimSpace(date), `"`))
	// Some firmwares report the GPS epoch (1980) instead of the data's start:
	// fall back to when esimctl injected it.
	if start.Year() < 2000 {
		if t, err := injectedAt(); err == nil {
			start = t
		}
	}
	return start, validMinutes, nil
}

func injectedPath() string { return filepath.Join(filepath.Dir(lastFixPath()), "xtra-injected") }

func injectedAt() (time.Time, error) {
	b, err := os.ReadFile(injectedPath())
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
}

// XTRAFresh reports whether injected assistance data is still valid now.
func XTRAFresh(p *at.Port) bool {
	start, minutes, err := XTRAStatus(p)
	return err == nil && minutes > 0 && time.Now().Before(start.Add(time.Duration(minutes)*time.Minute))
}

// InjectXTRA uploads assistance data and the current UTC time to the modem.
// The receiver is stopped while the data is loaded and restarted after.
func InjectXTRA(p *at.Port, data []byte) error {
	wasOn, err := Enabled(p)
	if err != nil {
		return err
	}
	if wasOn {
		if err := SetEnabled(p, false); err != nil {
			return err
		}
	}
	defer func() {
		if wasOn {
			_ = SetEnabled(p, true)
		}
	}()
	_, _ = p.Command(fmt.Sprintf(`AT+QFDEL="%s"`, xtraFile))
	if err := p.Upload(xtraFile, data); err != nil {
		return fmt.Errorf("uploading XTRA data: %w", err)
	}
	defer p.Command(fmt.Sprintf(`AT+QFDEL="%s"`, xtraFile))
	now := time.Now().UTC().Format("2006/01/02,15:04:05")
	if _, err := p.Command(fmt.Sprintf(`AT+QGPSXTRATIME="%s",50`, now)); err != nil {
		return fmt.Errorf("setting XTRA time: %w", err)
	}
	if _, err := p.Command(fmt.Sprintf(`AT+QGPSXTRADATA="%s"`, xtraFile)); err != nil {
		return fmt.Errorf("injecting XTRA data: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(injectedPath()), 0o700); err == nil {
		_ = os.WriteFile(injectedPath(), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
	}
	return nil
}

// LastFix is the most recent valid position, kept across restarts.
type LastFix struct {
	Lat  float64   `json:"lat"`
	Lon  float64   `json:"lon"`
	Alt  float64   `json:"alt"`
	HDOP float64   `json:"hdop"`
	At   time.Time `json:"at"`
}

func lastFixPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".local/state")
	}
	return filepath.Join(dir, "esimctl", "last-fix.json")
}

// RememberFix stores a valid fix as the last known position.
func RememberFix(f Fix) error {
	if !f.Valid {
		return errors.New("no fix")
	}
	lf := LastFix{Lat: f.Lat, Lon: f.Lon, Alt: f.Alt, HDOP: f.HDOP, At: time.Now()}
	if f.Time != nil {
		lf.At = *f.Time
	}
	p := lastFixPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(lf)
	return os.WriteFile(p, b, 0o600)
}

// Last returns the last known position, if any.
func Last() (*LastFix, error) {
	b, err := os.ReadFile(lastFixPath())
	if err != nil {
		return nil, err
	}
	var lf LastFix
	if err := json.Unmarshal(b, &lf); err != nil {
		return nil, err
	}
	return &lf, nil
}
