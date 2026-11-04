package lpa

import "testing"

func TestActivationCode(t *testing.T) {
	ac, err := ParseActivationCode("LPA:1$rsp.example.com$ABC-123$$1")
	if err != nil || ac.SMDP != "rsp.example.com" || ac.MatchingID != "ABC-123" || !ac.CCRequired {
		t.Fatalf("%+v %v", ac, err)
	}
	if _, err := ParseActivationCode("LPA:2$x$y"); err == nil {
		t.Fatal("expected error")
	}
}
