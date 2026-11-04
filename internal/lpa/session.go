// Package lpa ties the MBIM transport, the eUICC commands and the ES9+ client
// together into the Local Profile Assistant operations exposed to users.
package lpa

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/mbim"
)

// Session is an open ISD-R channel on the modem's eUICC.
type Session struct {
	Card *euicc.Card
	dev  *mbim.Device
	uicc *mbim.UICC
	ch   uint32
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

// Open connects to the modem and opens a logical channel to the ISD-R.
func Open(path string, debug io.Writer) (*Session, error) {
	if path == "" {
		var err error
		if path, err = FindDevice(); err != nil {
			return nil, err
		}
	}
	dev, err := mbim.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, errors.New("permission denied on " + path + ": install the udev rule (see README) or run as root")
		}
		return nil, err
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
	return &Session{Card: euicc.New(u, ch), dev: dev, uicc: u, ch: ch}, nil
}

// Close releases the logical channel and the MBIM function.
func (s *Session) Close() error {
	_ = s.uicc.CloseChannel(s.ch)
	return s.dev.Close()
}
