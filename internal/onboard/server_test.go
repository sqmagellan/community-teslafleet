package onboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewServer(Options{DataDir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s, dir
}

// loopbackReq builds a request that passes the passwordless auth gate. With no
// password configured the wizard serves loopback only, and httptest defaults
// RemoteAddr to a non-loopback address.
func loopbackReq(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "127.0.0.1:54321"
	return r
}

func TestServer_PageRenders(t *testing.T) {
	s, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, loopbackReq(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "onboarding") {
		t.Errorf("page missing expected content")
	}
}

// Without a password the wizard must refuse the network. Every handler behind
// this gate can replace the signing keypair or the stored refresh token.
func TestServer_RejectsNonLoopbackWithoutPassword(t *testing.T) {
	s, dir := newTestServer(t)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/generate", nil)
	r.RemoteAddr = "192.168.1.50:41234"
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST /generate from the LAN = %d, want 403", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "private-key.pem")); err == nil {
		t.Error("rejected request still generated a keypair")
	}
}

func TestServer_GenerateThenDownload(t *testing.T) {
	s, dir := newTestServer(t)
	h := s.Handler()

	// Generate keys → 303 redirect (PRG).
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, loopbackReq(http.MethodPost, "/generate", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /generate = %d, want 303", rr.Code)
	}
	for _, f := range []string{"private-key.pem", "public-key.pem", "state.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s after generate: %v", f, err)
		}
	}
	// Private key must be 0600.
	if fi, _ := os.Stat(filepath.Join(dir, "private-key.pem")); fi != nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("private key perms = %v, want 0600", fi.Mode().Perm())
	}

	// Download public key.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, loopbackReq(http.MethodGet, "/public-key.pem", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "PUBLIC KEY") {
		t.Errorf("download public key: code=%d body=%.40q", rr.Code, rr.Body.String())
	}
}

func TestServer_BasicAuth(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewServer(Options{DataDir: dir, Password: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no-auth GET = %d, want 401", rr.Code)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("x", "secret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("auth GET = %d, want 200", rr.Code)
	}
}
