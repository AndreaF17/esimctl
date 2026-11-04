// Package es9p is a client for the ES9+ interface (LPA <-> SM-DP+), the
// JSON-over-HTTPS binding defined in SGP.22 section 6.
package es9p

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Client talks to one SM-DP+.
//
// SM-DP+ TLS certificates chain to the GSMA CI rather than to a public CA, so
// they fail verification against the system store. When that happens the
// client retries without TLS verification: server authenticity is still
// enforced end to end, because the eUICC verifies the SM-DP+ certificate
// chain and signature in AuthenticateServer, and the profile package is
// protected by keys negotiated with the eUICC.
type Client struct {
	Address  string
	Strict   bool // never fall back to unverified TLS
	http     *http.Client
	insecure bool
}

func New(address string) *Client {
	return &Client{Address: address, http: &http.Client{Timeout: 60 * time.Second}}
}

// ServerError is a "Failed" functionExecutionStatus.
type ServerError struct {
	Function, Subject, Reason, Message string
}

func (e *ServerError) Error() string {
	msg := fmt.Sprintf("SM-DP+ %s failed: subject %s reason %s", e.Function, e.Subject, e.Reason)
	if e.Message != "" {
		msg += " (" + e.Message + ")"
	}
	if hint := reasonHints[e.Subject+"/"+e.Reason]; hint != "" {
		msg += ": " + hint
	}
	return msg
}

var reasonHints = map[string]string{
	"8.2.6/3.8":  "matching ID refused (already used or wrong activation code)",
	"8.2.5/4.10": "too many attempts, retry later",
	"8.1/4.8":    "insufficient eUICC memory",
	"8.2.7/2.2":  "confirmation code required",
	"8.2.7/3.8":  "wrong confirmation code",
	"8.2.7/6.4":  "too many wrong confirmation codes, profile order cancelled",
	"8.8/3.10":   "SM-DP+ doesn't trust this eUICC's certificates",
	"8.1.1/3.8":  "EID refused by SM-DP+ (profile bound to another device)",
	"8.2.1/3.9":  "unknown or expired profile order",
}

type header struct {
	FunctionExecutionStatus struct {
		Status         string `json:"status"`
		StatusCodeData struct {
			SubjectCode string `json:"subjectCode"`
			ReasonCode  string `json:"reasonCode"`
			Message     string `json:"message"`
		} `json:"statusCodeData"`
	} `json:"functionExecutionStatus"`
}

// hostRE matches an SM-DP+ address as SGP.22 defines it: an FQDN, with an
// optional port. Anything else (a scheme, a path, credentials) is refused so
// that a crafted activation code can't downgrade ES9+ to plain HTTP or point
// it at an arbitrary URL.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*(:[0-9]{1,5})?$`)

// ValidAddress reports whether addr is a well-formed SM-DP+ address.
func ValidAddress(addr string) bool { return len(addr) <= 255 && hostRE.MatchString(addr) }

func (c *Client) post(ctx context.Context, function string, req any, resp any) error {
	if !ValidAddress(c.Address) {
		return fmt.Errorf("invalid SM-DP+ address %q", c.Address)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	url := "https://" + c.Address + "/gsma/rsp2/es9plus/" + function

	raw, err := c.do(ctx, url, body)
	var uerr *tls.CertificateVerificationError
	var unk x509.UnknownAuthorityError
	if err != nil && !c.Strict && !c.insecure && (errors.As(err, &uerr) || errors.As(err, &unk)) {
		c.insecure = true
		c.http = &http.Client{Timeout: c.http.Timeout, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}}
		raw, err = c.do(ctx, url, body)
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 { // 204 for handleNotification
		return nil
	}
	var h struct {
		Header header `json:"header"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return fmt.Errorf("SM-DP+ %s: bad JSON: %w", function, err)
	}
	st := h.Header.FunctionExecutionStatus
	if st.Status != "" && !strings.HasPrefix(st.Status, "Executed") {
		return &ServerError{Function: function, Subject: st.StatusCodeData.SubjectCode,
			Reason: st.StatusCodeData.ReasonCode, Message: st.StatusCodeData.Message}
	}
	if resp != nil {
		return json.Unmarshal(raw, resp)
	}
	return nil
}

func (c *Client) do(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Admin-Protocol", "gsma/rsp/v2.2.0")
	req.Header.Set("User-Agent", "gsma-rsp-lpad")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("SM-DP+ HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// Insecure reports whether TLS verification had to be disabled.
func (c *Client) Insecure() bool { return c.insecure }

var b64 = base64.StdEncoding

// B64 is a byte slice that marshals as standard base64.
type B64 []byte

func (b B64) MarshalJSON() ([]byte, error) { return json.Marshal(b64.EncodeToString(b)) }
func (b *B64) UnmarshalJSON(d []byte) error {
	var s string
	if err := json.Unmarshal(d, &s); err != nil {
		return err
	}
	v, err := b64.DecodeString(s)
	*b = v
	return err
}

type InitiateAuthResponse struct {
	TransactionID       string `json:"transactionId"`
	ServerSigned1       B64    `json:"serverSigned1"`
	ServerSignature1    B64    `json:"serverSignature1"`
	EuiccCiPKIdToBeUsed B64    `json:"euiccCiPKIdToBeUsed"`
	ServerCertificate   B64    `json:"serverCertificate"`
}

func (c *Client) InitiateAuthentication(ctx context.Context, challenge, info1 []byte) (*InitiateAuthResponse, error) {
	var r InitiateAuthResponse
	err := c.post(ctx, "initiateAuthentication", map[string]any{
		"smdpAddress":    c.Address,
		"euiccChallenge": B64(challenge),
		"euiccInfo1":     B64(info1),
	}, &r)
	return &r, err
}

type AuthenticateClientResponse struct {
	TransactionID   string `json:"transactionId"`
	ProfileMetadata B64    `json:"profileMetadata"`
	SmdpSigned2     B64    `json:"smdpSigned2"`
	SmdpSignature2  B64    `json:"smdpSignature2"`
	SmdpCertificate B64    `json:"smdpCertificate"`
}

func (c *Client) AuthenticateClient(ctx context.Context, tid string, authServerResp []byte) (*AuthenticateClientResponse, error) {
	var r AuthenticateClientResponse
	err := c.post(ctx, "authenticateClient", map[string]any{
		"transactionId":              tid,
		"authenticateServerResponse": B64(authServerResp),
	}, &r)
	return &r, err
}

func (c *Client) GetBoundProfilePackage(ctx context.Context, tid string, prepResp []byte) ([]byte, error) {
	var r struct {
		BoundProfilePackage B64 `json:"boundProfilePackage"`
	}
	err := c.post(ctx, "getBoundProfilePackage", map[string]any{
		"transactionId":           tid,
		"prepareDownloadResponse": B64(prepResp),
	}, &r)
	return r.BoundProfilePackage, err
}

func (c *Client) HandleNotification(ctx context.Context, pending []byte) error {
	return c.post(ctx, "handleNotification", map[string]any{"pendingNotification": B64(pending)}, nil)
}

func (c *Client) CancelSession(ctx context.Context, tid string, resp []byte) error {
	return c.post(ctx, "cancelSession", map[string]any{
		"transactionId":         tid,
		"cancelSessionResponse": B64(resp),
	}, nil)
}
