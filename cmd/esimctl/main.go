// Command esimctl manages the eSIM (eUICC) of an MBIM cellular modem.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/lpa"
	"github.com/AndreaF17/esimctl/internal/qr"
)

type globals struct {
	device    string
	transport string
	json      bool
	debug     bool
	yes       bool
	noNotify  bool
	quiet     bool // suppress warnings on stderr (TUI)
}

var g globals

const usage = `esimctl - eSIM, SIM, mobile data and GPS manager for cellular modems

Usage: esimctl [flags] <command> [args]

Commands:
  info                        chip information (EID, firmware, free memory)
  list                        installed profiles
  enable <iccid>              enable a profile (disables the current one)
  disable <iccid>             disable a profile
  delete <iccid>              delete a disabled profile
  nickname <iccid> [name]     set or clear a profile nickname
  download <activation code>  download a profile ("LPA:1$smdp$id")
  notifications [list]        pending notifications
  notifications process       send pending notifications to the SM-DP+
  notifications remove <seq>  drop a notification without sending it
  default-smdp [address]      show or set the default SM-DP+ address
  status                      EID, profiles and notifications in one read
  scan [--paste]              read an activation code from a QR on screen
                              (or from the clipboard) and print it
  modem [status|on|off|signal]
                              modem state, radio power, signal details
  sim slot <n>                switch SIM slot (physical / eSIM)
  sim pin <pin>               unlock the SIM (also: puk, pin-lock, change-pin)
  data [status|on|off]        mobile data connection (NetworkManager)
  data apn auto|<apn> [user] [password]
                              APN selection (auto: from the provider database)
  data apns [mccmnc]          known APNs for the active SIM's network
  data roaming|autoconnect on|off
  gps [status|on|off]         GNSS receiver: position, satellites, power
  tui [--scan|--paste|--code <c>]
                              interactive terminal interface, optionally
                              starting a download right away

Flags (accepted before or after the command):
  --device <path>  modem port (default: auto-detect)
  --transport <t>  eSIM transport: mbim or at (default: at when ModemManager
                   is running, mbim otherwise)
  --json           machine-readable output
  -y, --yes        don't ask for confirmation
  --cc <code>      confirmation code for download
  --no-notify      don't send notifications to the SM-DP+ after an operation
  --debug          dump MBIM traffic to stderr
`

func addCommon(fs *flag.FlagSet) {
	fs.StringVar(&g.device, "device", g.device, "")
	fs.StringVar(&g.transport, "transport", g.transport, "")
	fs.BoolVar(&g.json, "json", g.json, "")
	fs.BoolVar(&g.debug, "debug", g.debug, "")
	fs.BoolVar(&g.yes, "y", g.yes, "")
	fs.BoolVar(&g.yes, "yes", g.yes, "")
	fs.BoolVar(&g.noNotify, "no-notify", g.noNotify, "")
}

func main() {
	root := flag.NewFlagSet("esimctl", flag.ContinueOnError)
	root.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	addCommon(root)
	if err := root.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	args := root.Args()
	if len(args) == 0 {
		root.Usage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = root.Usage
	addCommon(fs)
	cc := fs.String("cc", "", "")
	scan := fs.Bool("scan", false, "")
	paste := fs.Bool("paste", false, "")
	code := fs.String("code", "", "")
	if err := fs.Parse(interleave(rest)); err != nil {
		os.Exit(2)
	}
	rest = fs.Args()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch cmd {
	case "info":
		err = withSession(cmdInfo)
	case "status":
		err = withSession(cmdStatus)
	case "list", "ls", "profiles":
		err = withSession(cmdList)
	case "enable", "disable":
		if len(rest) != 1 {
			err = errors.New(cmd + ": expected an ICCID")
			break
		}
		err = cmdSwitch(ctx, cmd, rest[0])
	case "delete", "rm":
		if len(rest) != 1 {
			err = errors.New("delete: expected an ICCID")
			break
		}
		err = withSession(func(s *lpa.Session) error { return cmdDelete(ctx, s, rest[0]) })
	case "nickname", "rename":
		if len(rest) < 1 {
			err = errors.New("nickname: expected an ICCID")
			break
		}
		err = withSession(func(s *lpa.Session) error {
			if err := s.Card.SetNickname(rest[0], strings.Join(rest[1:], " ")); err != nil {
				return err
			}
			return done("nickname updated")
		})
	case "download", "add":
		if len(rest) != 1 {
			err = errors.New("download: expected an activation code")
			break
		}
		err = withSession(func(s *lpa.Session) error { return cmdDownload(ctx, s, rest[0], *cc) })
	case "notifications", "notif":
		err = withSession(func(s *lpa.Session) error { return cmdNotifications(ctx, s, rest) })
	case "default-smdp":
		err = withSession(func(s *lpa.Session) error {
			if len(rest) == 0 {
				a, err := s.Card.Addresses()
				if err != nil {
					return err
				}
				return output(a, func() {
					fmt.Printf("default SM-DP+: %s\nroot SM-DS:     %s\n", orDash(a.DefaultSMDP), orDash(a.RootSMDS))
				})
			}
			if err := s.Card.SetDefaultSMDP(rest[0]); err != nil {
				return err
			}
			return done("default SM-DP+ updated")
		})
	case "gps":
		err = cmdGPS(ctx, rest)
	case "modem":
		err = cmdModem(rest)
	case "sim":
		err = cmdSIM(rest)
	case "data":
		err = cmdData(rest)
	case "tui":
		err = runTUI(ctx, *scan, *paste, *code)
	case "scan":
		var c string
		if *paste {
			c, err = qr.FromClipboard()
		} else {
			c, err = qr.FromScreen()
		}
		if err == nil {
			err = output(map[string]string{"code": c}, func() { fmt.Println(c) })
		}
	case "help", "-h", "--help":
		root.Usage()
	default:
		err = fmt.Errorf("unknown command %q (see esimctl help)", cmd)
	}
	if err != nil {
		if g.json {
			json.NewEncoder(os.Stdout).Encode(map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintln(os.Stderr, "esimctl:", err)
		}
		os.Exit(1)
	}
}

// interleave moves flags after positional arguments to the front, so that
// "esimctl enable 8939... --json" works like "esimctl enable --json 8939...".
func interleave(args []string) []string {
	var flags, pos []string
	valued := map[string]bool{"-cc": true, "--cc": true, "-device": true, "--device": true, "-code": true, "--code": true, "-transport": true, "--transport": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			if valued[a] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func openSession() (*lpa.Session, error) {
	var dbg io.Writer
	if g.debug {
		dbg = os.Stderr
	}
	return lpa.Open(g.transport, g.device, dbg)
}

func withSession(f func(*lpa.Session) error) error {
	s, err := openSession()
	if err != nil {
		return err
	}
	defer s.Close()
	return f(s)
}

func output(v any, human func()) error {
	if g.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human()
	return nil
}

func done(msg string) error {
	return output(map[string]any{"ok": true, "message": msg}, func() { fmt.Println(msg) })
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func confirm(prompt string) bool {
	if g.yes {
		return true
	}
	if g.json {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes" || line == "s" || line == "si" || line == "sì"
}

type chipInfo struct {
	EID       string          `json:"eid"`
	Info      euicc.Info      `json:"info"`
	Addresses euicc.Addresses `json:"addresses"`
}

func cmdInfo(s *lpa.Session) error {
	var ci chipInfo
	var err error
	if ci.EID, err = s.Card.EID(); err != nil {
		return err
	}
	if t, err := s.Card.EUICCInfo2(); err == nil {
		ci.Info = euicc.ParseInfo2(t)
	}
	ci.Addresses, _ = s.Card.Addresses()
	return output(ci, func() {
		fmt.Printf("EID:             %s\n", ci.EID)
		fmt.Printf("Firmware:        %s\n", orDash(ci.Info.FirmwareVersion))
		fmt.Printf("SGP.22 version:  %s\n", orDash(ci.Info.SVN))
		fmt.Printf("Free memory:     %d KiB\n", ci.Info.FreeNVM/1024)
		fmt.Printf("Default SM-DP+:  %s\n", orDash(ci.Addresses.DefaultSMDP))
		fmt.Printf("Root SM-DS:      %s\n", orDash(ci.Addresses.RootSMDS))
		if ci.Info.SASAccreditation != "" {
			fmt.Printf("SAS:             %s\n", ci.Info.SASAccreditation)
		}
	})
}

type status struct {
	EID           string               `json:"eid"`
	FreeNVM       int                  `json:"free_nvm"`
	Profiles      []euicc.Profile      `json:"profiles"`
	Notifications []euicc.Notification `json:"notifications"`
}

func cmdStatus(s *lpa.Session) error {
	var st status
	var err error
	if st.EID, err = s.Card.EID(); err != nil {
		return err
	}
	if t, err := s.Card.EUICCInfo2(); err == nil {
		st.FreeNVM = euicc.ParseInfo2(t).FreeNVM
	}
	if st.Profiles, err = s.Card.Profiles(); err != nil {
		return err
	}
	st.Notifications, _ = s.Card.Notifications()
	if st.Profiles == nil {
		st.Profiles = []euicc.Profile{}
	}
	if st.Notifications == nil {
		st.Notifications = []euicc.Notification{}
	}
	for i := range st.Profiles {
		st.Profiles[i].Icon = nil // keep the widget payload small
	}
	return output(st, func() {
		fmt.Printf("EID %s, %d profile(s), %d notification(s)\n", st.EID, len(st.Profiles), len(st.Notifications))
	})
}

func cmdList(s *lpa.Session) error {
	ps, err := s.Card.Profiles()
	if err != nil {
		return err
	}
	if ps == nil {
		ps = []euicc.Profile{}
	}
	return output(ps, func() {
		if len(ps) == 0 {
			fmt.Println("no profiles installed")
			return
		}
		for _, p := range ps {
			state := "  "
			if p.Enabled {
				state = "● "
			}
			name := p.Nickname
			if name == "" {
				name = p.Name
			}
			fmt.Printf("%s%-22s %-24s %s", state, p.ICCID, name, p.Provider)
			if p.Class != "operational" {
				fmt.Printf(" [%s]", p.Class)
			}
			fmt.Println()
		}
	})
}

func cmdSwitch(ctx context.Context, op, iccid string) error {
	s, err := openSession()
	if err != nil {
		return err
	}
	if op == "enable" {
		err = s.Card.Enable(iccid, true)
	} else {
		err = s.Card.Disable(iccid, true)
	}
	s.Close()
	if err != nil {
		return err
	}
	if !g.noNotify {
		// The REFRESH resets the card: wait for it to come back before
		// delivering the notification the operation generated.
		notifyAfterReset(ctx)
	}
	return done("profile " + op + "d")
}

func notifyAfterReset(ctx context.Context) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		s, err := openSession()
		if err != nil {
			continue
		}
		_, errs := s.ProcessNotifications(ctx, true)
		s.Close()
		for _, e := range errs {
			if !g.json && !g.quiet {
				fmt.Fprintln(os.Stderr, "warning:", e)
			}
		}
		return
	}
	if !g.json && !g.quiet {
		fmt.Fprintln(os.Stderr, "warning: eUICC did not come back in time; run 'esimctl notifications process' later")
	}
}

func cmdDelete(ctx context.Context, s *lpa.Session, iccid string) error {
	if !confirm("Delete profile " + iccid + "? This cannot be undone") {
		return errors.New("aborted")
	}
	if err := s.Card.Delete(iccid); err != nil {
		return err
	}
	if !g.noNotify {
		if _, errs := s.ProcessNotifications(ctx, true); len(errs) > 0 && !g.json {
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, "warning:", e)
			}
		}
	}
	return done("profile deleted")
}

func cmdDownload(ctx context.Context, s *lpa.Session, code, cc string) error {
	ac, err := lpa.ParseActivationCode(code)
	if err != nil {
		return err
	}
	opt := lpa.DownloadOptions{
		ConfirmationCode: cc,
		Progress: func(stage string, d, t int) {
			if g.json {
				return
			}
			if t > 0 {
				fmt.Fprintf(os.Stderr, "\r  %s %d/%d", stage, d, t)
				if d == t {
					fmt.Fprintln(os.Stderr)
				}
				return
			}
			fmt.Fprintf(os.Stderr, "  %s…\n", stage)
		},
		Confirm: func(m lpa.ProfileMetadata, ccRequired bool) (bool, string) {
			if !g.json {
				fmt.Fprintf(os.Stderr, "\nProfile:  %s\nProvider: %s\nICCID:    %s\n\n", orDash(m.Name), orDash(m.Provider), orDash(m.ICCID))
			}
			if !confirm("Install this profile?") {
				return false, ""
			}
			if ccRequired && cc == "" && !g.json {
				fmt.Fprint(os.Stderr, "Confirmation code: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				return true, strings.TrimSpace(line)
			}
			return true, ""
		},
	}
	res, err := s.Download(ctx, ac, opt)
	if err != nil {
		return err
	}
	return output(res, func() {
		fmt.Printf("installed %s (%s)\n", orDash(res.Profile.Name), res.Profile.ICCID)
		fmt.Println("the profile is disabled: enable it with 'esimctl enable " + res.Profile.ICCID + "'")
	})
}

func cmdNotifications(ctx context.Context, s *lpa.Session, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		ns, err := s.Card.Notifications()
		if err != nil {
			return err
		}
		if ns == nil {
			ns = []euicc.Notification{}
		}
		return output(ns, func() {
			if len(ns) == 0 {
				fmt.Println("no pending notifications")
			}
			for _, n := range ns {
				fmt.Printf("%4d  %-8s %-22s %s\n", n.Seq, n.Operation, orDash(n.ICCID), n.Address)
			}
		})
	case "process", "send":
		n, errs := s.ProcessNotifications(ctx, true)
		for _, e := range errs {
			if !g.json {
				fmt.Fprintln(os.Stderr, "warning:", e)
			}
		}
		return output(map[string]any{"ok": len(errs) == 0, "sent": n, "failed": len(errs)},
			func() { fmt.Printf("%d notification(s) sent\n", n) })
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("notifications remove: expected a sequence number")
		}
		seq, err := strconv.Atoi(args[1])
		if err != nil {
			return err
		}
		if err := s.Card.RemoveNotification(seq); err != nil {
			return err
		}
		return done("notification removed")
	}
	return fmt.Errorf("notifications: unknown subcommand %q", sub)
}
