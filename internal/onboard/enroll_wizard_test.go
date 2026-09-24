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

	"github.com/LasseLegarth/community-teslafleet/internal/enroll"
)

func form(method, target string, vals url.Values) *http.Request {
	r := loopbackReq(method, target, strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// A second press of "Generate" used to replace the key every car is paired
// to, with no warning.
func TestServer_RegenerateNeedsConfirmation(t *testing.T) {
	s, dir := newTestServer(t)
	h := s.Handler()
	h.ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/generate", nil))
	key := filepath.Join(dir, "private-key.pem")
	first, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, form(http.MethodPost, "/generate", nil))
	if now, _ := os.ReadFile(key); string(now) != string(first) {
		t.Fatal("key replaced without confirmation")
	}
	if !strings.Contains(rr.Body.String(), "Tick the box") {
		t.Errorf("no explanation shown: %.200s", rr.Body.String())
	}

	h.ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/generate", url.Values{"confirm": {"replace"}}))
	if now, _ := os.ReadFile(key); string(now) == string(first) {
		t.Error("confirmed regenerate kept the old key")
	}
}

func TestServer_RejectsCrossSitePost(t *testing.T) {
	s, dir := newTestServer(t)
	r := form(http.MethodPost, "/generate", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "private-key.pem")); err == nil {
		t.Error("cross-site POST generated a key")
	}

	r = form(http.MethodPost, "/generate", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("same-origin POST = %d, want 303", rr.Code)
	}
}

func TestServer_FleetAPIFromTheWizard(t *testing.T) {
	s, _ := newTestServer(t)
	s.opts.FleetAPI = "https://from-config"
	if got := s.fleetAPI(); got != "https://from-config" {
		t.Fatalf("fleetAPI() = %q", got)
	}
	s.Handler().ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/domain",
		url.Values{"domain": {"t.example.com"}, "fleet_api": {"https://fleet-api.prd.na.vn.cloud.tesla.com/"}}))
	if got := s.fleetAPI(); got != "https://fleet-api.prd.na.vn.cloud.tesla.com" {
		t.Errorf("fleetAPI() after step 1 = %q", got)
	}
}

const testCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"

// Step 7 posted the bare config, which panics Tesla's proxy (upstream issue
// #1). It must send {vins, config}, and refuse a config without a ca.
func TestServer_EnrollSendsTheEnvelope(t *testing.T) {
	var got []byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"response":{"updated_vehicles":1}}`))
	}))
	defer proxy.Close()

	dir := t.TempDir()
	ftcPath := filepath.Join(dir, "ftc.json")
	s, err := NewServer(Options{DataDir: dir, ProxyURL: proxy.URL, EnrollFile: ftcPath, Tokens: &fakeOwner{}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	write := func(ca string) {
		ftc := enroll.Generate("balanced", "t.example.com", 443)
		ftc.CA = ca
		b, _ := json.Marshal(ftc)
		if err := os.WriteFile(ftcPath, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(testCA)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, form(http.MethodPost, "/enroll", nil))
	if got != nil || !strings.Contains(rr.Body.String(), "List vehicles") {
		t.Fatalf("enrolled with no cars known: sent=%q", got)
	}

	s.store.st.Vehicles = []teslaVehicle{{VIN: "VIN1"}}
	write("")
	h.ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/enroll", nil))
	if got != nil {
		t.Fatalf("sent a config with no ca: %s", got)
	}

	write(testCA)
	h.ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/enroll", nil))
	var env struct {
		VINs   []string        `json:"vins"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(got, &env); err != nil || len(env.VINs) != 1 || env.VINs[0] != "VIN1" || len(env.Config) == 0 {
		t.Errorf("posted %s, want {vins:[VIN1], config:{...}}", got)
	}
}

func TestServer_SaveEnrollmentKeepsThePastedChain(t *testing.T) {
	dir := t.TempDir()
	ftcPath := filepath.Join(dir, "ftc.json")
	s, err := NewServer(Options{DataDir: dir, EnrollFile: ftcPath}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.Handler().ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/enrollment",
		url.Values{"profile": {"eco"}, "port": {"443"}, "ca": {testCA}}))
	b, err := os.ReadFile(ftcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !enroll.HasCA(b) {
		t.Errorf("saved config lost the chain: %s", b)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, form(http.MethodPost, "/enrollment",
		url.Values{"profile": {"eco"}, "port": {"443"}, "ca": {"not a cert"}}))
	if !strings.Contains(rr.Body.String(), "not PEM") {
		t.Errorf("junk chain accepted: %.200s", rr.Body.String())
	}
}
