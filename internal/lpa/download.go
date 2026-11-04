package lpa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/AndreaF17/esimctl/internal/es9p"
	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/textutil"
	"github.com/AndreaF17/esimctl/internal/tlv"
)

// ActivationCode is a parsed "LPA:1$<smdp>$<matching id>[$<oid>[$<cc flag>]]" code.
type ActivationCode struct {
	SMDP       string
	MatchingID string
	CCRequired bool
}

func ParseActivationCode(s string) (ActivationCode, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "LPA:")
	parts := strings.Split(s, "$")
	if len(parts) < 2 || parts[0] != "1" || parts[1] == "" {
		return ActivationCode{}, errors.New("invalid activation code (expected LPA:1$smdp$matchingId)")
	}
	if !es9p.ValidAddress(parts[1]) {
		return ActivationCode{}, fmt.Errorf("invalid SM-DP+ address %q in activation code", parts[1])
	}
	ac := ActivationCode{SMDP: parts[1]}
	if len(parts) > 2 {
		ac.MatchingID = parts[2]
	}
	if len(parts) > 4 && parts[4] == "1" {
		ac.CCRequired = true
	}
	return ac, nil
}

// ProfileMetadata is what the SM-DP+ tells us about the profile before download.
type ProfileMetadata = euicc.Profile

// DownloadOptions controls an interactive download.
type DownloadOptions struct {
	ConfirmationCode string
	IMEI             string
	// Confirm is called with the profile metadata before anything is
	// installed; returning false cancels the session.
	Confirm  func(meta ProfileMetadata, ccRequired bool) (ok bool, cc string)
	Progress func(stage string, done, total int)
	Strict   bool
}

// DownloadResult reports a completed download.
type DownloadResult struct {
	Profile      ProfileMetadata `json:"profile"`
	InsecureTLS  bool            `json:"insecure_tls"`
	NotifiedSMDP bool            `json:"notified_smdp"`
}

// ErrCancelled is returned when the user declines the profile.
var ErrCancelled = errors.New("download cancelled")

// Download performs the SGP.22 "profile download and installation" procedure.
func (s *Session) Download(ctx context.Context, ac ActivationCode, opt DownloadOptions) (*DownloadResult, error) {
	stage := func(name string) {
		if opt.Progress != nil {
			opt.Progress(name, 0, 0)
		}
	}
	card := s.Card
	cl := es9p.New(ac.SMDP)
	cl.Strict = opt.Strict

	stage("eUICC challenge")
	challenge, err := card.Challenge()
	if err != nil {
		return nil, err
	}
	info1, err := card.EUICCInfo1()
	if err != nil {
		return nil, err
	}

	stage("authenticating with " + ac.SMDP)
	init, err := cl.InitiateAuthentication(ctx, challenge, info1.Raw)
	if err != nil {
		return nil, err
	}
	tid, err := hex.DecodeString(init.TransactionID)
	if err != nil {
		return nil, fmt.Errorf("bad transaction id %q", init.TransactionID)
	}
	cancel := func(reason int) {
		if r, err := card.CancelSession(tid, reason); err == nil {
			_ = cl.CancelSession(context.WithoutCancel(ctx), init.TransactionID, r)
		}
	}

	stage("eUICC verifying SM-DP+")
	authResp, err := card.AuthenticateServer(init.ServerSigned1, init.ServerSignature1,
		init.EuiccCiPKIdToBeUsed, init.ServerCertificate, ac.MatchingID, euicc.DeviceInfo(tac(opt.IMEI)))
	if err != nil {
		var ce *euicc.CardError
		if errors.As(err, &ce) && authResp != nil {
			// The SM-DP+ must learn about the failure to close its session.
			_, _ = cl.AuthenticateClient(context.WithoutCancel(ctx), init.TransactionID, authResp)
		}
		return nil, err
	}

	stage("authenticating eUICC")
	auth, err := cl.AuthenticateClient(ctx, init.TransactionID, authResp)
	if err != nil {
		return nil, err
	}

	meta := ProfileMetadata{Class: "operational"}
	if m, err := tlv.ParseOne(auth.ProfileMetadata); err == nil {
		meta = parseMetadata(m)
	}
	ccRequired := ac.CCRequired
	if signed2, err := tlv.ParseOne(auth.SmdpSigned2); err == nil {
		if f, ok := signed2.Find(0x01); ok {
			ccRequired = tlv.Int(f.Value) != 0
		}
	}

	cc := opt.ConfirmationCode
	if opt.Confirm != nil {
		ok, typed := opt.Confirm(meta, ccRequired)
		if !ok {
			cancel(0) // endUserRejection
			return nil, ErrCancelled
		}
		if typed != "" {
			cc = typed
		}
	}
	var hashCC []byte
	if ccRequired {
		if cc == "" {
			cancel(0)
			return nil, errors.New("this profile requires a confirmation code")
		}
		h1 := sha256.Sum256([]byte(cc))
		h2 := sha256.Sum256(append(h1[:], tid...))
		hashCC = h2[:]
	}

	stage("preparing download")
	prep, err := card.PrepareDownload(auth.SmdpSigned2, auth.SmdpSignature2, hashCC, auth.SmdpCertificate)
	if err != nil && prep == nil {
		cancel(127)
		return nil, err
	}

	// An eUICC-side PrepareDownload error is still reported to the SM-DP+.
	stage("downloading profile")
	bpp, gerr := cl.GetBoundProfilePackage(ctx, init.TransactionID, prep)
	if err != nil {
		return nil, err
	}
	if gerr != nil {
		return nil, gerr
	}

	res, err := card.LoadBoundProfilePackage(bpp, func(done, total int) {
		if opt.Progress != nil {
			opt.Progress("installing", done, total)
		}
	})
	if err != nil {
		cancel(127)
		return nil, err
	}

	out := &DownloadResult{Profile: meta, InsecureTLS: cl.Insecure()}
	stage("notifying SM-DP+")
	if nerr := cl.HandleNotification(context.WithoutCancel(ctx), res.Raw); nerr == nil {
		out.NotifiedSMDP = true
		if res.Seq > 0 {
			_ = card.RemoveNotification(res.Seq)
		}
	}
	if res.Err != nil {
		return out, res.Err
	}
	return out, nil
}

func parseMetadata(m tlv.TLV) ProfileMetadata {
	p := ProfileMetadata{Class: "operational"}
	kids, _ := m.Children()
	for _, k := range kids {
		switch k.Tag {
		case 0x5A:
			p.ICCID = euicc.DecodeICCID(k.Value)
		case 0x91:
			p.Provider = textutil.Line(string(k.Value))
		case 0x92:
			p.Name = textutil.Line(string(k.Value))
		case 0x93:
			p.IconType = map[int]string{0: "jpg", 1: "png"}[tlv.Int(k.Value)]
		case 0x94:
			p.Icon = k.Value
		case 0x95:
			p.Class = map[int]string{0: "test", 1: "provisioning", 2: "operational"}[tlv.Int(k.Value)]
		}
	}
	return p
}

// tac derives the 4-byte Type Allocation Code from an IMEI, with a generic
// default when the IMEI is unknown.
func tac(imei string) []byte {
	if len(imei) >= 8 {
		var b [4]byte
		if _, err := fmt.Sscanf(imei[:8], "%02x%02x%02x%02x", &b[0], &b[1], &b[2], &b[3]); err == nil {
			// TAC is BCD-coded digits, which hex scanning reproduces exactly.
			return b[:]
		}
	}
	return []byte{0x35, 0x29, 0x06, 0x11}
}

// ProcessNotifications sends every pending notification to its SM-DP+ and
// removes those that were delivered. It returns how many were sent.
func (s *Session) ProcessNotifications(ctx context.Context, remove bool) (int, []error) {
	list, err := s.Card.Notifications()
	if err != nil {
		return 0, []error{err}
	}
	sent := 0
	var errs []error
	for _, n := range list {
		raw, meta, err := s.Card.RetrieveNotification(n.Seq)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		addr := meta.Address
		if addr == "" {
			addr = n.Address
		}
		if err := es9p.New(addr).HandleNotification(ctx, raw); err != nil {
			errs = append(errs, fmt.Errorf("notification %d (%s): %w", n.Seq, addr, err))
			continue
		}
		sent++
		if remove {
			if err := s.Card.RemoveNotification(n.Seq); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return sent, errs
}
