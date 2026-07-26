package vehicledata

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// testVIN is synthetic (valid check digit, not a real car).
const testVIN = "5YJ3TESTVIN000001"

func buildWith(t *testing.T, fields map[string]any) map[string]any {
	t.Helper()
	st := store.New(testVIN)
	for k, v := range fields {
		st.SetField(testVIN, k, v)
	}
	snap, _ := st.Snapshot(testVIN)
	now := time.Now()
	tmpl, err := LoadTemplate("")
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	d := store.Derive(snap, config.State{}, now)
	veh := config.Vehicle{VIN: testVIN, ID: 1, VehicleID: 1}
	return Build(snap, d, veh, tmpl, config.Units{RangeInput: "mi"}, now)
}

// The two SOC fields must land on the two different API fields, and
// usable_battery_level must never exceed battery_level whichever way the two
// telemetry fields happen to be ordered.
func TestBatteryLevelsUseBothSocFields(t *testing.T) {
	cases := []struct {
		name       string
		fields     map[string]any
		wantLevel  any
		wantUsable any
	}{
		{
			// The real values measured against a live car's Fleet API response: the API answered 60/59, so
			// rounding (not truncation) is what reproduces ground truth.
			name:       "measured against the real API: 59.221/59.553 -> 59/60",
			fields:     map[string]any{store.FieldSoc: 59.221, store.FieldBatteryLevel: 59.553},
			wantLevel:  60,
			wantUsable: 59,
		},
		{
			// A second live car, the same day: the API answered 70/70.
			name:       "measured against the real API: 69.541/69.851 -> 70/70",
			fields:     map[string]any{store.FieldSoc: 69.541, store.FieldBatteryLevel: 69.851},
			wantLevel:  70,
			wantUsable: 70,
		},
		{
			name:       "reversed ordering still cannot exceed displayed",
			fields:     map[string]any{store.FieldSoc: 61.9, store.FieldBatteryLevel: 60.2},
			wantLevel:  60,
			wantUsable: 60,
		},
		{
			name:       "only Soc: used for both",
			fields:     map[string]any{store.FieldSoc: 72.0},
			wantLevel:  72,
			wantUsable: 72,
		},
		{
			name:       "only BatteryLevel: used for both, rounded",
			fields:     map[string]any{store.FieldBatteryLevel: 40.7},
			wantLevel:  41,
			wantUsable: 41,
		},
		{
			name:       "neither: keys left absent, not zeroed",
			fields:     map[string]any{},
			wantLevel:  nil,
			wantUsable: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := buildWith(t, tc.fields)["charge_state"].(map[string]any)
			if got := cs["battery_level"]; got != tc.wantLevel {
				t.Errorf("battery_level = %v, want %v", got, tc.wantLevel)
			}
			if got := cs["usable_battery_level"]; got != tc.wantUsable {
				t.Errorf("usable_battery_level = %v, want %v", got, tc.wantUsable)
			}
			lvl, okL := cs["battery_level"].(int)
			usable, okU := cs["usable_battery_level"].(int)
			if okL && okU && usable > lvl {
				t.Errorf("usable_battery_level %d > battery_level %d", usable, lvl)
			}
		})
	}
}

func TestIsClimateOn(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  any // nil = key must be absent
	}{
		{"on", "HvacPowerStateOn", true},
		{"off", "HvacPowerStateOff", false},
		{"preconditioning counts as on", "HvacPowerStatePrecondition", true},
		{"unprefixed", "On", true},
		{"bool passthrough", true, true},
		{"unknown enum omits the key", "HvacPowerStateUnknown", nil},
		{"garbage omits the key", "wat", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := buildWith(t, map[string]any{store.FieldIsClimateOn: tc.value})["climate_state"].(map[string]any)
			if got := cls["is_climate_on"]; got != tc.want {
				t.Errorf("is_climate_on = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsClimateOnAbsentWithoutTelemetry(t *testing.T) {
	cls := buildWith(t, map[string]any{})["climate_state"].(map[string]any)
	if v, ok := cls["is_climate_on"]; ok {
		t.Errorf("is_climate_on = %v, want absent when HvacPower never streamed", v)
	}
}
