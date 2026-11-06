package httpserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/plusclouds/ubuntu-agent/internal/config"
	"github.com/plusclouds/ubuntu-agent/internal/protocol"
)

type fakeDisp struct{}

func (fakeDisp) Dispatch(_ context.Context, env protocol.Envelope) protocol.Envelope {
	env.Type = "result"
	return env
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(config.HTTPConfig{Listen: "127.0.0.1:0"}, fakeDisp{}, testToken, "uuid", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthAndDispatch(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.srv.Handler)
	defer ts.Close()

	body, _ := json.Marshal(protocol.Envelope{ID: "1", Type: "command"})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/command", bytes.NewReader(body))
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/command", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, _ = http.DefaultClient.Do(req)
	var out protocol.Envelope
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out.Type != "result" {
		t.Fatalf("got %d %+v", resp.StatusCode, out)
	}
}

func TestEmptyTokenRejected(t *testing.T) {
	if _, err := New(config.HTTPConfig{}, fakeDisp{}, "", "u", zap.NewNop()); err == nil {
		t.Fatal("want error for empty token")
	}
}

func TestSelfSignedTLS(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "a.crt"), filepath.Join(dir, "a.key")

	if _, _, err := LoadOrCreateCert(cert, key, false, nil); err == nil {
		t.Fatal("self_signed=false with missing files must error")
	}
	c1, self, err := LoadOrCreateCert(cert, key, true, []string{"agent.example.com"})
	if err != nil || !self {
		t.Fatalf("generate: %v self=%v", err, self)
	}
	c2, _, err := LoadOrCreateCert(cert, key, true, nil)
	if err != nil || string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Fatal("cert must be reused on second load")
	}

	s := newTestServer(t)
	ts := httptest.NewUnstartedServer(s.srv.Handler)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{c1}}
	ts.StartTLS()
	defer ts.Close()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz over TLS: %v", err)
	}
}

type acceptedDisp struct{}

func (acceptedDisp) Dispatch(_ context.Context, env protocol.Envelope) protocol.Envelope {
	res, _ := protocol.ResultFor(env, "uuid", protocol.StatusAccepted, "started", map[string]string{"job_id": "j1"})
	return res
}

func TestAcceptedResultReturns202(t *testing.T) {
	s, err := New(config.HTTPConfig{Listen: "127.0.0.1:0"}, acceptedDisp{}, testToken, "uuid", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.srv.Handler)
	defer ts.Close()
	body, _ := json.Marshal(protocol.Envelope{ID: "1", Type: "command"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/command", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %v / %v", resp, err)
	}
}

func TestSelfSignedRenewalAndOperatorCertUntouched(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "r.crt"), filepath.Join(dir, "r.key")

	// A self-signed cert expiring in 10 days is inside the 30-day renewal window.
	if err := generateSelfSignedValid(cert, key, []string{"a.example", "a.example", "10.9.8.7"}, 10*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	old, _, err := LoadOrCreateCert(cert, key, false, nil) // selfSigned=false: must NOT renew
	if err != nil {
		t.Fatal(err)
	}
	same, _, _ := LoadOrCreateCert(cert, key, false, nil)
	if string(old.Certificate[0]) != string(same.Certificate[0]) {
		t.Fatal("with self_signed=false the existing cert must be left alone")
	}
	renewed, isSelf, err := LoadOrCreateCert(cert, key, true, nil) // selfSigned=true: renews
	if err != nil || !isSelf {
		t.Fatalf("renew: %v self=%v", err, isSelf)
	}
	if string(old.Certificate[0]) == string(renewed.Certificate[0]) {
		t.Fatal("an expiring self-signed cert must be regenerated")
	}
	leaf, _ := x509.ParseCertificate(renewed.Certificate[0])
	if time.Until(leaf.NotAfter) < 300*24*time.Hour {
		t.Fatalf("renewed cert should be valid ~1 year, expires %v", leaf.NotAfter)
	}
	seen := map[string]bool{}
	for _, ip := range leaf.IPAddresses {
		if seen[ip.String()] {
			t.Fatalf("duplicate SAN IP %s", ip)
		}
		seen[ip.String()] = true
	}
	// And a fresh cert is reused, not regenerated.
	again, _, _ := LoadOrCreateCert(cert, key, true, nil)
	if string(renewed.Certificate[0]) != string(again.Certificate[0]) {
		t.Fatal("a fresh cert must be reused")
	}
}

const testToken = "0123456789abcdef0123456789abcdef-test-token"

func TestWeakTokensRefused(t *testing.T) {
	for _, tok := range []string{"secret", "CHANGE_ME_GENERATE_A_LONG_RANDOM_TOKEN", "change_me_" + testToken, "short-token-31-chars-xxxxxxxxxx"[:31]} {
		if _, err := New(config.HTTPConfig{}, fakeDisp{}, tok, "u", zap.NewNop()); err == nil {
			t.Errorf("token %q should be refused", tok)
		}
	}
	if _, err := New(config.HTTPConfig{}, fakeDisp{}, testToken, "u", zap.NewNop()); err != nil {
		t.Errorf("long token refused: %v", err)
	}
}

func TestVersionHeadersHealthAndRejection(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var h map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if resp.Header.Get("X-Protocol-Version") != "1" || resp.Header.Get("X-Agent-Version") == "" {
		t.Fatalf("missing version headers: %v", resp.Header)
	}
	if _, ok := h["agent_version"]; !ok || h["protocol_versions"] == nil {
		t.Fatalf("healthz lacks versions: %v", h)
	}

	body, _ := json.Marshal(protocol.Envelope{V: 7, ID: "1", Type: "command"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/command", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported version: got %d, want 400", resp.StatusCode)
	}
}
