package tlv

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	long := bytes.Repeat([]byte{0xAA}, 300)
	enc := Cons(0xBF2D, Encode(0x5A, []byte{1, 2}), Encode(0x9F70, []byte{1}), Encode(0x94, long))
	root, err := ParseOne(enc)
	if err != nil {
		t.Fatal(err)
	}
	if root.Tag != 0xBF2D || !root.Constructed() {
		t.Fatalf("tag %X", root.Tag)
	}
	if v, ok := root.Find(0x9F70); !ok || Int(v.Value) != 1 {
		t.Fatal("9F70 not found")
	}
	if v, ok := root.Find(0x94); !ok || len(v.Value) != 300 || v.HeaderLen() != 4 {
		t.Fatalf("long value: %d %d", len(v.Value), v.HeaderLen())
	}
	if !bytes.Equal(root.Raw, enc) {
		t.Fatal("raw mismatch")
	}
}

func TestTruncated(t *testing.T) {
	if _, _, err := Parse([]byte{0x5A, 0x05, 0x01}); err == nil {
		t.Fatal("expected error")
	}
}
