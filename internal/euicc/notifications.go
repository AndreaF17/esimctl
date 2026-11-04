package euicc

import (
	"errors"
	"fmt"

	"github.com/AndreaF17/esimctl/internal/tlv"
)

// Notification describes a pending profile-management notification.
type Notification struct {
	Seq       int    `json:"seq"`
	Operation string `json:"operation"`
	Address   string `json:"address"`
	ICCID     string `json:"iccid,omitempty"`
}

var notificationEvents = []string{"install", "enable", "disable", "delete"}

func parseMetadata(m tlv.TLV) Notification {
	var n Notification
	kids, _ := m.Children()
	for _, k := range kids {
		switch k.Tag {
		case 0x80:
			n.Seq = tlv.Int(k.Value)
		case 0x81:
			// BIT STRING: first octet is the number of unused bits.
			if len(k.Value) >= 2 {
				for i, name := range notificationEvents {
					if k.Value[1]&(0x80>>i) != 0 {
						n.Operation = name
					}
				}
			}
		case 0x0C:
			n.Address = string(k.Value)
		case 0x5A:
			n.ICCID = DecodeICCID(k.Value)
		}
	}
	return n
}

// Notifications lists pending notifications.
func (c *Card) Notifications() ([]Notification, error) {
	t, err := c.Call(tlv.Encode(0xBF28, nil))
	if err != nil {
		return nil, err
	}
	if e, ok := t.Find(0x81); ok {
		return nil, fmt.Errorf("euicc: list notification error %d", tlv.Int(e.Value))
	}
	list, ok := t.Find(0xA0)
	if !ok {
		return nil, errors.New("euicc: malformed notification list")
	}
	kids, err := list.Children()
	if err != nil {
		return nil, err
	}
	var out []Notification
	for _, k := range kids {
		if k.Tag == 0xBF2F {
			out = append(out, parseMetadata(k))
		}
	}
	return out, nil
}

// RetrieveNotification returns the signed PendingNotification for seq, ready
// to be sent to the SM-DP+ via ES9+ handleNotification, plus its metadata.
func (c *Card) RetrieveNotification(seq int) ([]byte, Notification, error) {
	t, err := c.Call(tlv.Cons(0xBF2B, tlv.Cons(0xA0, tlv.Encode(0x80, intBytes(seq)))))
	if err != nil {
		return nil, Notification{}, err
	}
	if e, ok := t.Find(0x81); ok {
		return nil, Notification{}, fmt.Errorf("euicc: retrieve notification error %d", tlv.Int(e.Value))
	}
	list, ok := t.Find(0xA0)
	if !ok {
		return nil, Notification{}, errors.New("euicc: malformed notification")
	}
	kids, err := list.Children()
	if err != nil || len(kids) == 0 {
		return nil, Notification{}, fmt.Errorf("euicc: notification %d not found", seq)
	}
	pn := kids[0]
	return pn.Raw, metadataOf(pn), nil
}

// metadataOf digs the NotificationMetadata out of a PendingNotification.
func metadataOf(pn tlv.TLV) Notification {
	var m tlv.TLV
	var ok bool
	switch pn.Tag {
	case 0xBF37: // ProfileInstallationResult
		m, ok = pn.Path(0xBF27, 0xBF2F)
	case 0x30: // OtherSignedNotification
		m, ok = pn.Find(0xBF2F)
	}
	if !ok {
		return Notification{}
	}
	return parseMetadata(m)
}

// RemoveNotification deletes a notification from the eUICC list.
func (c *Card) RemoveNotification(seq int) error {
	t, err := c.Call(tlv.Cons(0xBF30, tlv.Encode(0x80, intBytes(seq))))
	if err != nil {
		return err
	}
	return resultCode(t, "remove notification", map[int]string{1: "nothing to delete"})
}

// intBytes encodes a non-negative INTEGER in minimal two's complement form.
func intBytes(v int) []byte {
	var b []byte
	for {
		b = append([]byte{byte(v)}, b...)
		v >>= 8
		if v == 0 {
			break
		}
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return b
}
