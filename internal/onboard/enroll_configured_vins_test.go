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

// Step 7 enrolled every car on the account. A gateway configured for some of
// them paid for streams from the others and dropped them. With vehicles
// configured, it enrolls those that are on the account; with none, all.

func enrollWith(t *testing.T, configured []string, listed ...string) ([]string, string) {
	t.Helper()
	var sent []byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"response":{"updated_vehicles":1}}`))
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
	s, err := NewServer(Options{DataDir: dir, ProxyURL: proxy.URL, EnrollFile: ftcPath,
		Tokens: &fakeOwner{}, VINs: configured}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range listed {
		s.store.st.Vehicles = append(s.store.st.Vehicles, teslaVehicle{VIN: v})
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, form(http.MethodPost, "/enroll", nil))
	if sent == nil {
		return nil, rr.Body.String()
	}
	var env struct {
		VINs []string `json:"vins"`
	}
	if err := json.Unmarshal(sent, &env); err != nil {
		t.Fatal(err)
	}
	return env.VINs, rr.Body.String()
}

func TestEnrollOnlyConfiguredVehicles(t *testing.T) {
	got, _ := enrollWith(t, []string{"VINS"}, "VINS", "VINY")
	if len(got) != 1 || got[0] != "VINS" {
		t.Errorf("enrolled %v, want [VINS]", got)
	}
}

func TestEnrollAllWhenNoneConfigured(t *testing.T) {
	got, _ := enrollWith(t, nil, "VINS", "VINY")
	if len(got) != 2 {
		t.Errorf("enrolled %v, want both cars", got)
	}
}

func TestEnrollRefusesWhenNoConfiguredCarIsListed(t *testing.T) {
	got, body := enrollWith(t, []string{"VINX"}, "VINS")
	if got != nil {
		t.Errorf("enrolled %v, want nothing", got)
	}
	if !strings.Contains(body, "VINX") {
		t.Errorf("the refusal does not name the configured car: %.300s", body)
	}
}

func TestStep7ShowsWhichCarsEnroll(t *testing.T) {
	s, err := NewServer(Options{DataDir: t.TempDir(), VINs: []string{"VINS"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.store.st.Vehicles = []teslaVehicle{{VIN: "VINS", DisplayName: "S"}, {VIN: "VINY", DisplayName: "Y"}}
	rr := httptest.NewRecorder()
	s.page(rr, loopbackReq(http.MethodGet, "/", nil))
	page := rr.Body.String()
	step7 := page[strings.Index(page, "7 · Enroll"):]
	if !strings.Contains(step7, "VINS") || strings.Contains(step7, "VINY") {
		t.Errorf("step 7 should list VINS only: %.400s", step7)
	}
}
