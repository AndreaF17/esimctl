package euicc

import (
	"encoding/hex"
	"fmt"

	"github.com/AndreaF17/esimctl/internal/tlv"
)

// Info summarises EUICCInfo2.
type Info struct {
	ProfileVersion   string   `json:"profile_version"`
	SVN              string   `json:"svn"`
	FirmwareVersion  string   `json:"firmware_version"`
	GlobalPlatform   string   `json:"globalplatform_version,omitempty"`
	FreeNVM          int      `json:"free_nvm"`
	InstalledApps    int      `json:"installed_apps"`
	CIKeysVerify     []string `json:"ci_keys_verification"`
	CIKeysSign       []string `json:"ci_keys_signing"`
	SASAccreditation string   `json:"sas_accreditation,omitempty"`
}

func version(v []byte) string {
	if len(v) != 3 {
		return hex.EncodeToString(v)
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func keyList(t tlv.TLV) []string {
	var out []string
	kids, _ := t.Children()
	for _, k := range kids {
		out = append(out, hex.EncodeToString(k.Value))
	}
	return out
}

// ParseInfo2 decodes an EUICCInfo2 structure.
func ParseInfo2(t tlv.TLV) Info {
	var in Info
	kids, _ := t.Children()
	for _, k := range kids {
		switch k.Tag {
		case 0x81:
			in.ProfileVersion = version(k.Value)
		case 0x82:
			in.SVN = version(k.Value)
		case 0x83:
			in.FirmwareVersion = version(k.Value)
		case 0x84:
			res, _ := tlv.ParseAll(k.Value)
			for _, r := range res {
				switch r.Tag {
				case 0x81:
					in.InstalledApps = tlv.Int(r.Value)
				case 0x82:
					in.FreeNVM = tlv.Int(r.Value)
				}
			}
		case 0x87:
			in.GlobalPlatform = version(k.Value)
		case 0xA9:
			in.CIKeysVerify = keyList(k)
		case 0xAA:
			in.CIKeysSign = keyList(k)
		case 0x0C:
			in.SASAccreditation = string(k.Value)
		}
	}
	return in
}
