package main

import (
	"context"
	"fmt"

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

// readFix reports the receiver state and, when it is running, the current fix.
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
	return fix, err
}

func cmdGPS(ctx context.Context, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on", "off":
		if err := withAT(func(p *at.Port) error { return gnss.SetEnabled(p, sub == "on") }); err != nil {
			return err
		}
		return done("GPS " + sub)
	case "status":
		fix, err := readFix()
		if err != nil {
			return err
		}
		return output(fix, func() { printFix(fix) })
	}
	return fmt.Errorf("gps: unknown subcommand %q (status, on, off)", sub)
}

func printFix(f gnss.Fix) {
	if !f.Enabled {
		fmt.Println("GPS off (esimctl gps on)")
		return
	}
	if !f.Valid {
		fmt.Printf("searching… %d satellites in view\n", f.SatsInView)
		return
	}
	fmt.Printf("%.6f, %.6f  (%s fix, %d/%d satellites, HDOP %.1f)\n", f.Lat, f.Lon, f.Mode, f.SatsUsed, f.SatsInView, f.HDOP)
	fmt.Printf("altitude %.0f m, speed %.1f km/h, course %.0f°\n", f.Alt, f.SpeedKmh, f.Course)
	fmt.Printf("https://www.openstreetmap.org/?mlat=%.6f&mlon=%.6f#map=17/%.6f/%.6f\n", f.Lat, f.Lon, f.Lat, f.Lon)
}
