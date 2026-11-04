package modem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleDB = `<?xml version="1.0"?>
<serviceproviders format="2.0">
 <country code="it">
  <provider><name>TIM</name><gsm>
   <network-id mcc="222" mnc="01"/>
   <apn value="ibox.tim.it"><usage type="internet"/><name>TIM Web</name></apn>
   <apn value="unico.tim.it"><usage type="mms"/></apn>
  </gsm></provider>
  <provider><name>Iliad</name><gsm>
   <network-id mcc="222" mnc="50"/>
   <apn value="iliad"><usage type="internet"/></apn>
  </gsm></provider>
 </country>
 <country code="us">
  <provider><name>T-Mobile</name><gsm>
   <network-id mcc="310" mnc="260"/>
   <apn value="fast.t-mobile.com"><usage type="internet"/></apn>
  </gsm></provider>
 </country>
</serviceproviders>`

func TestLookupAPNs(t *testing.T) {
	ProvidersDB = filepath.Join(t.TempDir(), "sp.xml")
	os.WriteFile(ProvidersDB, []byte(sampleDB), 0o644)
	for in, want := range map[string]string{
		"222015": "ibox.tim.it",       // IMSI prefix, 2-digit MNC
		"22250":  "iliad",             //
		"310260": "fast.t-mobile.com", // 3-digit MNC
	} {
		apns, err := LookupAPNs(in)
		if err != nil || len(apns) != 1 || apns[0].Name != want {
			t.Fatalf("%s: %+v %v", in, apns, err)
		}
	}
	if apns, _ := LookupAPNs("222990"); len(apns) != 0 {
		t.Fatalf("unknown network matched: %+v", apns)
	}
}

func TestBestAPN(t *testing.T) {
	ProvidersDB = filepath.Join(t.TempDir(), "sp.xml")
	db := strings.Replace(sampleDB, "</country>", `<provider><name>CoopVoce</name><gsm>
   <network-id mcc="222" mnc="01"/><apn value="web.coopvoce.it"><usage type="internet"/></apn>
  </gsm></provider></country>`, 1)
	os.WriteFile(ProvidersDB, []byte(db), 0o644)
	if a, ok := BestAPN("22201", "CoopVoce"); !ok || a.Name != "web.coopvoce.it" {
		t.Fatalf("SPN match: %+v %v", a, ok)
	}
	if a, ok := BestAPN("22201", "TIM"); !ok || a.Name != "ibox.tim.it" {
		t.Fatalf("SPN match: %+v %v", a, ok)
	}
	if _, ok := BestAPN("22201", ""); ok {
		t.Fatal("ambiguous network code reported as certain")
	}
	if a, ok := BestAPN("22250", ""); !ok || a.Name != "iliad" {
		t.Fatalf("single provider: %+v %v", a, ok)
	}
}

func TestRealDatabase(t *testing.T) {
	if _, err := os.Stat("/usr/share/mobile-broadband-provider-info/serviceproviders.xml"); err != nil {
		t.Skip("provider database not installed")
	}
	ProvidersDB = "/usr/share/mobile-broadband-provider-info/serviceproviders.xml"
	apns, err := LookupAPNs("222015")
	if err != nil || len(apns) == 0 {
		t.Fatalf("TIM Italy: %+v %v", apns, err)
	}
	t.Logf("TIM: %+v", apns)
}

func TestCleanError(t *testing.T) {
	got := cleanError("error: couldn't enable the modem: 'GDBus.Error:org.freedesktop.ModemManager1.Error.Core.Retry: Retry: Invalid transition'")
	if got != "Retry: Invalid transition" {
		t.Fatalf("%q", got)
	}
}

func TestBlockingLock(t *testing.T) {
	for in, want := range map[string]string{
		"none": "", "sim-pin2": "", "sim-puk2": "", "": "",
		"sim-pin": "sim-pin", "sim-puk": "sim-puk", "ph-net-pin": "ph-net-pin",
	} {
		if got := blockingLock(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
