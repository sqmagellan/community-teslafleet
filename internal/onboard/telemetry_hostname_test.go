package onboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The car dials the telemetry server over mutual TLS, which no tunnel or
// TLS-terminating proxy can carry, while the public key is plain HTTPS and is
// often served through one. So the telemetry host can differ from the partner
// domain; Tesla accepts any host under the same root domain.

func newHostnameServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	ftc := filepath.Join(dir, "ftc.json")
	s, err := NewServer(Options{DataDir: dir, EnrollFile: ftc},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.store.st.Domain = "keys.example.com"
	return s, ftc
}

func savedHostname(t *testing.T, ftc string) string {
	t.Helper()
	b, err := os.ReadFile(ftc)
	if err != nil {
		t.Fatalf("no enrollment config: %v", err)
	}
	var c struct {
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c.Hostname
}

func saveEnrollmentForm(s *Server, vals url.Values) *httptest.ResponseRecorder {
	vals.Set("profile", "eco")
	vals.Set("ca", testCA)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, form(http.MethodPost, "/enrollment", vals))
	return rr
}

func TestTelemetryHostnameDefaultsToDomain(t *testing.T) {
	s, ftc := newHostnameServer(t)
	saveEnrollmentForm(s, url.Values{})
	if got := savedHostname(t, ftc); got != "keys.example.com" {
		t.Errorf("hostname = %q, want the partner domain", got)
	}
}

func TestTelemetryHostnameFromTheWizard(t *testing.T) {
	s, ftc := newHostnameServer(t)
	saveEnrollmentForm(s, url.Values{"hostname": {" telemetry.example.com "}})
	if got := savedHostname(t, ftc); got != "telemetry.example.com" {
		t.Errorf("hostname = %q, want telemetry.example.com", got)
	}
	// It is remembered, and shown, for the next save.
	rr := httptest.NewRecorder()
	s.page(rr, loopbackReq(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Body.String(), `value="telemetry.example.com"`) {
		t.Error("step 6b does not show the saved telemetry hostname")
	}
	saveEnrollmentForm(s, url.Values{})
	if got := savedHostname(t, ftc); got != "telemetry.example.com" {
		t.Errorf("hostname after a save without the field = %q", got)
	}
}
