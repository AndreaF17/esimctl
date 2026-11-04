// Package lpa ties the MBIM transport, the eUICC commands and the ES9+ client
// together into the Local Profile Assistant operations exposed to users.
package lpa

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndreaF17/esimctl/internal/at"
	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/mbim"
)

// Session is an open ISD-R channel on the modem's eUICC.
type Session struct {
	Card      *euicc.Card
	Transport string // "mbim" or "at"
	close     func() error
}

// FindDevice returns the first MBIM control port present on the system.
func FindDevice() (string, error) {
	for _, pat := range []string{"/dev/wwan*mbim*", "/dev/cdc-wdm*"} {
		m, _ := filepath.Glob(pat)
		if len(m) > 0 {
			return m[0], nil
		}
	}
	return "", errors.New("no MBIM modem found (/dev/wwan*mbim*)")
}

// ModemManagerRunning reports whether ModemManager is running, in which case
// it owns the MBIM port and the eUICC must be reached through the AT port.
func ModemManagerRunning() bool {
	procs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, p := range procs {
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == "ModemManager" {
			return true
		}
	}
	return false
}

// Open connects to the modem and opens a logical channel to the ISD-R.
// transport is "mbim", "at" or "" (auto: AT when ModemManager is running).
func Open(transport, path string, debug io.Writer) (*Session, error) {
	if transport == "" {
		transport = "mbim"
		if ModemManagerRunning() {
			transport = "at"
		}
	}
	switch transport {
	case "mbim":
		return openMBIM(path, debug)
	case "at":
		return openAT(path, debug)
	}
	return nil, fmt.Errorf("unknown transport %q (mbim or at)", transport)
}

func permissionHint(err error, path string) error {
	if errors.Is(err, os.ErrPermission) {
		return errors.New("permission denied on " + path + ": install contrib/70-esimctl.rules (see README) or run as root")
	}
	return err
}

func openMBIM(path string, debug io.Writer) (*Session, error) {
	if path == "" {
		var err error
		if path, err = FindDevice(); err != nil {
			return nil, err
		}
	}
	dev, err := mbim.Open(path)
	if err != nil {
		return nil, permissionHint(err, path)
	}
	dev.Debug = debug
	u := mbim.NewUICC(dev)
	ch, _, err := u.OpenChannel(euicc.ISDR)
	var se *mbim.StatusError
	if errors.As(err, &se) && se.Status == mbim.StatusSelectFailed {
		// The ISD-R is held by a channel leaked by an interrupted client.
		if debug != nil {
			fmt.Fprintln(debug, "ISD-R busy, resetting UICC")
		}
		if err = u.Reset(); err == nil {
			time.Sleep(3 * time.Second)
			ch, _, err = u.OpenChannel(euicc.ISDR)
		}
	}
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &Session{Card: euicc.New(u, ch), Transport: "mbim", close: func() error {
		_ = u.CloseChannel(ch)
		return dev.Close()
	}}, nil
}

func openAT(path string, debug io.Writer) (*Session, error) {
	if path == "" || strings.Contains(path, "mbim") {
		var err error
		if path, err = at.FindPort(); err != nil {
			return nil, err
		}
	}
	p, err := at.Open(path)
	if err != nil {
		return nil, permissionHint(err, path)
	}
	if debug != nil {
		p.Debug = func(dir, line string) { fmt.Fprintf(debug, "at %s %s\n", dir, line) }
	}
	u := at.NewUICC(p)
	ch, err := u.OpenChannel(euicc.ISDR)
	if err != nil {
		// The ISD-R may be held by a channel an interrupted esimctl left open.
		// Unlike MBIM, the AT port can close it.
		if debug != nil {
			fmt.Fprintln(debug, "ISD-R busy, closing stale logical channels")
		}
		for c := uint32(1); c <= 3; c++ {
			_ = u.CloseChannel(c)
		}
		ch, err = u.OpenChannel(euicc.ISDR)
	}
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("opening ISD-R channel: %w", err)
	}
	return &Session{Card: euicc.New(u, ch), Transport: "at", close: func() error {
		_ = u.CloseChannel(ch)
		return p.Close()
	}}, nil
}

// Close releases the logical channel and the modem port.
func (s *Session) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}
