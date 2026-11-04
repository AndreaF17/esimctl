package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/AndreaF17/esimctl/internal/gnss"
	"github.com/AndreaF17/esimctl/internal/modem"
)

type modemReport struct {
	Modem      *modem.Status    `json:"modem"`
	Connection modem.Connection `json:"connection"`
	Settings   modem.Settings   `json:"settings"`
	// SuggestedAPN is what automatic mode would use for the active SIM.
	SuggestedAPN *modem.APN    `json:"suggested_apn,omitempty"`
	Problem      string        `json:"problem,omitempty"`
	Usage        *modem.Usage  `json:"usage,omitempty"`
	Messages     int           `json:"messages"`
	Unread       int           `json:"unread"`
	GPS          *gnss.Fix     `json:"gps,omitempty"`
	LastFix      *gnss.LastFix `json:"last_fix,omitempty"`
}

func getModemReport() (modemReport, error) {
	var r modemReport
	st, err := modem.Get()
	if err != nil {
		return r, err
	}
	r.Modem = st
	r.Problem = st.Problem()
	if u, err := modem.SampleUsage(time.Now()); err == nil {
		r.Usage = &u
	}
	r.Connection, _ = modem.GetConnection()
	r.Settings = modem.LoadSettings()
	if msgs, err := modem.Messages(); err == nil {
		r.Messages, r.Unread = len(msgs), modem.Unread(msgs)
	}
	if fix, err := readFix(); err == nil {
		r.GPS = &fix
	}
	r.LastFix, _ = gnss.Last()
	if slot, ok := st.ActiveSlot(); ok {
		if a, ok := modem.BestAPN(st.MCCMNC(), slot.Operator); ok {
			r.SuggestedAPN = &a
		}
	}
	return r, nil
}

func cmdModem(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		r, err := getModemReport()
		if err != nil {
			return err
		}
		return output(r, func() { printModem(r) })
	case "on", "off":
		st, err := modem.Get()
		if err != nil {
			return err
		}
		if err := st.SetEnabled(sub == "on"); err != nil {
			return explainEnable(err)
		}
		return done("radio " + sub)
	case "signal":
		st, err := modem.Get()
		if err != nil {
			return err
		}
		s, err := st.SignalDetails()
		if err != nil {
			return err
		}
		return output(s, func() {
			if s.Tech == "" {
				fmt.Println("no signal measurements (radio off or not registered)")
				return
			}
			fmt.Printf("%s  RSSI %.0f dBm  RSRP %.0f dBm  RSRQ %.1f dB  SNR %.1f dB\n", strings.ToUpper(s.Tech), s.RSSI, s.RSRP, s.RSRQ, s.SNR)
		})
	}
	return fmt.Errorf("modem: unknown subcommand %q (status, on, off, signal)", sub)
}

// explainEnable turns ModemManager's opaque enable failure on FCC-locked
// Lenovo modems into something actionable.
func explainEnable(err error) error {
	if strings.Contains(err.Error(), "Invalid transition") {
		return errors.New("the modem refused to turn the radio on; on Lenovo laptops this usually means the FCC lock is active (see README: FCC unlock)")
	}
	return err
}

func printModem(r modemReport) {
	st, c := r.Modem, r.Connection
	fmt.Printf("Modem:      %s %s (firmware %s)\n", st.Manufacturer, st.Model, st.Firmware)
	fmt.Printf("State:      %s, power %s\n", st.State, st.Power)
	if r.Problem != "" {
		fmt.Printf("Problem:    %s\n", r.Problem)
	}
	if st.UnlockRequired != "" {
		fmt.Printf("Locked:     %s required (%d attempts left)\n", st.UnlockRequired, st.UnlockRetries)
	}
	if st.Operator != "" || st.Registration != "" {
		roam := ""
		if st.Roaming {
			roam = " [roaming]"
		}
		fmt.Printf("Network:    %s %s, %s, signal %d%%%s\n", orDash(st.Operator), strings.Join(st.AccessTech, "/"), orDash(st.Registration), st.Signal, roam)
	}
	for _, s := range st.Slots {
		mark := "  "
		if s.Active {
			mark = "● "
		}
		desc := "empty"
		if s.Present {
			desc = s.Type
			if s.Operator != "" {
				desc += ", " + s.Operator
			}
			if s.ICCID != "" {
				desc += ", ICCID " + s.ICCID
			} else if s.Type == "esim" {
				desc += ", no profile enabled"
			}
		}
		fmt.Printf("Slot %d:     %s%s\n", s.Index, mark, desc)
	}
	data := "disconnected"
	if c.Active {
		data = "connected " + c.IPv4
	} else if c.State != "" {
		data = c.State
	}
	fmt.Printf("Data:       %s\n", data)
	apn := "automatic"
	if !r.Settings.AutoAPN {
		apn = r.Settings.APN
	} else if r.SuggestedAPN != nil {
		apn += " (" + r.SuggestedAPN.Name + ", " + r.SuggestedAPN.Provider + ")"
	}
	fmt.Printf("APN:        %s\n", apn)
	fmt.Printf("Options:    autoconnect %s, roaming %s\n", onOff(r.Settings.Autoconnect), onOff(r.Settings.Roaming))
	if u := r.Usage; u != nil {
		fmt.Printf("Usage:      %s\n", usageLine(*u))
	}
	if f := r.GPS; f != nil {
		switch {
		case !f.Enabled:
			fmt.Println("GPS:        off")
		case !f.Valid:
			fmt.Printf("GPS:        searching · %s\n", satSummary(*f))
		default:
			fmt.Printf("GPS:        %.6f, %.6f  (%s fix, %d/%d satellites, HDOP %.1f, altitude %.0f m)\n",
				f.Lat, f.Lon, strings.ToUpper(f.Mode), f.SatsUsed, f.SatsInView, f.HDOP, f.Alt)
			fmt.Printf("            %s\n", mapURL(f.Lat, f.Lon))
		}
		if lf := r.LastFix; lf != nil && !f.Valid {
			fmt.Printf("Last fix:   %.6f, %.6f  %s ago · %s\n", lf.Lat, lf.Lon, ago(lf.At), mapURL(lf.Lat, lf.Lon))
		}
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func cmdSIM(args []string) error {
	if len(args) == 0 {
		return cmdModem(nil)
	}
	st, err := modem.Get()
	if err != nil {
		return err
	}
	switch args[0] {
	case "slot":
		if len(args) != 2 {
			return errors.New("sim slot: expected a slot number")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > len(st.Slots) {
			return fmt.Errorf("sim slot: choose between 1 and %d", len(st.Slots))
		}
		if err := st.SetPrimarySlot(n); err != nil {
			return err
		}
		syncAfterSIMChange()
		return done(fmt.Sprintf("switched to slot %d", n))
	case "pin":
		pin, err := secretArg(args, 1, "SIM PIN")
		if err != nil {
			return err
		}
		if err := st.SendPIN(pin); err != nil {
			return err
		}
		syncAfterSIMChange()
		return done("SIM unlocked")
	case "puk":
		puk, err := secretArg(args, 1, "PUK")
		if err != nil {
			return err
		}
		newPIN, err := secretArg(args, 2, "New PIN")
		if err != nil {
			return err
		}
		if err := st.SendPUK(puk, newPIN); err != nil {
			return err
		}
		syncAfterSIMChange()
		return done("SIM unblocked, new PIN set")
	case "pin-lock":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("sim pin-lock: expected on|off")
		}
		pin, err := secretArg(args, 2, "SIM PIN")
		if err != nil {
			return err
		}
		if err := st.SetPINLock(pin, args[1] == "on"); err != nil {
			return err
		}
		return done("PIN request at power-on " + args[1])
	case "change-pin":
		oldPIN, err := secretArg(args, 1, "Current PIN")
		if err != nil {
			return err
		}
		newPIN, err := secretArg(args, 2, "New PIN")
		if err != nil {
			return err
		}
		if err := st.ChangePIN(oldPIN, newPIN); err != nil {
			return err
		}
		return done("PIN changed")
	}
	return fmt.Errorf("sim: unknown subcommand %q (slot, pin, puk, pin-lock, change-pin)", args[0])
}

// secretArg returns args[i], or asks for it on the terminal without echo:
// a PIN given on the command line is visible to other processes.
func secretArg(args []string, i int, label string) (string, error) {
	if i < len(args) {
		return args[i], nil
	}
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", fmt.Errorf("%s required", label)
	}
	noEcho := *old
	noEcho.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &noEcho); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, old)
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(line), nil
}

// limitBlocks reports whether the data cap keeps mobile data off.
func limitBlocks(u modem.Usage) bool {
	return u.Limit.Action == "stop" && u.Limit.Bytes > 0 && u.Bytes >= u.Limit.Bytes
}

func usageLine(u modem.Usage) string {
	since := u.PeriodStart.Format("02 Jan")
	if u.Limit.Bytes <= 0 {
		return fmt.Sprintf("%s since %s (no limit)", modem.FormatSize(u.Bytes), since)
	}
	act := "warn"
	if u.Limit.Action == "stop" {
		act = "stop data"
	}
	line := fmt.Sprintf("%s of %s (%.0f%%) since %s, resets %s · warn at %d%%, %s at the limit",
		modem.FormatSize(u.Bytes), modem.FormatSize(u.Limit.Bytes), u.Percent, since,
		u.NextReset.Format("02 Jan"), u.Limit.WarnPercent, act)
	if limitBlocks(u) {
		line += " · DATA STOPPED"
	}
	return line
}

// cmdLimit configures the data cap: "10GB", "off", "warn 80", "day 15",
// "action stop|warn".
func cmdLimit(set *modem.Settings, args []string, save func(string) error) error {
	if len(args) == 0 {
		return errors.New("data limit: expected a size (10GB), off, warn <percent>, day <1-28> or action stop|warn")
	}
	l := &set.Limit
	switch args[0] {
	case "off":
		l.Bytes = 0
		return save("data limit off")
	case "warn":
		if len(args) != 2 {
			return errors.New("data limit warn: expected a percentage")
		}
		p, err := strconv.Atoi(strings.TrimSuffix(args[1], "%"))
		if err != nil || p < 1 || p > 99 {
			return errors.New("data limit warn: percentage between 1 and 99")
		}
		l.WarnPercent = p
		return save(fmt.Sprintf("warning at %d%% of the limit", p))
	case "day":
		if len(args) != 2 {
			return errors.New("data limit day: expected the day of the month the count restarts")
		}
		d, err := strconv.Atoi(args[1])
		if err != nil || d < 1 || d > 28 {
			return errors.New("data limit day: between 1 and 28")
		}
		l.ResetDay = d
		return save(fmt.Sprintf("usage count restarts on day %d of each month", d))
	case "action":
		if len(args) != 2 || (args[1] != "stop" && args[1] != "warn") {
			return errors.New("data limit action: stop (disconnect at the limit) or warn")
		}
		l.Action = args[1]
		return save("at the limit: " + args[1])
	}
	b, err := modem.ParseSize(strings.Join(args, ""))
	if err != nil {
		return err
	}
	l.Bytes = b
	if l.WarnPercent == 0 {
		l.WarnPercent = 80
	}
	if l.ResetDay == 0 {
		l.ResetDay = 1
	}
	if l.Action == "" {
		l.Action = "warn"
	}
	return save("data limit " + modem.FormatSize(b))
}

// syncAfterSIMChange re-resolves the automatic APN once the active SIM can
// be read: after a slot switch ModemManager re-probes the modem, and a SIM
// unlocked with its PIN only then reveals its IMSI and home network.
func syncAfterSIMChange() {
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Second)
		st, err := modem.Get()
		if err != nil || st.State == "" || st.State == "initializing" || st.UnlockRequired != "" {
			continue
		}
		if s, ok := st.ActiveSlot(); ok && (s.IMSI != "" || s.HomeCode != "") {
			_, _ = modem.Apply(st, modem.LoadSettings())
			return
		}
	}
}

func cmdData(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	set := modem.LoadSettings()
	save := func(msg string) error {
		if err := modem.SaveSettings(set); err != nil {
			return err
		}
		st, _ := modem.Get()
		r, err := modem.Apply(st, set)
		if err != nil {
			return err
		}
		if r.APN != "" && msg == "APN set to automatic" {
			msg += fmt.Sprintf(" (%s, %s)", r.APN, r.Provider)
		}
		return done(msg)
	}
	switch sub {
	case "status":
		return cmdModem(nil)
	case "usage":
		if len(args) > 1 && args[1] == "reset" {
			if err := modem.ResetUsage(); err != nil {
				return err
			}
			return done("usage count reset for this period")
		}
		u, err := modem.SampleUsage(time.Now())
		if err != nil {
			return err
		}
		return output(u, func() { fmt.Println(usageLine(u)) })
	case "limit":
		return cmdLimit(&set, args[1:], save)
	case "on", "connect", "up":
		if u, err := modem.SampleUsage(time.Now()); err == nil && limitBlocks(u) {
			return fmt.Errorf("data limit of %s reached; raise it (esimctl data limit <size>) or wait until %s",
				modem.FormatSize(u.Limit.Bytes), u.NextReset.Format("02 Jan"))
		}
		st, err := modem.Get()
		if err != nil {
			return err
		}
		r, err := modem.Connect(st)
		if err != nil {
			return err
		}
		msg := "mobile data connected"
		if r.APN != "" {
			msg += " (APN " + r.APN + ")"
		}
		return done(msg)
	case "off", "disconnect", "down":
		if err := modem.Disconnect(); err != nil {
			return err
		}
		return done("mobile data disconnected")
	case "apn":
		if len(args) < 2 {
			return errors.New("data apn: expected 'auto' or an APN [user] [password]")
		}
		if args[1] == "auto" {
			set.AutoAPN, set.APN, set.User, set.Password = true, "", "", ""
			return save("APN set to automatic")
		}
		set.AutoAPN, set.APN = false, args[1]
		set.User, set.Password = "", ""
		if len(args) > 2 {
			set.User = args[2]
		}
		if len(args) > 3 {
			set.Password = args[3]
		}
		return save("APN set to " + args[1])
	case "apns":
		st, err := modem.Get()
		if err != nil {
			return err
		}
		code := st.MCCMNC()
		if len(args) > 1 {
			code = args[1]
		}
		apns, err := modem.LookupAPNs(code)
		if err != nil {
			return err
		}
		if apns == nil {
			apns = []modem.APN{}
		}
		return output(apns, func() {
			if len(apns) == 0 {
				fmt.Println("no known APNs for network", orDash(code))
			}
			for _, a := range apns {
				fmt.Printf("%-24s %-28s %s\n", a.Provider, a.Name, a.Label)
			}
		})
	case "roaming", "autoconnect":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return fmt.Errorf("data %s: expected on or off", sub)
		}
		if sub == "roaming" {
			set.Roaming = args[1] == "on"
		} else {
			set.Autoconnect = args[1] == "on"
		}
		return save(sub + " " + args[1])
	}
	return fmt.Errorf("data: unknown subcommand %q (status, on, off, apn, apns, roaming, autoconnect)", sub)
}
