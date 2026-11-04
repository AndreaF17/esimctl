// Package mbim implements the subset of the MBIM control protocol needed to
// exchange APDUs with the UICC through the Microsoft "UICC Low Level Access"
// device service, talking directly to a /dev/wwan*mbim* character device.
package mbim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	msgOpen          = 0x00000001
	msgClose         = 0x00000002
	msgCommand       = 0x00000003
	msgOpenDone      = 0x80000001
	msgCloseDone     = 0x80000002
	msgCommandDone   = 0x80000003
	msgFunctionError = 0x80000004
	msgIndicate      = 0x80000007

	cmdQuery = 0
	cmdSet   = 1

	maxControlTransfer = 4096
)

// UUIDs are transmitted on the wire in the same byte order they are written.
var (
	ServiceBasicConnect = mustUUID("a289cc33-bcbb-8b4f-b6b0-133ec2aae6df")
	ServiceUICCLowLevel = mustUUID("c2f6588e-f037-4bc9-8665-f4d44bd09367")
)

// CIDs of the MS UICC Low Level Access service.
const (
	CIDUICCATR          = 1
	CIDUICCOpenChannel  = 2
	CIDUICCCloseChannel = 3
	CIDUICCAPDU         = 4
	CIDUICCReset        = 6
)

// CIDs of the basic connect service.
const (
	CIDDeviceCaps     = 1
	CIDSubscriberRdy  = 2
	CIDDeviceServices = 9
)

// Status is an MBIM status code returned in COMMAND_DONE.
type Status uint32

var statusNames = map[Status]string{
	0: "SUCCESS", 1: "BUSY", 2: "FAILURE", 3: "SIM_NOT_INSERTED", 4: "BAD_SIM",
	5: "PIN_REQUIRED", 9: "NO_DEVICE_SUPPORT", 14: "NOT_INITIALIZED",
	20: "RADIO_POWER_OFF", 21: "INVALID_PARAMETERS", 22: "READ_FAILURE",
	23: "WRITE_FAILURE", 0x87430001: "MS_NO_LOGICAL_CHANNELS",
	0x87430002: "MS_SELECT_FAILED", 0x87430003: "MS_INVALID_LOGICAL_CHANNEL",
}

func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return fmt.Sprintf("0x%08x", uint32(s))
}

// Status codes callers need to branch on.
const (
	StatusInvalidParameters Status = 21
	StatusNoLogicalChannels Status = 0x87430001
	StatusSelectFailed      Status = 0x87430002
)

// StatusError is returned when the device completes a command with a non-success status.
type StatusError struct {
	CID    uint32
	Status Status
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("mbim: command %d failed: %s", e.CID, e.Status)
}

// Device is an open MBIM control channel.
type Device struct {
	f       *os.File
	mu      sync.Mutex
	tid     uint32
	rbuf    []byte
	Timeout time.Duration
	Debug   io.Writer
}

// Open opens the MBIM control device and performs the MBIM OPEN handshake.
func Open(path string) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{f: f, Timeout: 30 * time.Second}
	if err := d.open(); err != nil {
		// A previous session may have left the function opened: reset it.
		_ = d.simple(msgClose, nil, msgCloseDone)
		if err2 := d.open(); err2 != nil {
			f.Close()
			return nil, fmt.Errorf("mbim open: %w", err)
		}
	}
	return d, nil
}

func (d *Device) open() error {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, maxControlTransfer)
	return d.simple(msgOpen, payload, msgOpenDone)
}

// Close sends MBIM CLOSE and closes the file.
func (d *Device) Close() error {
	_ = d.simple(msgClose, nil, msgCloseDone)
	return d.f.Close()
}

func (d *Device) nextTID() uint32 {
	d.tid++
	return d.tid
}

func (d *Device) simple(typ uint32, payload []byte, want uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tid := d.nextTID()
	msg := header(typ, 12+len(payload), tid)
	msg = append(msg, payload...)
	if err := d.write(msg); err != nil {
		return err
	}
	for {
		m, err := d.readMsg()
		if err != nil {
			return err
		}
		mt := binary.LittleEndian.Uint32(m[0:])
		if mt == msgFunctionError {
			return fmt.Errorf("mbim: function error 0x%x", binary.LittleEndian.Uint32(m[12:]))
		}
		if mt != want || binary.LittleEndian.Uint32(m[8:]) != tid {
			continue
		}
		if st := Status(binary.LittleEndian.Uint32(m[12:])); st != 0 {
			return &StatusError{Status: st}
		}
		return nil
	}
}

// Command sends a COMMAND message and returns the information buffer of the
// matching COMMAND_DONE.
func (d *Device) Command(service [16]byte, cid uint32, set bool, info []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tid := d.nextTID()
	ctype := uint32(cmdQuery)
	if set {
		ctype = cmdSet
	}
	body := make([]byte, 0, 48+len(info))
	body = le32(body, 1) // total fragments
	body = le32(body, 0) // current fragment
	body = append(body, service[:]...)
	body = le32(body, cid)
	body = le32(body, ctype)
	body = le32(body, uint32(len(info)))
	body = append(body, info...)
	if 12+len(body) > maxControlTransfer {
		return nil, errors.New("mbim: command too large")
	}
	msg := append(header(msgCommand, 12+len(body), tid), body...)
	if err := d.write(msg); err != nil {
		return nil, err
	}

	var assembled []byte
	var status Status
	for {
		m, err := d.readMsg()
		if err != nil {
			return nil, err
		}
		mt := binary.LittleEndian.Uint32(m[0:])
		if mt == msgFunctionError && binary.LittleEndian.Uint32(m[8:]) == tid {
			return nil, fmt.Errorf("mbim: function error 0x%x", binary.LittleEndian.Uint32(m[12:]))
		}
		if mt != msgCommandDone || binary.LittleEndian.Uint32(m[8:]) != tid {
			continue // indications or stale replies
		}
		total := binary.LittleEndian.Uint32(m[12:])
		cur := binary.LittleEndian.Uint32(m[16:])
		if cur == 0 {
			if len(m) < 48 {
				return nil, errors.New("mbim: short COMMAND_DONE")
			}
			status = Status(binary.LittleEndian.Uint32(m[40:]))
			n := int(binary.LittleEndian.Uint32(m[44:]))
			if 48+n > len(m) {
				n = len(m) - 48
			}
			assembled = append(assembled, m[48:48+n]...)
		} else {
			assembled = append(assembled, m[20:]...)
		}
		if cur+1 >= total {
			break
		}
	}
	if status != 0 {
		return assembled, &StatusError{CID: cid, Status: status}
	}
	return assembled, nil
}

func (d *Device) write(msg []byte) error {
	if d.Debug != nil {
		fmt.Fprintf(d.Debug, "mbim >> % x\n", msg)
	}
	_, err := d.f.Write(msg)
	return err
}

func (d *Device) readMsg() ([]byte, error) {
	if d.Timeout > 0 {
		_ = d.f.SetReadDeadline(time.Now().Add(d.Timeout))
	}
	buf := make([]byte, maxControlTransfer)
	for {
		if len(d.rbuf) >= 12 {
			l := int(binary.LittleEndian.Uint32(d.rbuf[4:]))
			if l < 12 {
				d.rbuf = nil
				return nil, errors.New("mbim: corrupt message length")
			}
			if len(d.rbuf) >= l {
				m := bytes.Clone(d.rbuf[:l])
				d.rbuf = d.rbuf[l:]
				if d.Debug != nil {
					fmt.Fprintf(d.Debug, "mbim << % x\n", m)
				}
				if binary.LittleEndian.Uint32(m[0:]) == msgIndicate {
					continue
				}
				return m, nil
			}
		}
		n, err := d.f.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return nil, errors.New("mbim: timeout waiting for modem")
			}
			return nil, err
		}
		d.rbuf = append(d.rbuf, buf[:n]...)
	}
}

func header(typ uint32, length int, tid uint32) []byte {
	b := make([]byte, 0, length)
	b = le32(b, typ)
	b = le32(b, uint32(length))
	b = le32(b, tid)
	return b
}

func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func mustUUID(s string) [16]byte {
	var u [16]byte
	j := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		var v byte
		fmt.Sscanf(s[i:i+2], "%02x", &v)
		u[j] = v
		j++
		i++
	}
	return u
}
