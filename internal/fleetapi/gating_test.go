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

const vinA, vinB = "5YJYGDEF5LF001234", "5YJYGDEF6LF001234"

// Freshness is per vehicle. LastIngest() is fleet-wide, so with two cars the
// one still streaming kept the timestamp fresh and /healthz stayed 200 through
// the other one's stall — the exact failure the probe exists to catch.
func TestHealthz_PerVehicleStall(t *testing.T) {
	st := store.New(vinA, vinB)
	clock := time.Unix(1_800_000_000, 0).UTC()
	st.SetClock(func() time.Time { return clock })
	cfg := &config.Config{
		State: config.State{StaleAfterSeconds: 660},
		Vehicles: []config.Vehicle{
			{VIN: vinA, ID: 1, VehicleID: 11},
			{VIN: vinB, ID: 2, VehicleID: 22},
		},
	}
	srv := NewServer(st, cfg, nil, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	st.SetConnectivity(vinA, "online")
	st.SetConnectivity(vinB, "online")
	st.SetField(vinA, store.FieldSoc, float64(61))
	st.SetField(vinB, store.FieldSoc, float64(72))

	// A stalls; B keeps streaming.
	clock = clock.Add(20 * time.Minute)
	st.SetField(vinB, store.FieldSoc, float64(73))

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("healthz body is not JSON (%q): %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503 (%s)", rec.Code, h.Reason)
	}
	if h.VehiclesStale != 1 {
		t.Errorf("vehicles_stale = %d, want 1", h.VehiclesStale)
	}
}

// /admin/enroll makes a signed, billable Tesla call and rewrites the vehicle's
// telemetry configuration. It sits on the auth-less TeslaMate router, so it has
// to carry the debug gate: anything that reaches port 4460 could otherwise
// trigger it in a loop.
func TestAdminEnroll_Gated(t *testing.T) {
	st := store.New(vinA)
	base := &config.Config{Vehicles: []config.Vehicle{{VIN: vinA, ID: 1, VehicleID: 11}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("404 when debug is disabled", func(t *testing.T) {
		srv := NewServer(st, base, nil, nil, "", log)
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/enroll", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("code = %d, want 404", rec.Code)
		}
	})

	t.Run("403 when debug is on but no token is configured", func(t *testing.T) {
		cfg := *base
		cfg.Debug = config.Debug{StateEnabled: true}
		srv := NewServer(st, &cfg, nil, nil, "", log)
		for _, path := range []string{"/admin/enroll", "/debug/upstream/" + vinA} {
			method := http.MethodGet
			if path == "/admin/enroll" {
				method = http.MethodPost
			}
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: code = %d, want 403", path, rec.Code)
			}
		}
	})

	t.Run("403 without the token", func(t *testing.T) {
		cfg := *base
		cfg.Debug = config.Debug{StateEnabled: true, Token: "s3cret"}
		srv := NewServer(st, &cfg, nil, nil, "", log)
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/enroll", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("code = %d, want 403", rec.Code)
		}
	})
}
