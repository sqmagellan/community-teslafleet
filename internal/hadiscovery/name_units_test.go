package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// One acronym is always written one way, whatever casing Tesla used.
func TestFieldNameAcronyms(t *testing.T) {
	for field, want := range map[string]string{
		"BmsFullchargecomplete":      "BMS fullchargecomplete",
		"BMSState":                   "BMS state",
		"HvacACEnabled":              "HVAC AC enabled",
		"GpsState":                   "GPS state",
		"TpmsLastSeenPressureTimeFl": "TPMS last seen pressure time fl",
		"DiAxleSpeedF":               "DI axle speed F",
		"DCDCEnable":                 "DCDC enable",
		"Hvil":                       "HVIL",
	} {
		if got := fieldName(field); got != want {
			t.Errorf("fieldName(%s) = %q, want %q", field, got, want)
		}
	}
	// Keys do not change.
	if got := fieldKey("BmsFullchargecomplete"); got != "bms_fullchargecomplete" {
		t.Errorf("fieldKey = %q", got)
	}
}

// The unit word in a field name decides its class and unit, before the other
// heuristics. The gateway never converts; HA converts speed and distance to
// the house unit system itself.
func TestUnitWordDecides(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "V", DisplayName: "Car"}
	for field, want := range map[string][2]string{
		"CurrentLimitMph":            {"speed", "mph"},
		"MilesSinceReset":            {"distance", "mi"},
		"SelfDrivingMilesSinceReset": {"distance", "mi"},
	} {
		_, c := p.genericDiscoveryConfig(v, field, float64(10), map[string]any{}, map[string]any{})
		if c["device_class"] != want[0] || c["unit_of_measurement"] != want[1] {
			t.Errorf("%s: class %v unit %v, want %s %s", field, c["device_class"], c["unit_of_measurement"], want[0], want[1])
		}
	}
}
