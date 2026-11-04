package modem

import (
	"encoding/xml"
	"os"
	"strings"
	"unicode"
)

// ProvidersDB is the mobile-broadband-provider-info database NetworkManager
// also uses for gsm.auto-config.
var ProvidersDB = "/usr/share/mobile-broadband-provider-info/serviceproviders.xml"

// APN is a data APN offered by a provider.
type APN struct {
	Provider string `json:"provider"`
	Name     string `json:"name"` // the APN itself
	Label    string `json:"label,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

type xmlProviders struct {
	Countries []struct {
		Providers []struct {
			Name string `xml:"name"`
			GSM  *struct {
				Networks []struct {
					MCC string `xml:"mcc,attr"`
					MNC string `xml:"mnc,attr"`
				} `xml:"network-id"`
				APNs []struct {
					Value  string `xml:"value,attr"`
					Usages []struct {
						Type string `xml:"type,attr"`
					} `xml:"usage"`
					Name     string `xml:"name"`
					Username string `xml:"username"`
					Password string `xml:"password"`
				} `xml:"apn"`
			} `xml:"gsm"`
		} `xml:"provider"`
	} `xml:"country"`
}

// LookupAPNs returns the internet APNs for an IMSI prefix or MCCMNC (5 or 6
// digits; 6-digit inputs also match 2-digit MNCs).
func LookupAPNs(mccmnc string) ([]APN, error) {
	if len(mccmnc) < 5 {
		return nil, nil
	}
	f, err := os.Open(ProvidersDB)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var db xmlProviders
	if err := xml.NewDecoder(f).Decode(&db); err != nil {
		return nil, err
	}
	mcc := mccmnc[:3]
	var out []APN
	for _, c := range db.Countries {
		for _, p := range c.Providers {
			if p.GSM == nil {
				continue
			}
			match := false
			for _, n := range p.GSM.Networks {
				code := n.MCC + n.MNC
				if n.MCC == mcc && (code == mccmnc || len(mccmnc) == 6 && len(n.MNC) == 2 && code == mccmnc[:5]) {
					match = true
				}
			}
			if !match {
				continue
			}
			for _, a := range p.GSM.APNs {
				internet := len(a.Usages) == 0
				for _, u := range a.Usages {
					internet = internet || u.Type == "internet"
				}
				if internet {
					out = append(out, APN{Provider: p.Name, Name: a.Value, Label: a.Name, User: a.Username, Password: a.Password})
				}
			}
		}
	}
	return out, nil
}

// BestAPN picks the APN for a SIM from its home network code and the
// operator name it advertises (SPN). Virtual operators share their host's
// network code, so the name is what tells, say, CoopVoce from TIM. ok is
// false when the choice would be a guess.
func BestAPN(mccmnc, spn string) (APN, bool) {
	apns, err := LookupAPNs(mccmnc)
	if err != nil || len(apns) == 0 {
		return APN{}, false
	}
	if spn != "" {
		want := normalize(spn)
		for _, a := range apns {
			if p := normalize(a.Provider); p == want || strings.Contains(p, want) || strings.Contains(want, p) {
				return a, true
			}
		}
	}
	providers := map[string]bool{}
	for _, a := range apns {
		providers[a.Provider] = true
	}
	return apns[0], len(providers) == 1
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
