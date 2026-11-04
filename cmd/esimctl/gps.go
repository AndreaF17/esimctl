package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/at"
	"github.com/AndreaF17/esimctl/internal/gnss"
)

func withAT(f func(*at.Port) error) error {
	path, err := at.FindPort()
	if err != nil {
		return err
	}
	p, err := at.Open(path)
	if err != nil {
		return err
	}
	defer p.Close()
	return f(p)
}

// readFix reports the receiver state and, when it is running, the current
// fix. A valid fix is remembered as the last known position.
func readFix() (gnss.Fix, error) {
	fix := gnss.Fix{Mode: "none"}
	err := withAT(func(p *at.Port) error {
		on, err := gnss.Enabled(p)
		if err != nil || !on {
			return err
		}
		fix, err = gnss.Query(p)
		return err
	})
	if err == nil && fix.Valid {
		_ = gnss.RememberFix(fix)
	}
	return fix, err
}

type gpsReport struct {
	gnss.Fix
	XTRAValidUntil *time.Time    `json:"xtra_valid_until,omitempty"`
	LastFix        *gnss.LastFix `json:"last_fix,omitempty"`
}

func cmdGPS(ctx context.Context, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on":
		if err := withAT(func(p *at.Port) error { return gnss.SetEnabled(p, true) }); err != nil {
			return err
		}
		msg := "GPS on"
		// Assistance data makes the first fix minutes faster; best effort.
		if err := assistIfStale(ctx); err != nil {
			msg += " (no assistance data: " + err.Error() + ")"
		}
		return done(msg)
	case "off":
		if err := withAT(func(p *at.Port) error { return gnss.SetEnabled(p, false) }); err != nil {
			return err
		}
		return done("GPS off")
	case "assist":
		if err := assist(ctx); err != nil {
			return err
		}
		return done("assistance data loaded")
	case "status":
		r := gpsReport{}
		var err error
		if r.Fix, err = readFix(); err != nil {
			return err
		}
		_ = withAT(func(p *at.Port) error {
			if start, minutes, err := gnss.XTRAStatus(p); err == nil && minutes > 0 {
				until := start.Add(time.Duration(minutes) * time.Minute)
				r.XTRAValidUntil = &until
			}
			return nil
		})
		r.LastFix, _ = gnss.Last()
		return output(r, func() { printGPS(r) })
	}
	return fmt.Errorf("gps: unknown subcommand %q (status, on, off, assist)", sub)
}

func assist(ctx context.Context) error {
	data, err := gnss.DownloadXTRA(ctx)
	if err != nil {
		return err
	}
	return withAT(func(p *at.Port) error { return gnss.InjectXTRA(p, data) })
}

func assistIfStale(ctx context.Context) error {
	fresh := false
	_ = withAT(func(p *at.Port) error { fresh = gnss.XTRAFresh(p); return nil })
	if fresh {
		return nil
	}
	return assist(ctx)
}

func printGPS(r gpsReport) {
	f := r.Fix
	switch {
	case !f.Enabled:
		fmt.Println("GPS:       off (esimctl gps on)")
	case f.Valid:
		fmt.Printf("Position:  %.6f, %.6f  (%s fix, %d/%d satellites, HDOP %.1f)\n", f.Lat, f.Lon, strings.ToUpper(f.Mode), f.SatsUsed, f.SatsInView, f.HDOP)
		fmt.Printf("           altitude %.0f m, speed %.1f km/h, course %.0f°\n", f.Alt, f.SpeedKmh, f.Course)
		fmt.Printf("           %s\n", mapURL(f.Lat, f.Lon))
	default:
		fmt.Printf("GPS:       searching · %s\n", satSummary(f))
	}
	if f.Enabled {
		if r.XTRAValidUntil != nil {
			if r.XTRAValidUntil.Before(time.Now()) {
				fmt.Println("Assist:    XTRA data expired (esimctl gps assist)")
			} else {
				fmt.Printf("Assist:    XTRA data valid until %s\n", r.XTRAValidUntil.Local().Format("02 Jan 2006 15:04"))
			}
		} else {
			fmt.Println("Assist:    no XTRA data (esimctl gps assist)")
		}
	}
	if lf := r.LastFix; lf != nil && !f.Valid {
		fmt.Printf("Last fix:  %.6f, %.6f  %s ago\n           %s\n", lf.Lat, lf.Lon, ago(lf.At), mapURL(lf.Lat, lf.Lon))
	} else if lf == nil && !f.Valid {
		fmt.Println("Last fix:  none yet")
	}
}

func satSummary(f gnss.Fix) string {
	s := fmt.Sprintf("%d satellites in view", f.SatsInView)
	if f.SatsInView > 0 {
		s += fmt.Sprintf(", %d with signal", f.SatsHeard)
		if f.BestSNR > 0 {
			s += fmt.Sprintf(" (best %d dB-Hz)", f.BestSNR)
		}
	}
	if f.SatsHeard < 4 {
		s += "; a fix needs 4 — try near a window or outdoors"
	}
	return s
}

func mapURL(lat, lon float64) string {
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f", lat, lon, lat, lon)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}
