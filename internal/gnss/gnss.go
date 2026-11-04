// Package gnss controls the modem's GNSS receiver through Quectel AT
// commands (AT+QGPS, AT+QGPSLOC, AT+QGPSGNMEA).
//
// The NMEA port (/dev/gnss*) is deliberately not used: on the EM160R, when
// NMEA output is routed there and nobody reads it, the modem's shared
// transmit queue fills up and the AT port stops answering within seconds.
// SetEnabled therefore routes NMEA output nowhere, and sentences are pulled
// on demand over AT instead.
package gnss

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/at"
)

// Fix is the receiver state assembled from NMEA sentences.
type Fix struct {
	Enabled    bool       `json:"enabled"`
	Valid      bool       `json:"valid"`
	Mode       string     `json:"mode"` // "none", "2d", "3d"
	Lat        float64    `json:"lat"`
	Lon        float64    `json:"lon"`
	Alt        float64    `json:"alt"`          // metres above mean sea level
	SpeedKmh   float64    `json:"speed_kmh"`    //
	Course     float64    `json:"course"`       // degrees true
	HDOP       float64    `json:"hdop"`         //
	SatsUsed   int        `json:"sats_used"`    //
	SatsInView int        `json:"sats_in_view"` //
	SatsHeard  int        `json:"sats_heard"`   // in view with a measurable signal
	BestSNR    int        `json:"best_snr"`     // dB-Hz, strongest satellite
	Time       *time.Time `json:"time,omitempty"`

	inView map[string]int // per constellation (GP, GL, GA, BD, ...)
	snr    map[string]int // per talker+PRN
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
		// See the package comment: never stream NMEA to an unread port, and
		// keep sentences available to AT+QGPSGNMEA.
		if _, err := p.Command(`AT+QGPSCFG="outport","none"`); err != nil {
			return err
		}
		if _, err := p.Command(`AT+QGPSCFG="nmeasrc",1`); err != nil {
			return err
		}
	}
	_, err := p.Command(cmd)
	var ae *at.Error
	// 504: session already ongoing, 505: session not active.
	if errors.As(err, &ae) && (strings.HasSuffix(ae.Msg, " 504") || strings.HasSuffix(ae.Msg, " 505")) {
		return nil
	}
	return err
}

// Query reads the current fix over AT. It must only be called with the
// receiver enabled.
func Query(p *at.Port) (Fix, error) {
	fix := Fix{Enabled: true, Mode: "none", inView: map[string]int{}}
	// Satellites in view, from the GSV sentences of every constellation.
	if lines, err := p.Command(`AT+QGPSGNMEA="GSV"`); err == nil {
		for _, l := range lines {
			Parse(&fix, strings.TrimSpace(strings.TrimPrefix(l, "+QGPSGNMEA:")))
		}
	}
	for _, n := range fix.inView {
		fix.SatsInView += n
	}
	v, err := p.Query("AT+QGPSLOC=2", "+QGPSLOC:")
	var ae *at.Error
	if errors.As(err, &ae) && strings.HasSuffix(ae.Msg, " 516") { // not fixed yet
		return fix, nil
	}
	if err != nil {
		return fix, err
	}
	if err := ParseLoc(&fix, v); err != nil {
		return fix, err
	}
	return fix, nil
}

// ParseLoc decodes an AT+QGPSLOC=2 payload:
// <UTC>,<lat>,<lon>,<HDOP>,<alt>,<fix>,<COG>,<spkm>,<spkn>,<date>,<nsat>
func ParseLoc(fix *Fix, v string) error {
	f := strings.Split(v, ",")
	if len(f) < 11 {
		return errors.New("gnss: bad +QGPSLOC response " + v)
	}
	fix.Lat, fix.Lon = num(f[1]), num(f[2])
	fix.HDOP, fix.Alt = num(f[3]), num(f[4])
	switch f[5] {
	case "2":
		fix.Mode = "2d"
	case "3":
		fix.Mode = "3d"
	}
	fix.Valid = fix.Mode != "none"
	fix.Course = cog(f[6])
	fix.SpeedKmh = num(f[7])
	fix.SatsUsed, _ = strconv.Atoi(f[10])
	if t, err := time.Parse("020106150405", f[9]+strings.SplitN(f[0], ".", 2)[0]); err == nil {
		fix.Time = &t
	}
	if fix.SatsInView < fix.SatsUsed {
		fix.SatsInView = fix.SatsUsed
	}
	return nil
}

// cog decodes the ddd.mm course over ground (degrees and minutes).
func cog(v string) float64 {
	x := num(v)
	deg := math.Floor(x)
	return math.Round((deg+(x-deg)*100/60)*10) / 10
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
			fix.Time = &t
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
		if fix.snr == nil {
			fix.snr = map[string]int{}
		}
		fix.inView[talker], _ = strconv.Atoi(f[3])
		// Satellite blocks: PRN, elevation, azimuth, SNR (empty if not heard).
		for i := 4; i+3 < len(f); i += 4 {
			if f[i] == "" {
				continue
			}
			snr, err := strconv.Atoi(f[i+3])
			if err != nil {
				snr = 0
			}
			fix.snr[talker+f[i]] = snr
		}
		fix.SatsHeard, fix.BestSNR = 0, 0
		for _, v := range fix.snr {
			if v > 0 {
				fix.SatsHeard++
			}
			fix.BestSNR = max(fix.BestSNR, v)
		}
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
