package fleetapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

const dbgVIN = "5YJYGDEF5LF001234"

func debugServer(t *testing.T, dbg config.Debug) *Server {
	t.Helper()
	st := store.New(dbgVIN)
	st.SetField(dbgVIN, store.FieldSoc, float64(61))
	cfg := &config.Config{
		Debug:    dbg,
		Vehicles: []config.Vehicle{{VIN: dbgVIN, ID: 1, VehicleID: 2}},
	}
	return NewServer(st, cfg, nil, nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func getDebug(t *testing.T, srv *Server, header, value string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// Default off. /debug/state dumps precise GPS, the active route's destination and
// the odometer for a named vehicle, so it must not be reachable unless asked for.
func TestDebugStateDisabledByDefault(t *testing.T) {
	srv := debugServer(t, config.Debug{})
	rec := getDebug(t, srv, "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when disabled", rec.Code)
	}
	// 404, not 403: a 403 confirms the endpoint exists.
	if rec.Code == http.StatusForbidden {
		t.Error("a disabled endpoint must not advertise itself with 403")
	}
	if body := rec.Body.String(); len(body) > 0 && rec.Code == http.StatusOK {
		t.Errorf("body served while disabled: %q", body)
	}
}

func TestDebugStateEnabledWithoutToken(t *testing.T) {
	srv := debugServer(t, config.Debug{StateEnabled: true})
	rec := getDebug(t, srv, "", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when enabled with no token configured", rec.Code)
	}
}

func TestDebugStateTokenRequired(t *testing.T) {
	srv := debugServer(t, config.Debug{StateEnabled: true, Token: "sekrit"})

	cases := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{"no token", "", "", http.StatusForbidden},
		{"wrong token", "X-Debug-Token", "nope", http.StatusForbidden},
		{"empty token header", "X-Debug-Token", "", http.StatusForbidden},
		{"prefix of the real token", "X-Debug-Token", "sek", http.StatusForbidden},
		{"correct X-Debug-Token", "X-Debug-Token", "sekrit", http.StatusOK},
		{"correct bearer", "Authorization", "Bearer sekrit", http.StatusOK},
		{"bearer without the scheme", "Authorization", "sekrit", http.StatusOK},
		{"wrong bearer", "Authorization", "Bearer nope", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := getDebug(t, srv, tc.header, tc.value)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want != http.StatusOK && rec.Body.Len() > 0 {
				if got := rec.Body.String(); len(got) > 0 && containsVIN(got) {
					t.Errorf("rejected request still leaked state: %q", got)
				}
			}
		})
	}
}

// The token must NOT be accepted from the query string: a query parameter is
// written into this server's own request log and into any proxy's log.
func TestDebugStateTokenNotAcceptedInQuery(t *testing.T) {
	srv := debugServer(t, config.Debug{StateEnabled: true, Token: "sekrit"})
	req := httptest.NewRequest(http.MethodGet, "/debug/state?token=sekrit", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: a query-string token must not authenticate", rec.Code)
	}
}

func containsVIN(s string) bool {
	for i := 0; i+len(dbgVIN) <= len(s); i++ {
		if s[i:i+len(dbgVIN)] == dbgVIN {
			return true
		}
	}
	return false
}
