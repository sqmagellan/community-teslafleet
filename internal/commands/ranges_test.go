package commands

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func TestNumberCommandsEnforceRanges(t *testing.T) {
	tests := []struct {
		key, payload, want string
	}{
		{"charge_limit", "80", "set_charge_limit"},
		{"charge_limit", "50", "set_charge_limit"},
		{"charge_limit", "100", "set_charge_limit"},
		{"charge_limit", "49", ""},
		{"charge_limit", "101", ""},
		{"charge_limit", "-1", ""},
		{"charging_amps", "32", "set_charging_amps"},
		{"charging_amps", "48", "set_charging_amps"},
		{"charging_amps", "49", ""},
		{"charging_amps", "-5", ""},
		{"climate_temp", "21.5", "set_temps"},
		{"climate_temp", "14", ""},
		{"climate_temp", "70", ""}, // a Fahrenheit value sent to a °C entity
		{"speed_limit_value", "65", "speed_limit_set_limit"},
		{"speed_limit_value", "120", ""},
	}
	for _, tc := range tests {
		t.Run(tc.key+"="+tc.payload, func(t *testing.T) {
			r, sent := relayTo(t)
			res := r.Handle("VIN1", tc.key, tc.payload)
			if got := sent(); got != tc.want {
				t.Fatalf("sent %q, want %q (%+v)", got, tc.want, res)
			}
			if tc.want == "" && (res.Outcome != OutcomeRejected || res.Reason == "") {
				t.Fatalf("refusal reported as %+v", res)
			}
		})
	}
}

// A car that reports its own maximum current is held to it, in both
// directions: a 32 A car refuses 40, a car reporting 48 is not capped lower.
func TestChargingAmpsUsesCarReportedMax(t *testing.T) {
	snapWith := func(max float64) fakeLocator {
		return fakeLocator{"VIN1": {Fields: map[string]store.FieldValue{
			fieldChargeCurrentRequestMax: {Value: max},
		}}}
	}
	r, sent := relayTo(t)
	r.store = snapWith(32)
	if res := r.Handle("VIN1", "charging_amps", "40"); sent() != "" {
		t.Fatalf("40 A sent to a car reporting 32 A max (%+v)", res)
	}
	if rg, _ := r.NumberRange("VIN1", "charging_amps"); rg.Max != 32 {
		t.Fatalf("range max %v, want 32", rg.Max)
	}
	r2, sent2 := relayTo(t)
	r2.store = snapWith(48)
	r2.Handle("VIN1", "charging_amps", "48")
	if sent2() != "set_charging_amps" {
		t.Fatal("48 A refused for a car reporting 48 A")
	}
}

// vehicle_data carries charge_current_request_max and the charge-limit span;
// the relay keeps them from any document it fetched.
func TestRangesFromVehicleData(t *testing.T) {
	r, _ := relayTo(t)
	r.limits.recordLimits("VIN1", map[string]any{"charge_state": map[string]any{
		"charge_current_request_max": 40.0,
		"charge_limit_soc_min":       55.0,
		"charge_limit_soc_max":       100.0,
	}})
	if rg, _ := r.NumberRange("VIN1", "charging_amps"); rg.Max != 40 {
		t.Fatalf("amps max %v, want 40", rg.Max)
	}
	if rg, _ := r.NumberRange("VIN1", "charge_limit"); rg.Min != 55 || rg.Max != 100 {
		t.Fatalf("charge limit range %+v, want 55..100", rg)
	}
	// Telemetry wins over a cached document.
	r.store = fakeLocator{"VIN1": {Fields: map[string]store.FieldValue{fieldChargeCurrentRequestMax: {Value: 32.0}}}}
	if rg, _ := r.NumberRange("VIN1", "charging_amps"); rg.Max != 32 {
		t.Fatalf("amps max %v, want telemetry's 32", rg.Max)
	}
	// Another car keeps the static range.
	if rg, _ := r.NumberRange("VIN2", "charging_amps"); rg.Max != 48 {
		t.Fatalf("VIN2 amps max %v, want static 48", rg.Max)
	}
}
