package onboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/enroll"
)

// Tesla answers a fleet_telemetry_config with the cars it skipped and why
// (missing_key, unsupported_firmware, ...). Step 7 reported "sent" whatever
// the answer, so a car without the key looked enrolled.

func enrollAnswer(t *testing.T, answer string) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(proxy.Close)
	dir := t.TempDir()
	ftcPath := filepath.Join(dir, "ftc.json")
	ftc := enroll.Generate("eco", "t.example.com", 443)
	ftc.CA = testCA
	b, _ := json.Marshal(ftc)
	if err := os.WriteFile(ftcPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(Options{DataDir: dir, ProxyURL: proxy.URL, EnrollFile: ftcPath, Tokens: &fakeOwner{}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.store.st.Vehicles = []teslaVehicle{{VIN: "VINS"}, {VIN: "VINY"}}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, form(http.MethodPost, "/enroll", nil))
	return notice(t, rr.Body.String())
}

// notice is the one-shot message at the top of the page.
func notice(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, "<strong>")
	if !ok {
		t.Fatalf("no notice on the page")
	}
	n, _, _ := strings.Cut(rest, "</strong>")
	return n
}

func TestEnrollReportsSkippedCars(t *testing.T) {
	body := enrollAnswer(t, `{"response":{"updated_vehicles":1,"skipped_vehicles":{"missing_key":["VINY"],"unsupported_hardware":[],"unsupported_firmware":[],"max_configs":[]}}}`)
	if !strings.Contains(body, "VINY") || !strings.Contains(body, "missing_key") {
		t.Errorf("the skipped car and its reason are not shown: %.400s", body)
	}
	if !strings.Contains(body, "1 car") {
		t.Errorf("the updated count is not shown: %.400s", body)
	}
}

func TestEnrollAllSkippedIsAFailure(t *testing.T) {
	body := enrollAnswer(t, `{"response":{"updated_vehicles":0,"skipped_vehicles":{"missing_key":["VINS","VINY"]}}}`)
	if strings.Contains(body, "✓") {
		t.Errorf("an enrollment that updated no car reads as success: %.400s", body)
	}
}

func TestEnrollAllUpdated(t *testing.T) {
	body := enrollAnswer(t, `{"response":{"updated_vehicles":2,"skipped_vehicles":{"missing_key":[]}}}`)
	if !strings.Contains(body, "✓") || !strings.Contains(body, "2 car") || strings.Contains(body, "skipped") {
		t.Errorf("unexpected message: %.400s", body)
	}
}

// An accepted answer in a shape we do not know is shown as it came.
func TestEnrollUnknownAnswerIsShown(t *testing.T) {
	body := enrollAnswer(t, `{"response":{"result":true}}`)
	if !strings.Contains(body, "✓") || !strings.Contains(body, "result") {
		t.Errorf("unexpected message: %.400s", body)
	}
}
