package fleetapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

const testVIN = "5YJYGDEF5LF001234"

// fakeIngest stands in for the telemetry consumer so a dead link can be tested
// without a ZMQ socket.
type fakeIngest struct{ up bool }

func (f fakeIngest) Connected() bool { return f.up }

// healthFixture builds a server whose clock is frozen, so "age" is exact rather
// than racing the test.
func healthFixture(t *testing.T, staleAfter int) (*Server, *store.Store, func(time.Duration)) {
	t.Helper()
	st := store.New(testVIN)
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := now
	st.SetClock(func() time.Time { return clock })
	cfg := &config.Config{
		State: config.State{StaleAfterSeconds: staleAfter},
		// /debug/state is opt-in; these tests assert on what it exposes.
		Debug:    config.Debug{StateEnabled: true},
		Vehicles: []config.Vehicle{{VIN: testVIN, ID: 1, VehicleID: 2}},
	}
	srv := NewServer(st, cfg, nil, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return srv, st, func(d time.Duration) { clock = clock.Add(d) }
}

func getHealth(t *testing.T, srv *Server) (int, Health) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("healthz body is not JSON (%q): %v", rec.Body.String(), err)
	}
	return rec.Code, h
}

// A wedged ingest socket is THE failure this endpoint exists for: the publisher
// keeps republishing the in-memory store at full rate, so nothing else about the
// process looks wrong.
func TestHealthzIngestDown(t *testing.T) {
	srv, st, _ := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: false})
	st.SetField(testVIN, store.FieldSoc, float64(61)) // fresh data, dead link

	code, h := getHealth(t, srv)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if h.Status != "degraded" || h.Reason == "" {
		t.Errorf("health = %+v, want degraded with a reason", h)
	}
	if h.IngestConnected == nil || *h.IngestConnected {
		t.Errorf("ingest_connected = %v, want false", h.IngestConnected)
	}
}

func TestHealthzHealthy(t *testing.T) {
	srv, st, _ := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: true})
	st.SetConnectivity(testVIN, "online")
	st.SetField(testVIN, store.FieldSoc, float64(61))

	code, h := getHealth(t, srv)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (reason: %s)", code, h.Reason)
	}
	if h.Status != "ok" || h.Reason != "" {
		t.Errorf("health = %+v, want ok with no reason", h)
	}
	if h.LastIngestUnix == 0 || h.LastIngestAgeS != 0 {
		t.Errorf("last ingest = (%d, %ds), want a timestamp with age 0", h.LastIngestUnix, h.LastIngestAgeS)
	}
	if h.VehiclesOnline != 1 {
		t.Errorf("vehicles_online = %d, want 1", h.VehiclesOnline)
	}
}

// An ONLINE car that has gone silent past the staleness threshold is a stall.
func TestHealthzOnlineButStale(t *testing.T) {
	srv, st, advance := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: true})
	st.SetConnectivity(testVIN, "online")
	st.SetField(testVIN, store.FieldSoc, float64(61))
	advance(20 * time.Minute)

	code, h := getHealth(t, srv)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if h.LastIngestAgeS != 1200 {
		t.Errorf("last_ingest_age_s = %d, want 1200", h.LastIngestAgeS)
	}
}

// The regression that would make this endpoint useless in practice: a sleeping
// fleet streams nothing for hours, and that is correct. Failing overnight would
// train everyone to ignore the probe -- or, wired to a container healthcheck,
// restart a perfectly healthy gateway every night.
func TestHealthzAsleepFleetIsReady(t *testing.T) {
	srv, st, advance := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: true})
	st.SetConnectivity(testVIN, "offline")
	st.SetField(testVIN, store.FieldSoc, float64(61))
	advance(9 * time.Hour)

	code, h := getHealth(t, srv)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an asleep fleet (reason: %s)", code, h.Reason)
	}
	if h.LastIngestAgeS != 32400 {
		t.Errorf("last_ingest_age_s = %d, want 32400 reported anyway", h.LastIngestAgeS)
	}
}

// Cold start with every car asleep: nothing has ever been ingested and nothing
// is expected to be. Reporting unready here would leave the gateway unready
// indefinitely after any restart in the small hours.
func TestHealthzColdStartQuietFleetIsReady(t *testing.T) {
	srv, _, _ := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: true})

	code, h := getHealth(t, srv)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (reason: %s)", code, h.Reason)
	}
	if h.LastIngestUnix != 0 || h.LastIngestAgeS != -1 {
		t.Errorf("last ingest = (%d, %d), want (0, -1) for never", h.LastIngestUnix, h.LastIngestAgeS)
	}
}

// A car reported online with no telemetry ever received is a stall too, and must
// not be excused by the "never ingested" sentinel.
func TestHealthzColdStartWithOnlineCarIsDegraded(t *testing.T) {
	srv, st, _ := healthFixture(t, 660)
	srv.SetIngestHealth(fakeIngest{up: true})
	st.SetConnectivity(testVIN, "online")

	code, h := getHealth(t, srv)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (health: %+v)", code, h)
	}
}

// With no ingest source attached, unknown must stay unknown: null, not false.
// Reporting false would make a HA-only deployment permanently unhealthy.
func TestHealthzUnknownIngestIsNotDown(t *testing.T) {
	srv, st, _ := healthFixture(t, 660)
	st.SetConnectivity(testVIN, "online")
	st.SetField(testVIN, store.FieldSoc, float64(61))

	code, h := getHealth(t, srv)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (reason: %s)", code, h.Reason)
	}
	if h.IngestConnected != nil {
		t.Errorf("ingest_connected = %v, want null when no source is attached", *h.IngestConnected)
	}
}

// /debug/state must carry an explicit per-vehicle timestamp. Diffing successive
// responses does NOT work as a freshness signal: every field carries an age_s
// that ticks on its own, so a whole-object comparison always reports a change
// even for a car that has been silent for hours.
func TestDebugStateExposesLastIngest(t *testing.T) {
	srv, st, advance := healthFixture(t, 660)
	st.SetField(testVIN, store.FieldSoc, float64(61))
	advance(5 * time.Minute)

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/state", nil))
	var out map[string]struct {
		LastIngestUnix int64 `json:"last_ingest_unix"`
		LastIngestAgeS int64 `json:"last_ingest_age_s"`
		FieldCount     int   `json:"field_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("debug/state body: %v", err)
	}
	v, ok := out[testVIN]
	if !ok {
		t.Fatalf("VIN missing from /debug/state: %s", rec.Body.String())
	}
	if v.LastIngestAgeS != 300 {
		t.Errorf("last_ingest_age_s = %d, want 300", v.LastIngestAgeS)
	}
	if v.LastIngestUnix == 0 {
		t.Error("last_ingest_unix = 0, want the ingest timestamp")
	}
	if v.FieldCount != 1 {
		t.Errorf("field_count = %d, want 1", v.FieldCount)
	}
}
