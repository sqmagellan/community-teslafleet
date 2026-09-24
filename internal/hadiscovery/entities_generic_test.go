package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func TestHumanize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"DiStatorTempF", "Di Stator Temp F"},
		{"ACChargingPower", "AC Charging Power"},
		{"Soc", "Soc"},
		{"VehicleSpeed", "Vehicle Speed"},
		{"TpmsPressureFl", "Tpms Pressure Fl"},
		{"DCChargingEnergyIn", "DC Charging Energy In"},
		{"Experimental_5", "Experimental 5"},
		{"BMSState", "BMS State"},
		{"Setting24HourTime", "Setting 24 Hour Time"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := humanize(tc.in); got != tc.want {
			t.Errorf("humanize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGenericClassUnit(t *testing.T) {
	tests := []struct {
		in     string
		wantDC string
		wantU  string
	}{
		{"DiStatorTempF", "temperature", "°C"},
		{"ChargerVoltage", "voltage", "V"},
		{"DiVBatF", "voltage", "V"},
		{"DiMotorCurrentF", "current", "A"},
		{"ACChargingPower", "power", "kW"},
		{"TpmsPressureFl", "pressure", "bar"},
		// Distance/speed are curated+converted, so the generic heuristic stays unitless.
		{"RatedRange", "", ""},
		{"Odometer", "", ""},
		{"VehicleSpeed", "", ""},
		{"SentryMode", "", ""},
	}
	for _, tc := range tests {
		dc, u := genericClassUnit(tc.in)
		if dc != tc.wantDC || u != tc.wantU {
			t.Errorf("genericClassUnit(%q) = (%q,%q), want (%q,%q)", tc.in, dc, u, tc.wantDC, tc.wantU)
		}
	}
}

func TestNormalizeEnum(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ShiftStateP", "P"},
		{"DisplayStateOff", "Off"},
		{"BMSStateDrive", "Drive"},
		{"WindowStateClosed", "Closed"},
		{"2026.14.6", "2026.14.6"},
		{"It's a Beautiful Day", "It's a Beautiful Day"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeEnum(tc.in); got != tc.want {
			t.Errorf("normalizeEnum(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildState_IncludesRawFields(t *testing.T) {
	vin := "VINRAW"
	st := store.New(vin)
	// A curated field (BatteryLevel) and several uncurated raw fields.
	st.SetField(vin, store.FieldBatteryLevel, float64(72))
	st.SetField(vin, "DiStatorTempF", float64(42))
	st.SetField(vin, "BMSState", "BMSStateStandby")
	st.SetField(vin, store.FieldLocation, map[string]any{"latitude": 1.0, "longitude": 2.0})

	snap, _ := st.Snapshot(vin)
	s := buildState(snap, store.Derived{State: "online"}, config.Units{System: "metric"})

	// Curated key preserved (rounded to a float).
	if s["battery_level"] != float64(72) {
		t.Errorf("battery_level = %v, want 72", s["battery_level"])
	}
	// Raw numeric field present under its telemetry name.
	if s["DiStatorTempF"] != float64(42) {
		t.Errorf("raw DiStatorTempF = %v, want 42", s["DiStatorTempF"])
	}
	// Raw enum string is normalized (Tesla prefix stripped).
	if s["BMSState"] != "Standby" {
		t.Errorf("raw BMSState = %v, want Standby", s["BMSState"])
	}
	// Location must NOT be dumped as a raw map (exposed as lat/lng attributes).
	if _, ok := s[store.FieldLocation]; ok {
		t.Errorf("Location should not appear as a raw field key")
	}
	if s["latitude"] != 1.0 || s["longitude"] != 2.0 {
		t.Errorf("location attributes missing: lat=%v lng=%v", s["latitude"], s["longitude"])
	}
}

func TestBuildState_UnitSystem(t *testing.T) {
	vin := "VINUNIT"
	st := store.New(vin)
	st.SetField(vin, store.FieldRatedRange, float64(100)) // 100 mi
	st.SetField(vin, store.FieldInsideTemp, float64(20))  // 20 °C
	snap, _ := st.Snapshot(vin)

	metric := buildState(snap, store.Derived{State: "online"}, config.Units{System: "metric", RangeInput: "mi"})
	if metric["battery_range"] != float64(161) { // 100 mi -> 160.9 -> round0 161
		t.Errorf("metric battery_range = %v, want 161", metric["battery_range"])
	}
	if metric["inside_temp"] != float64(20) {
		t.Errorf("metric inside_temp = %v, want 20", metric["inside_temp"])
	}

	imp := buildState(snap, store.Derived{State: "online"}, config.Units{System: "imperial", RangeInput: "mi"})
	if imp["battery_range"] != float64(100) {
		t.Errorf("imperial battery_range = %v, want 100", imp["battery_range"])
	}
	if imp["inside_temp"] != float64(68) { // 20 °C -> 68 °F
		t.Errorf("imperial inside_temp = %v, want 68", imp["inside_temp"])
	}
}

func TestGenericDiscoveryConfig(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VINGEN", DisplayName: "Car"}
	dev := map[string]any{}
	origin := map[string]any{}

	component, c := p.genericDiscoveryConfig(v, "DiStatorTempF", float64(42), dev, origin)
	if component != "sensor" {
		t.Errorf("component = %q, want sensor", component)
	}
	if c["name"] != "Di Stator Temp F" {
		t.Errorf("name = %v, want humanized", c["name"])
	}
	// Default identifier mode is "name" → slug-based id (no VIN), e.g. "car_*".
	if c["unique_id"] != "car_raw_DiStatorTempF" {
		t.Errorf("unique_id = %v, want car_raw_DiStatorTempF", c["unique_id"])
	}
	if c["object_id"] != "car_DiStatorTempF" {
		t.Errorf("object_id = %v, want car_DiStatorTempF", c["object_id"])
	}
	if c["device_class"] != "temperature" || c["unit_of_measurement"] != "°C" {
		t.Errorf("heuristics not applied: %v %v", c["device_class"], c["unit_of_measurement"])
	}
	if c["entity_category"] != "diagnostic" {
		t.Errorf("generic entity should be diagnostic, got %v", c["entity_category"])
	}
	tpl, _ := c["value_template"].(string)
	if tpl != "{{ value_json.DiStatorTempF | default(none) }}" {
		t.Errorf("value_template = %q", tpl)
	}

	// Bool field becomes a binary_sensor.
	bc, bcfg := p.genericDiscoveryConfig(v, "ServiceMode", true, dev, origin)
	if bc != "binary_sensor" {
		t.Errorf("bool field component = %q, want binary_sensor", bc)
	}
	if bcfg["payload_on"] != "true" {
		t.Errorf("binary payload_on = %v", bcfg["payload_on"])
	}
}

func TestIdentifierMode(t *testing.T) {
	v := config.Vehicle{VIN: "5YJTESTVIN0000001", DisplayName: "Tesla - Test"}
	dev := map[string]any{}
	origin := map[string]any{}

	// Default ("name") → slug-based id, VIN only as serial_number.
	pName := &Publisher{cfg: &config.Config{HA: config.HA{StateTopicBase: "tgw"}}}
	if got := pName.pubID(v); got != "tesla_test" {
		t.Errorf("name mode pubID = %q, want tesla_test", got)
	}
	cfg := pName.discoveryConfig(v, entity{Component: "sensor", Key: "battery_level", Precision: unset}, dev, origin)
	if cfg["unique_id"] != "tesla_test_battery_level" {
		t.Errorf("name mode unique_id = %v", cfg["unique_id"])
	}
	if cfg["state_topic"] != "tgw/tesla_test/state" {
		t.Errorf("name mode state_topic = %v", cfg["state_topic"])
	}

	// "vin" mode → VIN-based id.
	pVin := &Publisher{cfg: &config.Config{HA: config.HA{StateTopicBase: "tgw", IdentifierMode: "vin"}}}
	if got := pVin.pubID(v); got != v.VIN {
		t.Errorf("vin mode pubID = %q, want %q", got, v.VIN)
	}

	// VIN must be the device serial_number in both modes.
	pName.store = nil
	di := (&Publisher{cfg: pName.cfg}).deviceInfo(v)
	if di["serial_number"] != v.VIN {
		t.Errorf("serial_number = %v, want VIN", di["serial_number"])
	}
}

func TestCoveredFieldsSkipped(t *testing.T) {
	// Curated/derived fields must be in the skip set so generic discovery doesn't dup them.
	covered := coveredFields()
	for _, f := range []string{
		store.FieldSoc, store.FieldBatteryLevel, store.FieldRatedRange, store.FieldVehicleSpeed,
		store.FieldGear, store.FieldOdometer, store.FieldInsideTemp,
		store.FieldOutsideTemp, store.FieldLocked, store.FieldSentryMode,
		store.FieldChargerVoltage, store.FieldTpmsFL, store.FieldVersion,
		store.FieldLocation, store.FieldDoorState, store.FieldWindowFd,
	} {
		if !covered[f] {
			t.Errorf("coveredFields missing %q", f)
		}
	}
}
