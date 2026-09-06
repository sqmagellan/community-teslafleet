package wss

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// /api/1/vehicles lists cars discovered from the stream, but the streaming
// server built its lookup map once, from the CONFIGURED vehicles. A zero-config
// deployment therefore advertised a car over HTTP and then answered its
// subscription with "unknown vehicle" — TeslaMate never got near-real-time
// drive detection.
func TestLookupTag_ResolvesAutoDiscoveredVehicle(t *testing.T) {
	const vin = "5YJYGDEF5LF001234"
	st := store.New() // no allow-list: auto-register whatever streams
	st.SetField(vin, store.FieldSoc, float64(61))

	s := NewServer(st, &config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	want := config.AutoVehicle(vin)

	for _, tag := range []string{vin, want.IDString(), strconv.FormatInt(want.VehicleID, 10)} {
		got, ok := s.lookupTag(tag)
		if !ok {
			t.Errorf("lookupTag(%q) = unknown vehicle", tag)
			continue
		}
		if got.VIN != vin {
			t.Errorf("lookupTag(%q) resolved to %s, want %s", tag, got.VIN, vin)
		}
	}
	if _, ok := s.lookupTag("not-a-vehicle"); ok {
		t.Error("lookupTag accepted an unknown tag")
	}
}

// The soc column must be the DISPLAYED battery level, matching the HTTP
// vehicle_data document for the same instant. Reading Soc and truncating gave
// TeslaMate two different battery levels for one moment (59 over the socket,
// 60 over HTTP) — measured on a real car.
func TestBuildCSV_SOCMatchesHTTP(t *testing.T) {
	snap := store.Snapshot{VIN: "V", Fields: map[string]store.FieldValue{
		store.FieldSoc:          {Value: 59.221},
		store.FieldBatteryLevel: {Value: 59.553},
	}}
	csv := buildCSV(snap, store.Derived{State: "online"}, config.Units{}, time.Unix(0, 0))
	if got := strings.Split(csv, ",")[3]; got != "60" {
		t.Errorf("soc column = %q, want 60", got)
	}
}

// TeslaMate is a server-side client and sends no Origin. A browser always does,
// and a cross-origin one must not be able to open this socket and read live
// vehicle position.
func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		origin string
		want   bool
	}{
		{"", true},
		{"http://gateway.local:4460", true},
		{"https://evil.example", false},
		{"::not a url", false},
	}
	for _, tc := range tests {
		r := httptest.NewRequest(http.MethodGet, "http://gateway.local:4460/streaming/", nil)
		r.Host = "gateway.local:4460"
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if got := sameOriginOrNone(r); got != tc.want {
			t.Errorf("origin %q = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// The soc column and the HTTP document must agree, and so must the clock. f[0]
// is what TeslaMate stores as the position's date, so it has to be the moment
// the telemetry was observed -- not the moment the frame was sent. A stalled
// feed used to write one position per second at the last known coordinates,
// each claiming to be a fresh fix.
func TestBuildCSV_TimeIsObservationTime(t *testing.T) {
	obs := time.Unix(1_800_000_000, 0).UTC()
	snap := store.Snapshot{VIN: "V", LastV: obs, Fields: map[string]store.FieldValue{
		store.FieldGear: {Value: "D"},
	}}
	d := store.Derived{State: "online", Driving: true}

	first := strings.Split(buildCSV(snap, d, config.Units{}, obs.Add(time.Second)), ",")[0]
	second := strings.Split(buildCSV(snap, d, config.Units{}, obs.Add(time.Minute)), ",")[0]

	if want := strconv.FormatInt(obs.UnixMilli(), 10); first != want {
		t.Errorf("time = %s, want the observation %s", first, want)
	}
	if first != second {
		t.Errorf("time advanced without new telemetry: %s then %s", first, second)
	}
}

func TestBuildCSV_NoTelemetryFallsBackToNow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	snap := store.Snapshot{VIN: "V", Fields: map[string]store.FieldValue{}}
	got := strings.Split(buildCSV(snap, store.Derived{}, config.Units{}, now), ",")[0]
	if want := strconv.FormatInt(now.UnixMilli(), 10); got != want {
		t.Errorf("time = %s, want %s", got, want)
	}
}
