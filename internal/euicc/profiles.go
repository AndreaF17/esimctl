package euicc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/andrea/esimctl/internal/tlv"
)

// Profile is one entry of the eUICC profile list.
type Profile struct {
	ICCID    string `json:"iccid"`
	ISDPAID  string `json:"isdp_aid"`
	Enabled  bool   `json:"enabled"`
	Nickname string `json:"nickname,omitempty"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	Class    string `json:"class"`
	IconType string `json:"icon_type,omitempty"`
	Icon     []byte `json:"icon,omitempty"`
}

var profileClasses = map[int]string{0: "test", 1: "provisioning", 2: "operational"}

// DecodeICCID converts the nibble-swapped BCD encoding to digits.
func DecodeICCID(b []byte) string {
	var sb strings.Builder
	for _, x := range b {
		for _, n := range []byte{x & 0x0F, x >> 4} {
			if n <= 9 {
				sb.WriteByte('0' + n)
			}
		}
	}
	return sb.String()
}

// EncodeICCID converts digits to the 10-byte nibble-swapped BCD encoding.
func EncodeICCID(s string) ([]byte, error) {
	if len(s) < 18 || len(s) > 20 {
		return nil, fmt.Errorf("invalid ICCID %q", s)
	}
	for len(s) < 20 {
		s += "F"
	}
	out := make([]byte, 10)
	for i := 0; i < 10; i++ {
		lo, hi := s[2*i], s[2*i+1]
		if !isNibble(lo) || !isNibble(hi) {
			return nil, fmt.Errorf("invalid ICCID %q", s)
		}
		out[i] = nibble(hi)<<4 | nibble(lo)
	}
	return out, nil
}

func isNibble(c byte) bool { return c >= '0' && c <= '9' || c == 'F' || c == 'f' }
func nibble(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	return 0xF
}

func parseProfile(e tlv.TLV) Profile {
	p := Profile{Class: "operational"}
	kids, _ := e.Children()
	for _, k := range kids {
		switch k.Tag {
		case 0x5A:
			p.ICCID = DecodeICCID(k.Value)
		case 0x4F:
			p.ISDPAID = hex.EncodeToString(k.Value)
		case 0x9F70:
			p.Enabled = tlv.Int(k.Value) == 1
		case 0x90:
			p.Nickname = string(k.Value)
		case 0x91:
			p.Provider = string(k.Value)
		case 0x92:
			p.Name = string(k.Value)
		case 0x93:
			p.IconType = map[int]string{0: "jpg", 1: "png"}[tlv.Int(k.Value)]
		case 0x94:
			p.Icon = k.Value
		case 0x95:
			p.Class = profileClasses[tlv.Int(k.Value)]
		}
	}
	return p
}

// Profiles returns the installed profiles.
func (c *Card) Profiles() ([]Profile, error) {
	t, err := c.Call(tlv.Encode(0xBF2D, nil))
	if err != nil {
		return nil, err
	}
	if e, ok := t.Find(0x81); ok {
		return nil, fmt.Errorf("euicc: profile list error %d", tlv.Int(e.Value))
	}
	list, ok := t.Find(0xA0)
	if !ok {
		return nil, errors.New("euicc: malformed profile list")
	}
	kids, err := list.Children()
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, k := range kids {
		if k.Tag == 0xE3 {
			out = append(out, parseProfile(k))
		}
	}
	return out, nil
}

var switchErrors = map[int]string{
	1: "ICCID or AID not found", 2: "profile not in the required state",
	3: "disallowed by policy", 4: "wrong profile re-enabling", 5: "CAT busy",
}

func iccidChoice(iccid string) ([]byte, error) {
	b, err := EncodeICCID(iccid)
	if err != nil {
		return nil, err
	}
	return tlv.Encode(0x5A, b), nil
}

// Enable enables the profile with the given ICCID. refresh asks the eUICC to
// issue a REFRESH so the modem picks up the new profile.
func (c *Card) Enable(iccid string, refresh bool) error {
	return c.switchProfile(0xBF31, "enable", iccid, refresh)
}

// Disable disables the profile with the given ICCID.
func (c *Card) Disable(iccid string, refresh bool) error {
	return c.switchProfile(0xBF32, "disable", iccid, refresh)
}

func (c *Card) switchProfile(tag uint32, op, iccid string, refresh bool) error {
	id, err := iccidChoice(iccid)
	if err != nil {
		return err
	}
	flag := byte(0)
	if refresh {
		flag = 0xFF
	}
	t, err := c.Call(tlv.Cons(tag, tlv.Cons(0xA0, id), tlv.Encode(0x81, []byte{flag})))
	if err != nil {
		return err
	}
	return resultCode(t, op, switchErrors)
}

// Delete removes the profile with the given ICCID. It must be disabled.
func (c *Card) Delete(iccid string) error {
	id, err := iccidChoice(iccid)
	if err != nil {
		return err
	}
	t, err := c.Call(tlv.Cons(0xBF33, id))
	if err != nil {
		return err
	}
	return resultCode(t, "delete", map[int]string{1: "ICCID or AID not found", 2: "profile not in disabled state", 3: "disallowed by policy"})
}

// SetNickname sets the user-visible nickname of a profile.
func (c *Card) SetNickname(iccid, nick string) error {
	b, err := EncodeICCID(iccid)
	if err != nil {
		return err
	}
	if len(nick) > 64 {
		return errors.New("nickname too long (max 64 bytes)")
	}
	t, err := c.Call(tlv.Cons(0xBF29, tlv.Encode(0x5A, b), tlv.Encode(0x90, []byte(nick))))
	if err != nil {
		return err
	}
	return resultCode(t, "set nickname", map[int]string{1: "ICCID not found"})
}
