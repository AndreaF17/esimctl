// Package at is a minimal client for the modem's AT command port. It carries
// eSIM APDUs over the 3GPP TS 27.007 logical channel commands (+CCHO, +CGLA,
// +CCHC) and Quectel GNSS control, so esimctl can work while ModemManager
// owns the MBIM port.
package at

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Port is an open AT command port.
type Port struct {
	f       *os.File
	buf     []byte
	Timeout time.Duration
	Debug   func(dir, line string)
}

// FindPort returns the first WWAN AT port.
func FindPort() (string, error) {
	m, _ := filepath.Glob("/dev/wwan*at*")
	if len(m) == 0 {
		return "", errors.New("no AT port found (/dev/wwan*at*)")
	}
	return m[0], nil
}

// Open opens the AT port. The kernel lets several processes open a WWAN AT
// port at once, and they would then steal each other's responses, so access
// is serialized with an exclusive flock that every esimctl instance takes.
func Open(path string) (*Port, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, syscall.EBUSY) {
			return nil, errors.New("AT port busy (is ModemManager using it? see contrib/70-esimctl.rules)")
		}
		return nil, err
	}
	if err := Lock(f, 30*time.Second); err != nil {
		f.Close()
		return nil, err
	}
	p := &Port{f: f, Timeout: 10 * time.Second}
	p.drain()
	// A short probe tells a wedged port apart from a slow command later on.
	p.Timeout = 3 * time.Second
	if _, err := p.Command("ATE0"); err != nil {
		var ae *Error
		if !errors.As(err, &ae) {
			f.Close()
			return nil, errors.New("modem AT port not responding; restart the modem with: esimctl modem off && esimctl modem on")
		}
	}
	p.Timeout = 10 * time.Second
	return p, nil
}

func (p *Port) Close() error { return p.f.Close() }

// Lock takes an exclusive advisory lock on f, waiting up to wait. It goes
// through SyscallConn: f.Fd() would switch the file to blocking mode and
// silently disable read deadlines.
func Lock(f *os.File, wait time.Duration) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		var err error
		if cerr := rc.Control(func(fd uintptr) {
			err = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
		}); cerr != nil {
			return cerr
		}
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("modem port in use by another esimctl")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Error is a final ERROR / +CME ERROR response.
type Error struct{ Msg string }

func (e *Error) Error() string { return "modem: " + e.Msg }

// Command sends cmd and returns the information lines of the response
// (without the final OK), skipping unsolicited result codes it doesn't ask for.
func (p *Port) Command(cmd string) ([]string, error) {
	if p.Debug != nil {
		p.Debug(">>", cmd)
	}
	if _, err := p.f.Write([]byte(cmd + "\r")); err != nil {
		return nil, err
	}
	var lines []string
	deadline := time.Now().Add(p.Timeout)
	for {
		line, err := p.readLine(deadline)
		if err != nil {
			return lines, err
		}
		if p.Debug != nil {
			p.Debug("<<", line)
		}
		switch {
		case line == "" || line == cmd:
			continue
		case line == "OK":
			return lines, nil
		case line == "ERROR", strings.HasPrefix(line, "+CME ERROR"), strings.HasPrefix(line, "+CMS ERROR"):
			return lines, &Error{Msg: line}
		}
		lines = append(lines, line)
	}
}

// Query runs cmd and returns the payload of the first line starting with prefix.
func (p *Port) Query(cmd, prefix string) (string, error) {
	lines, err := p.Command(cmd)
	if err != nil {
		return "", err
	}
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix)), nil
		}
	}
	return "", fmt.Errorf("modem: no %s in response to %s", prefix, cmd)
}

// drain discards responses left in the port by a client that was killed
// mid-command; they would otherwise be taken as replies to our commands.
func (p *Port) drain() {
	buf := make([]byte, 4096)
	for {
		_ = p.f.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		if n, err := p.f.Read(buf); err != nil || n == 0 {
			break
		}
	}
	p.buf = nil
}

func (p *Port) readLine(deadline time.Time) (string, error) {
	tmp := make([]byte, 4096)
	for {
		if i := bytes.IndexAny(p.buf, "\r\n"); i >= 0 {
			line := string(p.buf[:i])
			p.buf = p.buf[i+1:]
			return strings.TrimSpace(line), nil
		}
		_ = p.f.SetReadDeadline(deadline)
		n, err := p.f.Read(tmp)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return "", errors.New("modem: timeout waiting for AT response")
			}
			return "", err
		}
		p.buf = append(p.buf, tmp[:n]...)
	}
}

// UICC exposes logical-channel APDU access over AT+CCHO/+CGLA/+CCHC.
type UICC struct{ p *Port }

func NewUICC(p *Port) *UICC { return &UICC{p: p} }

// OpenChannel opens a logical channel with aid selected.
func (u *UICC) OpenChannel(aid []byte) (uint32, error) {
	v, err := u.p.Query(fmt.Sprintf(`AT+CCHO="%X"`, aid), "+CCHO:")
	if err != nil {
		// Some firmwares answer a bare number instead of "+CCHO: n".
		lines, err2 := u.p.Command(fmt.Sprintf(`AT+CCHO="%X"`, aid))
		if err2 != nil || len(lines) == 0 {
			return 0, err
		}
		v = lines[0]
	}
	ch, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("modem: bad +CCHO response %q", v)
	}
	return uint32(ch), nil
}

// CloseChannel closes a logical channel.
func (u *UICC) CloseChannel(ch uint32) error {
	_, err := u.p.Command(fmt.Sprintf("AT+CCHC=%d", ch))
	return err
}

// Transmit sends an APDU on channel ch with AT+CGLA.
func (u *UICC) Transmit(ch uint32, apdu []byte) ([]byte, uint16, error) {
	cmd := fmt.Sprintf(`AT+CGLA=%d,%d,"%X"`, ch, 2*len(apdu), apdu)
	v, err := u.p.Query(cmd, "+CGLA:")
	if err != nil {
		return nil, 0, err
	}
	_, data, ok := strings.Cut(v, ",")
	if !ok {
		return nil, 0, fmt.Errorf("modem: bad +CGLA response %q", v)
	}
	raw, err := hex.DecodeString(strings.Trim(strings.TrimSpace(data), `"`))
	if err != nil || len(raw) < 2 {
		return nil, 0, fmt.Errorf("modem: bad +CGLA data %q", data)
	}
	n := len(raw)
	return raw[:n-2], uint16(raw[n-2])<<8 | uint16(raw[n-1]), nil
}
