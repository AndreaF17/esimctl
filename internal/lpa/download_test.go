package lpa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AndreaF17/esimctl/internal/es9p"
	"github.com/AndreaF17/esimctl/internal/euicc"
	"github.com/AndreaF17/esimctl/internal/tlv"
)

const testTID = "0A0B0C0D"

// fakeEUICC answers ES10 commands sent through STORE DATA.
type fakeEUICC struct {
	mu          sync.Mutex
	buf         []byte
	calls       map[uint32][][]byte // tag -> request bodies
	bppSegments int
	installErr  int // non-zero: ProfileInstallationResult carries this error
	notifAddr   string
}

func newFakeEUICC() *fakeEUICC { return &fakeEUICC{calls: map[uint32][][]byte{}} }

func (f *fakeEUICC) Transmit(ch uint32, apdu []byte) ([]byte, uint16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if apdu[1] != 0xE2 {
		return nil, 0x6D00, nil
	}
	lc := int(apdu[4])
	f.buf = append(f.buf, apdu[5:5+lc]...)
	if apdu[2] == 0x11 { // more blocks follow
		return nil, 0x9000, nil
	}
	req := f.buf
	f.buf = nil
	return f.handle(req), 0x9000, nil
}

func (f *fakeEUICC) handle(req []byte) []byte {
	t, _, err := tlv.Parse(req)
	if err != nil {
		// LoadBoundProfilePackage segments are headers or bare TLVs.
		return f.bpp(req)
	}
	switch t.Tag {
	case 0xBF36, 0xA0, 0xA1, 0xA3, 0x86, 0x87, 0x88:
		return f.bpp(req)
	}
	f.calls[t.Tag] = append(f.calls[t.Tag], t.Value)
	switch t.Tag {
	case 0xBF2E:
		return tlv.Cons(0xBF2E, tlv.Encode(0x80, bytes.Repeat([]byte{0xCC}, 16)))
	case 0xBF20:
		return tlv.Cons(0xBF20, tlv.Encode(0x82, []byte{2, 2, 2}))
	case 0xBF38:
		return tlv.Cons(0xBF38, tlv.Cons(0xA0, tlv.Encode(0x30, []byte("auth-ok"))))
	case 0xBF21:
		return tlv.Cons(0xBF21, tlv.Cons(0xA0, tlv.Encode(0x30, []byte("prep-ok"))))
	case 0xBF41:
		return tlv.Cons(0xBF41, tlv.Cons(0xA0, tlv.Encode(0x30, []byte("cancelled"))))
	case 0xBF30:
		return tlv.Cons(0xBF30, tlv.Encode(0x80, []byte{0}))
	}
	return tlv.Cons(t.Tag, tlv.Encode(0x80, []byte{127}))
}

func (f *fakeEUICC) bpp(seg []byte) []byte {
	f.bppSegments++
	// Last segment of testBPP is the second '86'.
	if !bytes.Equal(seg, tlv.Encode(0x86, []byte{6})) {
		return nil
	}
	final := tlv.Cons(0xA2, tlv.Cons(0xA0, tlv.Encode(0x4F, []byte{1})))
	if f.installErr != 0 {
		final = tlv.Cons(0xA2, tlv.Cons(0xA1, tlv.Encode(0x80, []byte{3}), tlv.Encode(0x81, []byte{byte(f.installErr)})))
	}
	tid, _ := hex.DecodeString(testTID)
	return tlv.Cons(0xBF37, tlv.Cons(0xBF27,
		tlv.Encode(0x80, tid),
		tlv.Cons(0xBF2F, tlv.Encode(0x80, []byte{7}), tlv.Encode(0x81, []byte{7, 0x80}), tlv.Encode(0x0C, []byte(f.notifAddr))),
		final), tlv.Encode(0x5F37, []byte("sig")))
}

var testBPP = tlv.Cons(0xBF36,
	tlv.Encode(0xBF23, []byte{1}),
	tlv.Cons(0xA0, tlv.Encode(0x87, []byte{2})),
	tlv.Cons(0xA1, tlv.Encode(0x88, []byte{3}), tlv.Encode(0x88, []byte{4})),
	tlv.Cons(0xA3, tlv.Encode(0x86, []byte{5}), tlv.Encode(0x86, []byte{6})),
)

// fakeSMDP is an ES9+ server.
type fakeSMDP struct {
	t          *testing.T
	ccRequired bool
	failAuth   bool
	mu         sync.Mutex
	calls      []string
	bodies     map[string]map[string]string
}

func (s *fakeSMDP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fn := strings.TrimPrefix(r.URL.Path, "/gsma/rsp2/es9plus/")
	if r.Header.Get("X-Admin-Protocol") == "" || r.Header.Get("Content-Type") != "application/json" {
		s.t.Errorf("%s: missing ES9+ headers", fn)
	}
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.calls = append(s.calls, fn)
	s.bodies[fn] = body
	s.mu.Unlock()

	b64 := base64.StdEncoding.EncodeToString
	ok := map[string]any{"functionExecutionStatus": map[string]string{"status": "Executed-Success"}}
	reply := func(v map[string]any) {
		v["header"] = ok
		json.NewEncoder(w).Encode(v)
	}
	switch fn {
	case "initiateAuthentication":
		if s.failAuth {
			json.NewEncoder(w).Encode(map[string]any{"header": map[string]any{"functionExecutionStatus": map[string]any{
				"status": "Failed", "statusCodeData": map[string]string{"subjectCode": "8.2.6", "reasonCode": "3.8"}}}})
			return
		}
		reply(map[string]any{
			"transactionId":       testTID,
			"serverSigned1":       b64(tlv.Encode(0x30, []byte("signed1"))),
			"serverSignature1":    b64(tlv.Encode(0x5F37, []byte("sig1"))),
			"euiccCiPKIdToBeUsed": b64(tlv.Encode(0x04, []byte("pkid"))),
			"serverCertificate":   b64(tlv.Encode(0x30, []byte("cert1"))),
		})
	case "authenticateClient":
		iccid, _ := euicc.EncodeICCID("8939000000000000001")
		meta := tlv.Cons(0xBF25, tlv.Encode(0x5A, iccid), tlv.Encode(0x91, []byte("Test Mobile")), tlv.Encode(0x92, []byte("Test Plan")))
		flag := byte(0)
		if s.ccRequired {
			flag = 0xFF
		}
		tid, _ := hex.DecodeString(testTID)
		reply(map[string]any{
			"transactionId":   testTID,
			"profileMetadata": b64(meta),
			"smdpSigned2":     b64(tlv.Cons(0x30, tlv.Encode(0x80, tid), tlv.Encode(0x01, []byte{flag}))),
			"smdpSignature2":  b64(tlv.Encode(0x5F37, []byte("sig2"))),
			"smdpCertificate": b64(tlv.Encode(0x30, []byte("cert2"))),
		})
	case "getBoundProfilePackage":
		reply(map[string]any{"transactionId": testTID, "boundProfilePackage": b64(testBPP)})
	case "handleNotification", "cancelSession":
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T, smdp *fakeSMDP) (*Session, *fakeEUICC, ActivationCode) {
	smdp.t = t
	smdp.bodies = map[string]map[string]string{}
	srv := httptest.NewTLSServer(smdp)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "https://")
	card := newFakeEUICC()
	card.notifAddr = addr
	s := &Session{Card: euicc.New(card, 1)}
	return s, card, ActivationCode{SMDP: addr, MatchingID: "MATCH-1"}
}

func TestDownloadSuccess(t *testing.T) {
	smdp := &fakeSMDP{}
	s, card, ac := setup(t, smdp)
	var stages []string
	var seen ProfileMetadata
	res, err := s.Download(context.Background(), ac, DownloadOptions{
		Progress: func(stage string, d, n int) { stages = append(stages, stage) },
		Confirm: func(m ProfileMetadata, cc bool) (bool, string) {
			seen = m
			if cc {
				t.Error("cc not required")
			}
			return true, ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Name != "Test Plan" || seen.Provider != "Test Mobile" || seen.ICCID != "8939000000000000001" {
		t.Fatalf("metadata %+v", seen)
	}
	if res.Profile.ICCID != seen.ICCID || !res.InsecureTLS || !res.NotifiedSMDP {
		t.Fatalf("result %+v", res)
	}
	want := []string{"initiateAuthentication", "authenticateClient", "getBoundProfilePackage", "handleNotification"}
	if strings.Join(smdp.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("server calls %v", smdp.calls)
	}
	// BF36+BF23, A0, A1 header, 2x88, A3 header, 2x86
	if card.bppSegments != 8 {
		t.Fatalf("%d BPP segments", card.bppSegments)
	}
	auth := card.calls[0xBF38][0]
	if !bytes.Contains(auth, tlv.Encode(0x80, []byte("MATCH-1"))) || !bytes.Contains(auth, tlv.Encode(0x30, []byte("signed1"))) {
		t.Fatalf("AuthenticateServer request % X", auth)
	}
	if len(card.calls[0xBF30]) != 1 || !bytes.Equal(card.calls[0xBF30][0], tlv.Encode(0x80, []byte{7})) {
		t.Fatalf("notification not removed: %v", card.calls[0xBF30])
	}
	b := smdp.bodies["authenticateClient"]["authenticateServerResponse"]
	raw, _ := base64.StdEncoding.DecodeString(b)
	if r, err := tlv.ParseOne(raw); err != nil || r.Tag != 0xBF38 {
		t.Fatalf("authenticateServerResponse not forwarded verbatim: %v", err)
	}
	if smdp.bodies["initiateAuthentication"]["smdpAddress"] != ac.SMDP {
		t.Fatal("smdpAddress not sent")
	}
	if len(stages) == 0 || stages[len(stages)-1] != "notifying SM-DP+" {
		t.Fatalf("stages %v", stages)
	}
}

func TestDownloadConfirmationCode(t *testing.T) {
	smdp := &fakeSMDP{ccRequired: true}
	s, card, ac := setup(t, smdp)
	_, err := s.Download(context.Background(), ac, DownloadOptions{
		Confirm: func(m ProfileMetadata, cc bool) (bool, string) {
			if !cc {
				t.Error("cc should be required")
			}
			return true, "1234"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := hex.DecodeString(testTID)
	h1 := sha256.Sum256([]byte("1234"))
	h2 := sha256.Sum256(append(h1[:], tid...))
	if !bytes.Contains(card.calls[0xBF21][0], tlv.Encode(0x04, h2[:])) {
		t.Fatalf("hashed confirmation code missing from PrepareDownload")
	}
}

func TestDownloadMissingConfirmationCode(t *testing.T) {
	s, card, ac := setup(t, &fakeSMDP{ccRequired: true})
	_, err := s.Download(context.Background(), ac, DownloadOptions{})
	if err == nil || !strings.Contains(err.Error(), "confirmation code") {
		t.Fatalf("err = %v", err)
	}
	if len(card.calls[0xBF41]) != 1 {
		t.Fatal("session not cancelled on the eUICC")
	}
}

func TestDownloadDeclined(t *testing.T) {
	smdp := &fakeSMDP{}
	s, card, ac := setup(t, smdp)
	_, err := s.Download(context.Background(), ac, DownloadOptions{
		Confirm: func(ProfileMetadata, bool) (bool, string) { return false, "" },
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v", err)
	}
	if len(card.calls[0xBF41]) != 1 || !bytes.Contains(card.calls[0xBF41][0], tlv.Encode(0x81, []byte{0})) {
		t.Fatal("eUICC session not cancelled with endUserRejection")
	}
	if smdp.calls[len(smdp.calls)-1] != "cancelSession" {
		t.Fatalf("server calls %v", smdp.calls)
	}
	if len(card.calls[0xBF21]) != 0 || card.bppSegments != 0 {
		t.Fatal("download continued after the user declined")
	}
}

func TestDownloadServerError(t *testing.T) {
	s, _, ac := setup(t, &fakeSMDP{failAuth: true})
	_, err := s.Download(context.Background(), ac, DownloadOptions{})
	var se *es9p.ServerError
	if !errors.As(err, &se) || se.Subject != "8.2.6" || !strings.Contains(err.Error(), "matching ID refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadInstallError(t *testing.T) {
	s, card, ac := setup(t, &fakeSMDP{})
	card.installErr = 9
	res, err := s.Download(context.Background(), ac, DownloadOptions{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if res == nil || !res.NotifiedSMDP {
		t.Fatal("failed installation must still be reported to the SM-DP+")
	}
}

func TestDownloadStrictTLS(t *testing.T) {
	s, _, ac := setup(t, &fakeSMDP{})
	_, err := s.Download(context.Background(), ac, DownloadOptions{Strict: true})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("strict mode accepted an untrusted certificate: %v", err)
	}
}

func TestProcessNotifications(t *testing.T) {
	smdp := &fakeSMDP{}
	s, card, _ := setup(t, smdp)
	// A card with one pending "enable" notification.
	nc := &notifCard{fakeEUICC: newFakeEUICC(), addr: card.notifAddr}
	s.Card = euicc.New(nc, 1)
	sent, errs := s.ProcessNotifications(context.Background(), true)
	if sent != 1 || len(errs) != 0 {
		t.Fatalf("sent %d errs %v", sent, errs)
	}
	if !nc.removed {
		t.Fatal("notification not removed")
	}
	if got := smdp.bodies["handleNotification"]["pendingNotification"]; got == "" {
		t.Fatal("pendingNotification not sent")
	}
}

type notifCard struct {
	*fakeEUICC
	addr    string
	removed bool
}

func (n *notifCard) Transmit(ch uint32, apdu []byte) ([]byte, uint16, error) {
	t, _, _ := tlv.Parse(apdu[5 : len(apdu)-1])
	meta := tlv.Cons(0xBF2F, tlv.Encode(0x80, []byte{3}), tlv.Encode(0x81, []byte{4, 0x40}), tlv.Encode(0x0C, []byte(n.addr)))
	switch t.Tag {
	case 0xBF28:
		if n.removed {
			return tlv.Cons(0xBF28, tlv.Encode(0xA0, nil)), 0x9000, nil
		}
		return tlv.Cons(0xBF28, tlv.Cons(0xA0, meta)), 0x9000, nil
	case 0xBF2B:
		other := tlv.Cons(0x30, meta, tlv.Encode(0x5F37, []byte("sig")))
		return tlv.Cons(0xBF2B, tlv.Cons(0xA0, other)), 0x9000, nil
	case 0xBF30:
		n.removed = true
		return tlv.Cons(0xBF30, tlv.Encode(0x80, []byte{0})), 0x9000, nil
	}
	return nil, 0x6D00, nil
}
