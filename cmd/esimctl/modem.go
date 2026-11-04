package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/modem"
)

type modemReport struct {
	Modem      *modem.Status    `json:"modem"`
	Connection modem.Connection `json:"connection"`
	Settings   modem.Settings   `json:"settings"`
	// SuggestedAPN is what automatic mode would use for the active SIM.
	SuggestedAPN *modem.APN `json:"suggested_apn,omitempty"`
}

func getModemReport() (modemReport, error) {
	var r modemReport
	st, err := modem.Get()
	if err != nil {
		return r, err
	}
	r.Modem = st
	r.Connection, _ = modem.GetConnection()
	r.Settings = modem.LoadSettings()
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
		if len(args) != 2 {
			return errors.New("sim pin: expected the PIN")
		}
		if err := st.SendPIN(args[1]); err != nil {
			return err
		}
		syncAfterSIMChange()
		return done("SIM unlocked")
	case "puk":
		if len(args) != 3 {
			return errors.New("sim puk: expected PUK and new PIN")
		}
		if err := st.SendPUK(args[1], args[2]); err != nil {
			return err
		}
		syncAfterSIMChange()
		return done("SIM unblocked, new PIN set")
	case "pin-lock":
		if len(args) != 3 || (args[1] != "on" && args[1] != "off") {
			return errors.New("sim pin-lock: expected on|off and the PIN")
		}
		if err := st.SetPINLock(args[2], args[1] == "on"); err != nil {
			return err
		}
		return done("PIN request at power-on " + args[1])
	case "change-pin":
		if len(args) != 3 {
			return errors.New("sim change-pin: expected old and new PIN")
		}
		if err := st.ChangePIN(args[1], args[2]); err != nil {
			return err
		}
		return done("PIN changed")
	}
	return fmt.Errorf("sim: unknown subcommand %q (slot, pin, puk, pin-lock, change-pin)", args[0])
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
	case "on", "connect", "up":
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
