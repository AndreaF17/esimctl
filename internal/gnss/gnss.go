// Package gnss controls the modem's GNSS receiver (Quectel AT+QGPS) and
// decodes the NMEA stream of its /dev/gnss* port.
package gnss

import (
	"bufio"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/at"
)

// Fix is the receiver state assembled from NMEA sentences.
type Fix struct {
	Enabled    bool      `json:"enabled"`
	Valid      bool      `json:"valid"`
	Mode       string    `json:"mode"` // "none", "2d", "3d"
	Lat        float64   `json:"lat"`
	Lon        float64   `json:"lon"`
	Alt        float64   `json:"alt"`          // metres above mean sea level
	SpeedKmh   float64   `json:"speed_kmh"`    //
	Course     float64   `json:"course"`       // degrees true
	HDOP       float64   `json:"hdop"`         //
	SatsUsed   int       `json:"sats_used"`    //
	SatsInView int       `json:"sats_in_view"` //
	Time       time.Time `json:"time,omitempty"`

	inView map[string]int // per constellation (GP, GL, GA, BD, ...)
}

// Enabled reports whether the receiver is running.
func Enabled(p *at.Port) (bool, error) {
	v, err := p.Query("AT+QGPS?", "+QGPS:")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(v) == "1", nil
}

// SetEnabled starts or stops the receiver.
func SetEnabled(p *at.Port, on bool) error {
	cmd := "AT+QGPSEND"
	if on {
		cmd = "AT+QGPS=1"
	}
	_, err := p.Command(cmd)
	var ae *at.Error
	// 504: session already ongoing, 505: session not active.
	if errors.As(err, &ae) && (strings.HasSuffix(ae.Msg, " 504") || strings.HasSuffix(ae.Msg, " 505")) {
		return nil
	}
	return err
}

// FindPort returns the NMEA port.
func FindPort() (string, error) {
	m, _ := filepath.Glob("/dev/gnss*")
	if len(m) == 0 {
		return "", errors.New("no GNSS port found (/dev/gnss*)")
	}
	return m[0], nil
}

// Read collects NMEA sentences for up to d, returning early once a full
// cycle with a valid fix has been seen.
func Read(ctx context.Context, path string, d time.Duration) (Fix, error) {
	f, err := os.Open(path)
	if err != nil {
		return Fix{}, err
	}
	defer f.Close()
	deadline := time.Now().Add(d)
	_ = f.SetReadDeadline(deadline)
	go func() {
		<-ctx.Done()
		_ = f.SetReadDeadline(time.Now())
	}()

	var fix Fix
	fix.inView = map[string]int{}
	sawGGA, sawRMC := false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		switch Parse(&fix, sc.Text()) {
		case "GGA":
			sawGGA = true
		case "RMC":
			sawRMC = true
		}
		if sawGGA && sawRMC && fix.Valid {
			break
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		return fix, err
	}
	fix.SatsInView = 0
	for _, n := range fix.inView {
		fix.SatsInView += n
	}
	return fix, nil
}

// Parse folds one NMEA sentence into fix and returns its type (GGA, RMC, ...)
// or "" when the sentence is invalid or ignored.
func Parse(fix *Fix, line string) string {
	line = strings.TrimSpace(line)
	if len(line) < 7 || line[0] != '$' || !checksumOK(line) {
		return ""
	}
	if i := strings.IndexByte(line, '*'); i > 0 {
		line = line[:i]
	}
	f := strings.Split(line[1:], ",")
	talker, typ := f[0][:2], f[0][2:]
	switch typ {
	case "GGA":
		if len(f) < 10 {
			return ""
		}
		q, _ := strconv.Atoi(f[6])
		if q > 0 {
			fix.Lat, fix.Lon = coord(f[2], f[3]), coord(f[4], f[5])
			fix.Alt = num(f[9])
			if fix.Mode == "" || fix.Mode == "none" {
				fix.Mode = "2d"
			}
		} else {
			fix.Mode = "none"
		}
		fix.SatsUsed, _ = strconv.Atoi(f[7])
		fix.HDOP = num(f[8])
	case "RMC":
		if len(f) < 10 {
			return ""
		}
		fix.Valid = f[2] == "A"
		if fix.Valid {
			fix.Lat, fix.Lon = coord(f[3], f[4]), coord(f[5], f[6])
			fix.SpeedKmh = math.Round(num(f[7])*1.852*10) / 10
			fix.Course = num(f[8])
		}
		if t, err := time.Parse("020106150405", f[9]+strings.SplitN(f[1], ".", 2)[0]); err == nil {
			fix.Time = t
		}
	case "GSA":
		if len(f) < 3 {
			return ""
		}
		switch f[2] {
		case "2":
			fix.Mode = "2d"
		case "3":
			fix.Mode = "3d"
		default:
			fix.Mode = "none"
		}
	case "GSV":
		if len(f) < 4 {
			return ""
		}
		if fix.inView == nil {
			fix.inView = map[string]int{}
		}
		fix.inView[talker], _ = strconv.Atoi(f[3])
	default:
		return ""
	}
	return typ
}

func checksumOK(line string) bool {
	i := strings.IndexByte(line, '*')
	if i < 0 {
		return true // checksum is optional
	}
	want, err := strconv.ParseUint(strings.TrimSpace(line[i+1:]), 16, 8)
	if err != nil {
		return false
	}
	var sum byte
	for j := 1; j < i; j++ {
		sum ^= line[j]
	}
	return sum == byte(want)
}

// coord converts NMEA ddmm.mmmm / dddmm.mmmm plus hemisphere to degrees.
func coord(v, hemi string) float64 {
	x, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	deg := math.Floor(x / 100)
	d := deg + (x-deg*100)/60
	if hemi == "S" || hemi == "W" {
		d = -d
	}
	return math.Round(d*1e6) / 1e6
}

func num(v string) float64 {
	x, _ := strconv.ParseFloat(v, 64)
	return x
}
