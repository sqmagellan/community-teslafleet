package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testRelay(fleetURL string) *Relay {
	return &Relay{
		tm:        &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
		client:    http.DefaultClient,
		apiClient: http.DefaultClient,
		fleetAPI:  fleetURL,
		log:       testLogger(),
	}
}

func TestVehicleDataUnwrapsResponse(t *testing.T) {
	var gotPath, gotAuth, gotEndpoints string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotEndpoints = r.URL.Query().Get("endpoints")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"charge_state":{"battery_level":60,"usable_battery_level":59}}}`))
	}))
	defer srv.Close()

	doc, err := testRelay(srv.URL).VehicleData("5YJ3TESTVIN000001")
	if err != nil {
		t.Fatalf("VehicleData: %v", err)
	}
	if gotPath != "/api/1/vehicles/5YJ3TESTVIN000001/vehicle_data" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("authorization = %q", gotAuth)
	}
	// Every requested section must survive the round trip. Sent with literal
	// semicolons, Tesla reads only the first entry and answers with charge_state
	// alone -- which looks like a car reporting nothing rather than a bad request,
	// so it has to be asserted here.
	for _, want := range []string{"charge_state", "climate_state", "vehicle_config", "vehicle_state", "gui_settings", "drive_state"} {
		if !strings.Contains(gotEndpoints, want) {
			t.Errorf("endpoints missing %s: %q", want, gotEndpoints)
		}
	}
	// location_data must not be requested: telemetry supplies GPS for free.
	if strings.Contains(gotEndpoints, "location_data") {
		t.Errorf("endpoints requested location_data: %q", gotEndpoints)
	}
	cs, ok := doc["charge_state"].(map[string]any)
	if !ok {
		t.Fatalf("charge_state missing from %v", doc)
	}
	if cs["usable_battery_level"] != float64(59) {
		t.Errorf("usable_battery_level = %v", cs["usable_battery_level"])
	}
}

// A sleeping car answers 408. That must surface as an error, never as an empty
// document a caller might merge into the emulation.
func TestVehicleDataSleepingCarIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
		_, _ = w.Write([]byte(`{"error":"vehicle unavailable"}`))
	}))
	defer srv.Close()

	doc, err := testRelay(srv.URL).VehicleData("5YJ3TESTVIN000001")
	if err == nil {
		t.Fatalf("want an error for 408, got document %v", doc)
	}
	if !strings.Contains(err.Error(), "408") {
		t.Errorf("error should name the status: %v", err)
	}
}
