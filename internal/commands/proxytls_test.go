package commands

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// tlsProxy is a fake vehicle-command proxy on TLS. httptest's certificate
// names example.com, 127.0.0.1 and ::1. caFile holds that certificate.
func tlsProxy(t *testing.T) (url, caFile string, sent func() string) {
	t.Helper()
	var name string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/1/vehicles/{vin}/command/{name}", func(w http.ResponseWriter, r *http.Request) {
		name = r.PathValue("name")
		_, _ = w.Write([]byte(`{"response":{"result":true}}`))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "proxy-ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return srv.URL, caFile, func() string { return name }
}

func relayWithTLS(t *testing.T, url string, cfg config.Commands) *Relay {
	t.Helper()
	tc, err := ProxyTLSConfig(cfg)
	if err != nil {
		t.Fatalf("ProxyTLSConfig: %v", err)
	}
	return &Relay{
		tm:          &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
		proxy:       url,
		client:      NewProxyClient(tc),
		apiClient:   http.DefaultClient,
		log:         testLogger(),
		proxyHealth: &proxyHealth{insecure: cfg.ProxyInsecureSkipVerify},
	}
}

func TestProxyTLS_VerifiesAgainstConfiguredCA(t *testing.T) {
	url, ca, sent := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{ProxyCAFile: ca})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeSent || sent() != "honk_horn" {
		t.Fatalf("verified hop failed: %+v sent=%q", res, sent())
	}
	if got := r.ProxyStatus(); got != "ok" {
		t.Fatalf("ProxyStatus = %q, want ok", got)
	}
}

// The old client skipped verification for every deployment.
func TestProxyTLS_UnknownCertificateIsRefused(t *testing.T) {
	url, _, sent := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{})
	res := r.Handle("VIN1", "honk", "PRESS")
	if res.Outcome != OutcomeFailed || sent() != "" {
		t.Fatalf("an unverified proxy got the command: %+v sent=%q", res, sent())
	}
	if !strings.Contains(r.ProxyStatus(), "certificate") {
		t.Fatalf("ProxyStatus = %q, want the certificate error", r.ProxyStatus())
	}
}

func TestProxyTLS_HostNameIsChecked(t *testing.T) {
	url, ca, sent := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{ProxyCAFile: ca, ProxyServerName: "vehicle-command-proxy"})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeFailed || sent() != "" {
		t.Fatalf("a certificate for another name was accepted: %+v", res)
	}
	r = relayWithTLS(t, url, config.Commands{ProxyCAFile: ca, ProxyServerName: "example.com"})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeSent {
		t.Fatalf("server-name override not used: %+v", res)
	}
}

func TestProxyTLS_InsecureEscapeHatch(t *testing.T) {
	url, _, sent := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{ProxyInsecureSkipVerify: true})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeSent || sent() != "honk_horn" {
		t.Fatalf("insecure_skip_verify did not skip: %+v", res)
	}
	if !strings.Contains(r.ProxyStatus(), "disabled") {
		t.Fatalf("ProxyStatus = %q, want it to say verification is disabled", r.ProxyStatus())
	}
}

func TestProxyTLS_BadCAFileIsAnError(t *testing.T) {
	if _, err := ProxyTLSConfig(config.Commands{ProxyCAFile: "/nonexistent/ca.pem"}); err == nil {
		t.Error("missing CA file accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	_ = os.WriteFile(junk, []byte("not a cert"), 0o644)
	if _, err := ProxyTLSConfig(config.Commands{ProxyCAFile: junk}); err == nil {
		t.Error("CA file without a certificate accepted")
	}
	// The fail-closed fallback must not trust anything.
	url, _, _ := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{})
	r.client = NewProxyClient(FailClosedTLS())
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeFailed {
		t.Fatalf("fail-closed client sent a command: %+v", res)
	}
}

func TestProbeProxy(t *testing.T) {
	url, ca, _ := tlsProxy(t)
	r := relayWithTLS(t, url, config.Commands{ProxyCAFile: ca})
	if err := r.ProbeProxy(); err != nil {
		t.Fatalf("probe over verified TLS: %v", err)
	}
	r = relayWithTLS(t, url, config.Commands{})
	if err := r.ProbeProxy(); err == nil {
		t.Fatal("probe accepted an unknown certificate")
	}
}

// TLS settings mean nothing on an http:// URL, and a redirect could carry the
// token somewhere else. Both are refused.
func TestProxyClient_RefusesPlaintextAndRedirects(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	r := relayWithTLS(t, plain.URL, config.Commands{})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeFailed {
		t.Fatalf("command sent over plain http: %+v", res)
	}
	if err := r.ProbeProxy(); err == nil {
		t.Fatal("probe accepted plain http")
	}

	url, ca, sent := tlsProxy(t)
	redir := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, url+req.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	caB, _ := os.ReadFile(ca)
	both := filepath.Join(t.TempDir(), "both.pem")
	_ = os.WriteFile(both, append(caB, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redir.Certificate().Raw})...), 0o644)
	r = relayWithTLS(t, redir.URL, config.Commands{ProxyCAFile: both})
	if res := r.Handle("VIN1", "honk", "PRESS"); res.Outcome != OutcomeFailed || sent() != "" {
		t.Fatalf("redirect followed: %+v sent=%q", res, sent())
	}
}
