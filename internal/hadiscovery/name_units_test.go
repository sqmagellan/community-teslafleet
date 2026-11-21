package hadiscovery

import (
	"strings"
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
		"TpmsLastSeenPressureTimeFl": "TPMS last seen pressure time FL",
		"DiAxleSpeedF":               "DI axle speed F",
		"DCDCEnable":                 "DCDC enable",
		"Hvil":                       "HVIL",
		"TpmsLastSeenPressureTimeRr": "TPMS last seen pressure time RR",
		"DiHeatsinkTF":               "DI heatsink TF",
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

// A "Time" field with an epoch value is a timestamp, before the other
// heuristics: TpmsLastSeenPressureTimeFl is not a pressure.
func TestTimeFieldWithEpochIsTimestamp(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "V", DisplayName: "Car"}
	_, c := p.genericDiscoveryConfig(v, "TpmsLastSeenPressureTimeFl", float64(1795226905), map[string]any{}, map[string]any{})
	if c["device_class"] != "timestamp" {
		t.Errorf("device_class = %v, want timestamp", c["device_class"])
	}
	if _, ok := c["unit_of_measurement"]; ok {
		t.Errorf("a timestamp has no unit, got %v", c["unit_of_measurement"])
	}
	if _, ok := c["state_class"]; ok {
		t.Errorf("a timestamp has no state_class, got %v", c["state_class"])
	}
	tpl, _ := c["value_template"].(string)
	if tpl == "" || !strings.Contains(tpl, "TpmsLastSeenPressureTimeFl") || !strings.Contains(tpl, "timestamp_custom") {
		t.Errorf("value_template = %q", tpl)
	}
	// The car sends these as strings.
	_, c = p.genericDiscoveryConfig(v, "TpmsLastSeenPressureTimeFl", "1795226905", map[string]any{}, map[string]any{})
	if c["device_class"] != "timestamp" {
		t.Errorf("string epoch: device_class = %v, want timestamp", c["device_class"])
	}
	// A Time field that is not an epoch gets no class from the other words.
	_, c = p.genericDiscoveryConfig(v, "TpmsLastSeenPressureTimeFl", float64(120), map[string]any{}, map[string]any{})
	if c["device_class"] != nil || c["unit_of_measurement"] != nil {
		t.Errorf("non-epoch Time field: class %v unit %v, want none", c["device_class"], c["unit_of_measurement"])
	}
}
