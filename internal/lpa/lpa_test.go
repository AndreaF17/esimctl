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
	// A crafted QR must not be able to pick the scheme or the URL path.
	for _, bad := range []string{
		"LPA:1$http://evil.example$ID",
		"LPA:1$evil.example/steal?x=$ID",
		"LPA:1$user@evil.example$ID",
		"LPA:1$evil.example:99999999$ID",
		"LPA:1$-evil.example$ID",
	} {
		if _, err := ParseActivationCode(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if ac, err := ParseActivationCode("LPA:1$rsp.example.com:8443$ID"); err != nil || ac.SMDP != "rsp.example.com:8443" {
		t.Fatalf("port rejected: %+v %v", ac, err)
	}
}
