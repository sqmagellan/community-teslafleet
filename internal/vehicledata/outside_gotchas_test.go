package vehicledata

// Regression tests for failures other TeslaMate-feeding projects hit, so this
// gateway can't drift into them (sweep of 2026-11-19):
//
//   - teslamate-org/teslamate#5583: TeslaMate builds a record from one payload,
//     so a payload missing the odometer starts a drive with no start_km and a
//     charge row is rejected for want of a field. The gateway must answer every
//     request from the MERGED last-known state, never from the latest frame.
//   - kevinkinnett/teslamate-nopoll (MIT) README: TeslaMate silently drops
//     records unless some fields are integers, and telemetry configured in miles
//     must not be converted again.

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func gotchaBuild(t *testing.T, st *store.Store, vin string, units config.Units, now time.Time) map[string]any {
	t.Helper()
	snap, _ := st.Snapshot(vin)
	d := store.Derive(snap, config.State{OnlineGraceSeconds: 300, StaleAfterSeconds: 660}, now)
	tmpl, err := LoadTemplate("")
	if err != nil {
		t.Fatal(err)
	}
	return Build(snap, d, config.Vehicle{VIN: vin, ID: 1, VehicleID: 1, DisplayName: "T"}, tmpl, units, now)
}

// teslamate#5583: a frame carrying only speed and gear must not erase the
// odometer, state of charge or range that earlier frames delivered.
func TestGotcha5583_PartialFrameKeepsMergedState(t *testing.T) {
	vin := "5YJ3TESTVIN005583"
	st := store.New(vin)
	st.SetField(vin, store.FieldOdometer, float64(12345.6))
	st.SetField(vin, store.FieldSoc, float64(64))
	st.SetField(vin, store.FieldRatedRange, float64(200))
	st.SetField(vin, store.FieldGear, "P")
	st.SetConnectivity(vin, "online")

	// Later: a frame with only speed and gear (the shape that starts a drive).
	st.SetField(vin, store.FieldVehicleSpeed, float64(12))
	st.SetField(vin, store.FieldGear, "D")

	units := config.Units{RangeInput: "mi", SpeedInput: "mph", OdometerInput: "mi"}
	resp := gotchaBuild(t, st, vin, units, time.Now())

	vs := resp["vehicle_state"].(map[string]any)
	if o, ok := vs["odometer"].(float64); !ok || o != 12345.6 {
		t.Errorf("odometer after a speed-only frame = %v, want 12345.6 from the merged state", vs["odometer"])
	}
	cs := resp["charge_state"].(map[string]any)
	if cs["battery_level"] != 64 {
		t.Errorf("battery_level after a speed-only frame = %v, want 64", cs["battery_level"])
	}
	if r, ok := cs["battery_range"].(float64); !ok || r != 200 {
		t.Errorf("battery_range after a speed-only frame = %v, want 200", cs["battery_range"])
	}
}

// teslamate-nopoll: TeslaMate casts these to integers and drops the record when
// one arrives as a fraction. Checked on the wire (JSON), where it matters.
func TestGotchaNopoll_IntegerFieldsOnTheWire(t *testing.T) {
	vin := "5YJ3TESTVIN000INT"
	st := store.New(vin)
	st.SetField(vin, store.FieldSoc, float64(59.221))
	st.SetField(vin, store.FieldBatteryLevel, float64(59.553))
	st.SetField(vin, store.FieldChargeLimitSoc, float64(80))
	st.SetField(vin, store.FieldGpsHeading, float64(181.7))
	st.SetField(vin, store.FieldVehicleSpeed, float64(37.4))
	st.SetField(vin, store.FieldGear, "D")
	st.SetConnectivity(vin, "online")

	units := config.Units{RangeInput: "mi", SpeedInput: "mph", OdometerInput: "mi"}
	b, err := json.Marshal(gotchaBuild(t, st, vin, units, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"battery_level", "usable_battery_level", "charge_limit_soc",
		"heading", "speed", "power",
	} {
		re := regexp.MustCompile(`"` + field + `":(-?[0-9.eE+]+)`)
		m := re.FindSubmatch(b)
		if m == nil {
			t.Errorf("%s missing from the response (or not a bare number)", field)
			continue
		}
		if regexp.MustCompile(`[.eE]`).Match(m[1]) {
			t.Errorf("%s = %s on the wire, want an integer", field, m[1])
		}
	}
}

// teslamate-nopoll: telemetry configured in miles is already in miles.
func TestGotchaNopoll_MilesAreNotConvertedTwice(t *testing.T) {
	for _, in := range []string{"mi", "MI"} {
		if got := RangeToMiles(100, in); got != 100 {
			t.Errorf("RangeToMiles(100, %q) = %v, want 100", in, got)
		}
	}
	if got := RangeToMiles(100, "km"); got < 62.1 || got > 62.2 {
		t.Errorf("RangeToMiles(100, km) = %v, want ~62.14", got)
	}
}
