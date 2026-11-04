package euicc

import (
	"bytes"
	"testing"

	"github.com/andrea/esimctl/internal/tlv"
)

func TestICCID(t *testing.T) {
	for _, s := range []string{"8939104520000012345", "89391045200000123456"} {
		b, err := EncodeICCID(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := DecodeICCID(b); got != s {
			t.Fatalf("%s -> % X -> %s", s, b, got)
		}
	}
	b, _ := EncodeICCID("8939104520000012345")
	if b[0] != 0x98 || b[9] != 0xF5 {
		t.Fatalf("encoding % X", b)
	}
}

type fakeCard struct {
	apdus [][]byte
	reply func(apdu []byte) ([]byte, uint16)
}

func (f *fakeCard) Transmit(ch uint32, apdu []byte) ([]byte, uint16, error) {
	f.apdus = append(f.apdus, append([]byte{}, apdu...))
	r, sw := f.reply(apdu)
	return r, sw, nil
}

func TestStoreDataSegmentsAndGetResponse(t *testing.T) {
	resp := tlv.Encode(0xBF22, bytes.Repeat([]byte{1}, 10))
	f := &fakeCard{reply: func(a []byte) ([]byte, uint16) {
		if a[1] == 0xC0 {
			return resp[4:], 0x9000
		}
		if a[2] == 0x91 {
			return resp[:4], 0x6100 | uint16(len(resp)-4)
		}
		return nil, 0x9000
	}}
	c := New(f, 1)
	req := tlv.Encode(0xBF22, bytes.Repeat([]byte{7}, 400))
	got, err := c.StoreData(req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, resp) {
		t.Fatalf("response % X", got)
	}
	if len(f.apdus) != 3 || f.apdus[0][0] != 0x81 || f.apdus[0][2] != 0x11 || f.apdus[1][2] != 0x91 || f.apdus[1][3] != 1 {
		t.Fatalf("apdus: % X", f.apdus)
	}
	if f.apdus[2][0] != 0x01 {
		t.Fatalf("GET RESPONSE CLA %02X", f.apdus[2][0])
	}
}

func TestBPPSegmentation(t *testing.T) {
	bpp := tlv.Cons(0xBF36,
		tlv.Encode(0xBF23, []byte{1}),
		tlv.Cons(0xA0, tlv.Encode(0x87, []byte{2})),
		tlv.Cons(0xA1, tlv.Encode(0x88, []byte{3}), tlv.Encode(0x88, []byte{4})),
		tlv.Cons(0xA3, tlv.Encode(0x86, []byte{5}), tlv.Encode(0x86, []byte{6})),
	)
	result := tlv.Cons(0xBF37, tlv.Cons(0xBF27,
		tlv.Encode(0x80, []byte{9}),
		tlv.Cons(0xBF2F, tlv.Encode(0x80, []byte{4}), tlv.Encode(0x0C, []byte("smdp.example"))),
		tlv.Cons(0xA2, tlv.Cons(0xA0, tlv.Encode(0x4F, []byte{1})))))
	var segs [][]byte
	n := 0
	f := &fakeCard{reply: func(a []byte) ([]byte, uint16) {
		segs = append(segs, a[5:len(a)-1])
		n++
		if n == 8 {
			return result, 0x9000
		}
		return nil, 0x9000
	}}
	res, err := New(f, 1).LoadBoundProfilePackage(bpp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 8 || segs[0][0] != 0xBF || segs[1][0] != 0xA0 || len(segs[2]) != 2 || segs[3][0] != 0x88 || len(segs[5]) != 2 {
		t.Fatalf("segments: % X", segs)
	}
	if res.Err != nil || res.Seq != 4 || res.Address != "smdp.example" {
		t.Fatalf("result %+v", res)
	}
}
