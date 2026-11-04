// Package euicc implements the ES10a/b/c interfaces of GSMA SGP.22 on top of
// a logical channel opened to the ISD-R.
package euicc

import (
	"errors"
	"fmt"

	"github.com/andrea/esimctl/internal/tlv"
)

// ISDR is the AID of the eUICC's ISD-R (the LPA-facing RSP application).
var ISDR = []byte{0xA0, 0x00, 0x00, 0x05, 0x59, 0x10, 0x10, 0xFF, 0xFF, 0xFF, 0xFF, 0x89, 0x00, 0x00, 0x01, 0x00}

// Transport sends one command APDU on a logical channel.
type Transport interface {
	Transmit(ch uint32, apdu []byte) ([]byte, uint16, error)
}

// SWError is an unexpected ISO 7816 status word.
type SWError uint16

func (e SWError) Error() string { return fmt.Sprintf("euicc: card returned SW=%04X", uint16(e)) }

// Card is an ISD-R session.
type Card struct {
	t  Transport
	ch uint32
}

func New(t Transport, channel uint32) *Card { return &Card{t: t, ch: channel} }

func (c *Card) cla(proprietary bool) byte {
	var b byte
	if c.ch < 4 {
		b = byte(c.ch)
	} else {
		b = 0x40 | byte(c.ch-4)
	}
	if proprietary {
		b |= 0x80
	}
	return b
}

func (c *Card) transmit(apdu []byte) ([]byte, error) {
	resp, sw, err := c.t.Transmit(c.ch, apdu)
	if err != nil {
		return nil, err
	}
	for sw>>8 == 0x61 {
		le := byte(sw)
		more, sw2, err := c.t.Transmit(c.ch, []byte{c.cla(false), 0xC0, 0x00, 0x00, le})
		if err != nil {
			return nil, err
		}
		resp = append(resp, more...)
		sw = sw2
	}
	if sw != 0x9000 {
		return resp, SWError(sw)
	}
	return resp, nil
}

// StoreData sends an ES10 command, segmenting it into STORE DATA blocks.
func (c *Card) StoreData(data []byte) ([]byte, error) {
	const block = 255
	var resp []byte
	for i, n := 0, 0; ; n++ {
		end := i + block
		last := end >= len(data)
		if last {
			end = len(data)
		}
		p1 := byte(0x11)
		if last {
			p1 = 0x91
		}
		chunk := data[i:end]
		apdu := append([]byte{c.cla(true), 0xE2, p1, byte(n), byte(len(chunk))}, chunk...)
		apdu = append(apdu, 0x00)
		r, err := c.transmit(apdu)
		if err != nil {
			return nil, err
		}
		resp = append(resp, r...)
		if last {
			return resp, nil
		}
		i = end
	}
}

// Call sends an ES10 command and checks that the response carries the expected tag.
func (c *Card) Call(req []byte) (tlv.TLV, error) {
	r, err := c.StoreData(req)
	if err != nil {
		return tlv.TLV{}, err
	}
	want, _, err := tlv.Parse(req)
	if err != nil {
		return tlv.TLV{}, err
	}
	t, err := tlv.ParseOne(r)
	if err != nil {
		return t, fmt.Errorf("euicc: bad response: %w", err)
	}
	if t.Tag != want.Tag {
		return t, fmt.Errorf("euicc: response tag %X, expected %X", t.Tag, want.Tag)
	}
	return t, nil
}

// EID returns the eUICC identifier as a hex string.
func (c *Card) EID() (string, error) {
	t, err := c.Call(tlv.Cons(0xBF3E, tlv.Encode(0x5C, []byte{0x5A})))
	if err != nil {
		return "", err
	}
	eid, ok := t.Find(0x5A)
	if !ok {
		return "", errors.New("euicc: EID missing")
	}
	return fmt.Sprintf("%X", eid.Value), nil
}

// EUICCInfo1 returns the raw EUICCInfo1 (BF20) structure.
func (c *Card) EUICCInfo1() (tlv.TLV, error) { return c.Call(tlv.Encode(0xBF20, nil)) }

// EUICCInfo2 returns the raw EUICCInfo2 (BF22) structure.
func (c *Card) EUICCInfo2() (tlv.TLV, error) { return c.Call(tlv.Encode(0xBF22, nil)) }

// Challenge returns a fresh 16-byte eUICC challenge.
func (c *Card) Challenge() ([]byte, error) {
	t, err := c.Call(tlv.Encode(0xBF2E, nil))
	if err != nil {
		return nil, err
	}
	ch, ok := t.Find(0x80)
	if !ok {
		return nil, errors.New("euicc: challenge missing")
	}
	return ch.Value, nil
}

// Addresses holds the eUICC's configured SM-DP+ / SM-DS addresses.
type Addresses struct {
	DefaultSMDP string `json:"default_smdp"`
	RootSMDS    string `json:"root_smds"`
}

func (c *Card) Addresses() (Addresses, error) {
	var a Addresses
	t, err := c.Call(tlv.Encode(0xBF3C, nil))
	if err != nil {
		return a, err
	}
	if v, ok := t.Find(0x80); ok {
		a.DefaultSMDP = string(v.Value)
	}
	if v, ok := t.Find(0x81); ok {
		a.RootSMDS = string(v.Value)
	}
	return a, nil
}

// SetDefaultSMDP sets (or clears, with "") the default SM-DP+ address.
func (c *Card) SetDefaultSMDP(addr string) error {
	t, err := c.Call(tlv.Cons(0xBF3F, tlv.Encode(0x80, []byte(addr))))
	if err != nil {
		return err
	}
	return resultCode(t, "set default SM-DP+", nil)
}

// resultCode interprets the common "[0] INTEGER result" response.
func resultCode(t tlv.TLV, op string, names map[int]string) error {
	r, ok := t.Find(0x80)
	if !ok {
		return fmt.Errorf("euicc: %s: missing result", op)
	}
	code := tlv.Int(r.Value)
	if code == 0 {
		return nil
	}
	if n, ok := names[code]; ok {
		return &ResultError{Op: op, Code: code, Name: n}
	}
	if code == 127 {
		return &ResultError{Op: op, Code: code, Name: "undefined error"}
	}
	return &ResultError{Op: op, Code: code, Name: "error"}
}

// ResultError is a non-zero result from an ES10 operation.
type ResultError struct {
	Op   string
	Code int
	Name string
}

func (e *ResultError) Error() string { return fmt.Sprintf("%s: %s (%d)", e.Op, e.Name, e.Code) }
