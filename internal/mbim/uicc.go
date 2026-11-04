package mbim

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// channelGroup tags every logical channel this tool opens.
const channelGroup = 1

// UICC exposes logical-channel APDU access over MBIM.
type UICC struct {
	dev *Device
	// swapped records whether the device wants (size, offset) pairs, as the
	// Microsoft specification lays them out, instead of (offset, size).
	swapped bool
}

func NewUICC(dev *Device) *UICC { return &UICC{dev: dev, swapped: true} }

// ATR returns the card's answer-to-reset.
func (u *UICC) ATR() ([]byte, error) {
	info, err := u.dev.Command(ServiceUICCLowLevel, CIDUICCATR, false, nil)
	if err != nil {
		return nil, err
	}
	return refBytes(info, 0, 8)
}

// Reset resets the UICC. It is the only way to recover an ISD-R channel left
// open by a client that died, since the modem refuses to close channels it
// no longer tracks.
func (u *UICC) Reset() error {
	_, err := u.dev.Command(ServiceUICCLowLevel, CIDUICCReset, true, le32(nil, 0))
	return err
}

// OpenChannel opens a logical channel and selects the application aid on it.
func (u *UICC) OpenChannel(aid []byte) (uint32, []byte, error) {
	build := func(swapped bool) []byte {
		b := make([]byte, 0, 16+len(aid))
		b = u.pair(b, swapped, uint32(len(aid)), 16)
		b = le32(b, 0) // SelectP2Arg
		b = le32(b, channelGroup)
		return pad4(append(b, aid...))
	}
	info, err := u.dev.Command(ServiceUICCLowLevel, CIDUICCOpenChannel, true, build(u.swapped))
	var se *StatusError
	if errors.As(err, &se) && se.Status == StatusInvalidParameters { // try the other layout
		u.swapped = !u.swapped
		info, err = u.dev.Command(ServiceUICCLowLevel, CIDUICCOpenChannel, true, build(u.swapped))
	}
	if errors.As(err, &se) && se.Status == StatusNoLogicalChannels { // reclaim leaked channels
		for ch := uint32(1); ch < 20; ch++ {
			_ = u.CloseChannel(ch)
		}
		info, err = u.dev.Command(ServiceUICCLowLevel, CIDUICCOpenChannel, true, build(u.swapped))
	}
	if err != nil {
		return 0, nil, err
	}
	if len(info) < 8 {
		return 0, nil, errors.New("mbim: short open channel response")
	}
	sw := statusWord(info)
	ch := binary.LittleEndian.Uint32(info[4:])
	resp, _ := refBytes(info, 8, 16)
	if sw != 0x9000 && sw>>8 != 0x61 {
		_ = u.CloseChannel(ch)
		return 0, resp, fmt.Errorf("uicc: select failed, SW=%04X", sw)
	}
	return ch, resp, nil
}

// CloseChannel closes a logical channel.
func (u *UICC) CloseChannel(ch uint32) error {
	b := le32(nil, ch)
	b = le32(b, channelGroup)
	info, err := u.dev.Command(ServiceUICCLowLevel, CIDUICCCloseChannel, true, b)
	if err != nil {
		return err
	}
	if len(info) >= 4 {
		if sw := statusWord(info); sw != 0x9000 && sw != 0 {
			return fmt.Errorf("uicc: close channel SW=%04X", sw)
		}
	}
	return nil
}

// Transmit sends a raw command APDU on channel ch and returns response data
// and the status word.
func (u *UICC) Transmit(ch uint32, apdu []byte) ([]byte, uint16, error) {
	b := make([]byte, 0, 20+len(apdu))
	b = le32(b, ch)
	b = le32(b, 0) // no secure messaging
	b = le32(b, 0) // inter-industry class byte
	b = u.pair(b, u.swapped, uint32(len(apdu)), 20)
	b = pad4(append(b, apdu...))
	info, err := u.dev.Command(ServiceUICCLowLevel, CIDUICCAPDU, true, b)
	if err != nil {
		return nil, 0, err
	}
	if len(info) < 12 {
		return nil, 0, errors.New("mbim: short APDU response")
	}
	sw := statusWord(info)
	resp, err := refBytes(info, 4, 12)
	return resp, sw, err
}

// statusWord decodes the Status field, which carries SW1 and SW2 as the
// first two bytes rather than as a little-endian integer.
func statusWord(info []byte) uint16 { return uint16(info[0])<<8 | uint16(info[1]) }

func (u *UICC) pair(b []byte, swapped bool, size, offset uint32) []byte {
	if swapped {
		return le32(le32(b, size), offset)
	}
	return le32(le32(b, offset), size)
}

// refBytes decodes an (offset,size) or (size,offset) pair located at pos and
// returns the referenced bytes. dataStart is where the variable buffer begins,
// which disambiguates the two layouts.
func refBytes(info []byte, pos, dataStart int) ([]byte, error) {
	if len(info) < pos+8 {
		return nil, errors.New("mbim: short buffer")
	}
	a := int(binary.LittleEndian.Uint32(info[pos:]))
	b := int(binary.LittleEndian.Uint32(info[pos+4:]))
	try := func(off, size int) ([]byte, bool) {
		if size == 0 {
			return nil, true
		}
		if off < dataStart || off+size > len(info) {
			return nil, false
		}
		return info[off : off+size], true
	}
	// Prefer the interpretation whose offset points at the data area start.
	if b == dataStart {
		if r, ok := try(b, a); ok {
			return r, nil
		}
	}
	if r, ok := try(a, b); ok {
		return r, nil
	}
	if r, ok := try(b, a); ok {
		return r, nil
	}
	return nil, errors.New("mbim: bad buffer reference")
}
