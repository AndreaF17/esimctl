package euicc

import (
	"errors"
	"fmt"

	"github.com/AndreaF17/esimctl/internal/tlv"
)

var authErrors = map[int]string{
	1: "invalid certificate", 2: "invalid signature", 3: "unsupported curve",
	4: "no session context", 5: "invalid OID", 6: "eUICC challenge mismatch",
	7: "CI public key unknown", 127: "undefined error",
}

var installErrors = map[int]string{
	1: "incorrect input values", 2: "invalid signature", 3: "invalid transaction ID",
	4: "unsupported CRT values", 5: "unsupported remote operation type",
	6: "unsupported profile class", 7: "SCP03t structure error", 8: "SCP03t security error",
	9: "a profile with this ICCID already exists", 10: "insufficient memory for profile",
	11: "installation interrupted", 12: "profile element processing error",
	13: "data mismatch", 14: "invalid NAA key in test profile",
	15: "profile policy rule not allowed", 127: "unknown error",
}

// CardError is an error reported inside an ES10b response structure.
type CardError struct {
	Op   string
	Code int
}

func (e *CardError) Error() string {
	names := authErrors
	if e.Op == "install" {
		names = installErrors
	}
	if n, ok := names[e.Code]; ok {
		return fmt.Sprintf("eUICC %s: %s (%d)", e.Op, n, e.Code)
	}
	return fmt.Sprintf("eUICC %s: error %d", e.Op, e.Code)
}

// DeviceInfo returns the DeviceInfo structure sent in ctxParams1: a TAC plus
// the radio releases this device supports.
func DeviceInfo(tac []byte) []byte {
	rel := []byte{0x0F, 0x00, 0x00} // release 15
	caps := tlv.Cons(0xA1,
		tlv.Encode(0x80, rel), // GSM
		tlv.Encode(0x81, rel), // UTRAN
		tlv.Encode(0x85, rel), // E-UTRAN / EPC
	)
	return tlv.Cons(0xA1, tlv.Encode(0x80, tac), caps)
}

// AuthenticateServer forwards the SM-DP+ InitiateAuthentication result to the
// eUICC. The raw response is returned even on error, since the SM-DP+ wants it.
func (c *Card) AuthenticateServer(signed1, sig1, ciPKID, cert []byte, matchingID string, devInfo []byte) ([]byte, error) {
	var ctx []byte
	if matchingID != "" {
		ctx = append(ctx, tlv.Encode(0x80, []byte(matchingID))...)
	}
	ctx = append(ctx, devInfo...)
	req := tlv.Cons(0xBF38, signed1, sig1, ciPKID, cert, tlv.Encode(0xA0, ctx))
	t, err := c.Call(req)
	if err != nil {
		return nil, err
	}
	if e, ok := t.Path(0xA1, 0x81); ok {
		return t.Raw, &CardError{Op: "authenticate server", Code: tlv.Int(e.Value)}
	}
	return t.Raw, nil
}

// PrepareDownload forwards AuthenticateClient results. hashCC is nil when no
// confirmation code is required.
func (c *Card) PrepareDownload(signed2, sig2, hashCC, cert []byte) ([]byte, error) {
	parts := [][]byte{signed2, sig2}
	if hashCC != nil {
		parts = append(parts, tlv.Encode(0x04, hashCC))
	}
	parts = append(parts, cert)
	t, err := c.Call(tlv.Cons(0xBF21, parts...))
	if err != nil {
		return nil, err
	}
	if e, ok := t.Path(0xA1, 0x81); ok {
		return t.Raw, &CardError{Op: "prepare download", Code: tlv.Int(e.Value)}
	}
	return t.Raw, nil
}

// CancelSession aborts an RSP session; the response goes to the SM-DP+.
func (c *Card) CancelSession(tid []byte, reason int) ([]byte, error) {
	t, err := c.Call(tlv.Cons(0xBF41, tlv.Encode(0x80, tid), tlv.Encode(0x81, intBytes(reason))))
	if err != nil {
		return nil, err
	}
	return t.Raw, nil
}

// InstallResult is the outcome of loading a bound profile package.
type InstallResult struct {
	Raw     []byte // ProfileInstallationResult, to be sent as a notification
	Seq     int
	Address string
	Err     error
}

// LoadBoundProfilePackage splits the BPP into the segments mandated by
// SGP.22 section 2.5.5 and feeds them to the eUICC. progress, if set, is
// called after each segment.
func (c *Card) LoadBoundProfilePackage(bpp []byte, progress func(done, total int)) (*InstallResult, error) {
	root, err := tlv.ParseOne(bpp)
	if err != nil || root.Tag != 0xBF36 {
		return nil, errors.New("malformed bound profile package")
	}
	kids, err := root.Children()
	if err != nil || len(kids) < 4 || kids[0].Tag != 0xBF23 {
		return nil, errors.New("malformed bound profile package")
	}

	// Segment 1: BF36 header plus the whole InitialiseSecureChannel.
	segs := [][]byte{append(append([]byte{}, root.Raw[:root.HeaderLen()]...), kids[0].Raw...)}
	for _, k := range kids[1:] {
		switch k.Tag {
		case 0xA0, 0xA2: // sequences of '87' sent whole
			segs = append(segs, k.Raw)
		case 0xA1, 0xA3: // header, then each '88' / '86' separately
			segs = append(segs, k.Raw[:k.HeaderLen()])
			inner, err := k.Children()
			if err != nil {
				return nil, errors.New("malformed bound profile package")
			}
			for _, e := range inner {
				segs = append(segs, e.Raw)
			}
		default:
			return nil, fmt.Errorf("unexpected BPP element %X", k.Tag)
		}
	}

	for i, s := range segs {
		r, err := c.StoreData(s)
		if err != nil {
			return nil, fmt.Errorf("loading segment %d/%d: %w", i+1, len(segs), err)
		}
		if progress != nil {
			progress(i+1, len(segs))
		}
		if len(r) > 0 {
			return parseInstallResult(r)
		}
	}
	return nil, errors.New("eUICC returned no installation result")
}

func parseInstallResult(r []byte) (*InstallResult, error) {
	t, err := tlv.ParseOne(r)
	if err != nil || t.Tag != 0xBF37 {
		return nil, fmt.Errorf("unexpected installation response % X", r)
	}
	res := &InstallResult{Raw: t.Raw}
	n := metadataOf(t)
	res.Seq, res.Address = n.Seq, n.Address
	if e, ok := t.Path(0xBF27, 0xA2, 0xA1, 0x81); ok {
		res.Err = &CardError{Op: "install", Code: tlv.Int(e.Value)}
	}
	return res, nil
}
