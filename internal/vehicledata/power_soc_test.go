package vehicledata

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// A vehicle discovered from the stream has no configured template, so the
// per-VIN map hands Build a nil *Template. That used to dereference and panic
// the vehicle_data handler — the first request for any car nobody configured
// took the HTTP server's goroutine down with it.
func TestBuild_NilTemplateUsesSkeleton(t *testing.T) {
	veh := config.AutoVehicle("5YJYGDEF5LF001234")
	snap := store.Snapshot{Fields: map[string]store.FieldValue{}}
	resp := Build(snap, store.Derived{State: "asleep"}, veh, nil, config.Units{}, time.Now())

	if resp["vin"] != veh.VIN {
		t.Fatalf("vin = %v, want %s", resp["vin"], veh.VIN)
	}
	for _, section := range []string{"drive_state", "charge_state", "climate_state", "vehicle_state"} {
		if _, ok := resp[section].(map[string]any); !ok {
			t.Errorf("missing %s in skeleton response", section)
		}
	}
}

// battery_level (HTTP) and the WebSocket soc column must agree for the same
// instant. Both go through DisplayedSOC, which reads BatteryLevel — the
// DISPLAYED figure — and rounds the way the Fleet API does. Values measured
// from two real cars.
func TestDisplayedSOC_MatchesFleetAPIRounding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		soc   float64
		level float64
		want  int
	}{
		{"rounds up", 59.221, 59.553, 60},
		{"rounds up again", 69.4, 69.851, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := store.Snapshot{Fields: map[string]store.FieldValue{
				store.FieldSoc:          {Value: tc.soc},
				store.FieldBatteryLevel: {Value: tc.level},
			}}
			got, ok := DisplayedSOC(snap)
			if !ok || got != tc.want {
				t.Errorf("DisplayedSOC = %d (%v), want %d", got, ok, tc.want)
			}
		})
	}
}

// Pack power is only reported when both halves are present. PackVoltage and
// PackCurrent have to be enrolled in the telemetry field set; without them the
// mapping must stay silent rather than publish a confident zero.
func TestPackPowerKW(t *testing.T) {
	t.Run("absent fields report nothing", func(t *testing.T) {
		if _, ok := PackPowerKW(store.Snapshot{Fields: map[string]store.FieldValue{}}); ok {
			t.Error("reported power with no pack telemetry")
		}
	})
	t.Run("volts times amps", func(t *testing.T) {
		snap := store.Snapshot{Fields: map[string]store.FieldValue{
			store.FieldPackVoltage: {Value: 400.0},
			store.FieldPackCurrent: {Value: 25.0},
		}}
		kw, ok := PackPowerKW(snap)
		if !ok || kw != 10 {
			t.Errorf("PackPowerKW = %v (%v), want 10", kw, ok)
		}
	})
}

// power is negative while charging and positive while driving, matching the
// Fleet API sign convention TeslaMate stores.
func TestDrivePower_SignConvention(t *testing.T) {
	driving := store.Snapshot{Fields: map[string]store.FieldValue{
		store.FieldPackVoltage: {Value: 400.0},
		store.FieldPackCurrent: {Value: 25.0},
	}}
	if got := drivePower(driving, store.Derived{Driving: true}); got != 10 {
		t.Errorf("driving power = %d, want 10", got)
	}
	// Parked with the same residual telemetry must stay at 0: PackCurrent never
	// reaches a clean zero, so an unguarded mapping trickles forever.
	if got := drivePower(driving, store.Derived{}); got != 0 {
		t.Errorf("parked power = %d, want 0", got)
	}
	charging := store.Snapshot{Fields: map[string]store.FieldValue{
		store.FieldACChargingPower: {Value: 11.0},
	}}
	if got := drivePower(charging, store.Derived{Charging: true}); got != -11 {
		t.Errorf("charging power = %d, want -11", got)
	}
}
