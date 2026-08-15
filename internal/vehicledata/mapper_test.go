package vehicledata

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func TestBuild_OverlayAndUnits(t *testing.T) {
	vin := "5YJ3TESTVIN000001"
	st := store.New(vin)
	st.SetField(vin, store.FieldSoc, float64(72))
	st.SetField(vin, store.FieldRatedRange, float64(300))   // km
	st.SetField(vin, store.FieldEstBatteryRange, float64(280)) // km
	st.SetField(vin, store.FieldOdometer, float64(10000))   // km
	st.SetField(vin, store.FieldLocation, map[string]any{"latitude": 55.12, "longitude": 12.34})
	st.SetField(vin, store.FieldGear, "P")
	st.SetConnectivity(vin, "online")

	snap, _ := st.Snapshot(vin)
	now := time.Now()
	d := store.Derive(snap, config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}, now)
	if d.State != "online" {
		t.Fatalf("state = %q, want online", d.State)
	}

	veh := config.Vehicle{VIN: vin, ID: 123, VehicleID: 123, DisplayName: "Test"}
	tmpl, _ := LoadTemplate("")
	units := config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}
	resp := Build(snap, d, veh, tmpl, units, now)

	if resp["vin"] != vin {
		t.Errorf("vin = %v", resp["vin"])
	}
	if resp["state"] != "online" {
		t.Errorf("state = %v", resp["state"])
	}

	cs := resp["charge_state"].(map[string]any)
	if cs["battery_level"].(int) != 72 {
		t.Errorf("battery_level = %v, want 72", cs["battery_level"])
	}
	if r := cs["battery_range"].(float64); r < 186 || r > 187 { // 300km ≈ 186.4mi
		t.Errorf("battery_range = %v, want ~186.4 mi", r)
	}
	if r := cs["est_battery_range"].(float64); r < 173 || r > 175 { // 280km ≈ 174mi
		t.Errorf("est_battery_range = %v, want ~174 mi", r)
	}

	vs := resp["vehicle_state"].(map[string]any)
	if o := vs["odometer"].(float64); o < 6212 || o > 6215 { // 10000km ≈ 6213.7mi
		t.Errorf("odometer = %v, want ~6213.7 mi", o)
	}

	ds := resp["drive_state"].(map[string]any)
	if ds["latitude"].(float64) != 55.12 || ds["longitude"].(float64) != 12.34 {
		t.Errorf("location = %v,%v", ds["latitude"], ds["longitude"])
	}
	// parked → speed null
	if ds["speed"] != nil {
		t.Errorf("speed = %v, want nil (parked)", ds["speed"])
	}
}

func TestBuild_MphInput(t *testing.T) {
	vin := "MPHVIN"
	st := store.New(vin)
	st.SetField(vin, store.FieldRatedRange, float64(200)) // already miles
	snap, _ := st.Snapshot(vin)
	now := time.Now()
	d := store.Derive(snap, config.State{}, now)
	veh := config.Vehicle{VIN: vin, ID: 1, VehicleID: 1}
	tmpl, _ := LoadTemplate("")
	resp := Build(snap, d, veh, tmpl, config.Units{RangeInput: "mi"}, now)
	cs := resp["charge_state"].(map[string]any)
	if r := cs["battery_range"].(float64); r != 200 {
		t.Errorf("battery_range = %v, want 200 (mi passthrough)", r)
	}
}

// TestBuild_ChargeEnergyAdded_DC covers the DC fast-charging regression: the
// mapper read only ACChargingEnergyIn, so a Supercharger session mapped to
// charge_energy_added = 0.00 while the car was plainly taking DC energy.
func TestBuild_ChargeEnergyAdded_DC(t *testing.T) {
	cases := []struct {
		name string
		ac   any
		dc   any
		want float64
	}{
		{name: "dc only (supercharger)", ac: float64(0), dc: float64(15.2), want: 15.2},
		{name: "ac only (home)", ac: float64(7.4), dc: float64(0), want: 7.4},
		{name: "dc absent", ac: float64(3.5), dc: nil, want: 3.5},
		{name: "ac absent", ac: nil, dc: float64(9.1), want: 9.1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vin := "5YJ3TESTVIN000002"
			st := store.New(vin)
			st.SetField(vin, store.FieldSoc, float64(50))
			if tc.ac != nil {
				st.SetField(vin, store.FieldChargeEnergyIn, tc.ac)
			}
			if tc.dc != nil {
				st.SetField(vin, store.FieldDCChargingEnergyIn, tc.dc)
			}
			st.SetConnectivity(vin, "online")

			snap, _ := st.Snapshot(vin)
			now := time.Now()
			d := store.Derive(snap, config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}, now)
			veh := config.Vehicle{VIN: vin, ID: 1, VehicleID: 1, DisplayName: "Test"}
			tmpl, _ := LoadTemplate("")
			units := config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}
			resp := Build(snap, d, veh, tmpl, units, now)

			cs := resp["charge_state"].(map[string]any)
			got, ok := cs["charge_energy_added"].(float64)
			if !ok {
				t.Fatalf("charge_energy_added missing or not float64: %#v", cs["charge_energy_added"])
			}
			if got != tc.want {
				t.Errorf("charge_energy_added = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestChargeEnergyAdded_AbsentBoth asserts the helper reports not-found when the
// car has published neither counter, so the mapper omits the key entirely.
func TestChargeEnergyAdded_AbsentBoth(t *testing.T) {
	vin := "5YJ3TESTVIN000003"
	st := store.New(vin)
	st.SetField(vin, store.FieldSoc, float64(50))
	snap, _ := st.Snapshot(vin)
	if v, ok := snap.ChargeEnergyAdded(); ok {
		t.Errorf("ChargeEnergyAdded() = (%v, true), want (_, false)", v)
	}
}
