// Package tlv implements the BER-TLV encoding used by GSMA SGP.22.
package tlv

import (
	"errors"
	"fmt"
)

// TLV is a decoded BER-TLV element. Raw holds the complete encoding
// (tag + length + value), which SGP.22 often needs to forward verbatim.
type TLV struct {
	Tag   uint32
	Value []byte
	Raw   []byte
}

// Constructed reports whether the tag has the constructed bit set.
func (t TLV) Constructed() bool {
	first := t.Tag
	for first > 0xFF {
		first >>= 8
	}
	return first&0x20 != 0
}

// Children parses the value of a constructed element.
func (t TLV) Children() ([]TLV, error) { return ParseAll(t.Value) }

// Find returns the first direct child with the given tag.
func (t TLV) Find(tag uint32) (TLV, bool) {
	kids, err := t.Children()
	if err != nil {
		return TLV{}, false
	}
	for _, k := range kids {
		if k.Tag == tag {
			return k, true
		}
	}
	return TLV{}, false
}

// Path follows a chain of tags through nested constructed elements.
func (t TLV) Path(tags ...uint32) (TLV, bool) {
	cur := t
	for _, tag := range tags {
		next, ok := cur.Find(tag)
		if !ok {
			return TLV{}, false
		}
		cur = next
	}
	return cur, true
}

// Parse decodes one element from b and returns it with the remaining bytes.
func Parse(b []byte) (TLV, []byte, error) {
	if len(b) == 0 {
		return TLV{}, nil, errors.New("tlv: empty input")
	}
	i := 0
	tag := uint32(b[i])
	i++
	if b[0]&0x1F == 0x1F {
		for {
			if i >= len(b) {
				return TLV{}, nil, errors.New("tlv: truncated tag")
			}
			tag = tag<<8 | uint32(b[i])
			i++
			if b[i-1]&0x80 == 0 {
				break
			}
			if i > 4 {
				return TLV{}, nil, errors.New("tlv: tag too long")
			}
		}
	}
	if i >= len(b) {
		return TLV{}, nil, errors.New("tlv: truncated length")
	}
	l := int(b[i])
	i++
	if l&0x80 != 0 {
		n := l & 0x7F
		if n == 0 || n > 4 || i+n > len(b) {
			return TLV{}, nil, errors.New("tlv: bad length")
		}
		l = 0
		for j := 0; j < n; j++ {
			l = l<<8 | int(b[i])
			i++
		}
	}
	if i+l > len(b) {
		return TLV{}, nil, fmt.Errorf("tlv: value of tag %X truncated (%d > %d)", tag, l, len(b)-i)
	}
	return TLV{Tag: tag, Value: b[i : i+l], Raw: b[:i+l]}, b[i+l:], nil
}

// ParseOne decodes b, which must contain exactly one element.
func ParseOne(b []byte) (TLV, error) {
	t, rest, err := Parse(b)
	if err != nil {
		return t, err
	}
	if len(rest) != 0 {
		return t, errors.New("tlv: trailing data")
	}
	return t, nil
}

// ParseAll decodes a sequence of concatenated elements.
func ParseAll(b []byte) ([]TLV, error) {
	var out []TLV
	for len(b) > 0 {
		t, rest, err := Parse(b)
		if err != nil {
			return out, err
		}
		out = append(out, t)
		b = rest
	}
	return out, nil
}

// HeaderLen returns the length of the tag+length prefix of an encoded element.
func (t TLV) HeaderLen() int { return len(t.Raw) - len(t.Value) }

// Encode builds a TLV from a tag and value.
func Encode(tag uint32, value []byte) []byte {
	out := tagBytes(tag)
	out = append(out, lenBytes(len(value))...)
	return append(out, value...)
}

// Cons builds a constructed TLV whose value is the concatenation of children.
func Cons(tag uint32, children ...[]byte) []byte {
	var v []byte
	for _, c := range children {
		v = append(v, c...)
	}
	return Encode(tag, v)
}

func tagBytes(tag uint32) []byte {
	switch {
	case tag > 0xFFFFFF:
		return []byte{byte(tag >> 24), byte(tag >> 16), byte(tag >> 8), byte(tag)}
	case tag > 0xFFFF:
		return []byte{byte(tag >> 16), byte(tag >> 8), byte(tag)}
	case tag > 0xFF:
		return []byte{byte(tag >> 8), byte(tag)}
	}
	return []byte{byte(tag)}
}

func lenBytes(l int) []byte {
	switch {
	case l < 0x80:
		return []byte{byte(l)}
	case l <= 0xFF:
		return []byte{0x81, byte(l)}
	case l <= 0xFFFF:
		return []byte{0x82, byte(l >> 8), byte(l)}
	case l <= 0xFFFFFF:
		return []byte{0x83, byte(l >> 16), byte(l >> 8), byte(l)}
	}
	return []byte{0x84, byte(l >> 24), byte(l >> 16), byte(l >> 8), byte(l)}
}

// Int decodes a big-endian unsigned integer value.
func Int(v []byte) int {
	n := 0
	for _, b := range v {
		n = n<<8 | int(b)
	}
	return n
}
