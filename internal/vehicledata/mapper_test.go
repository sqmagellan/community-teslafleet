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

// TestBuild_ChargeEnergyAdded covers the DC fast-charging regression (the mapper
// read only ACChargingEnergyIn, so a Supercharger session mapped to 0.00 kWh) and
// the stale-counter trap that makes max(AC, DC) wrong: DCChargingEnergyIn was
// observed holding 14.34 kWh while the car sat disconnected, so it must not win
// during a later AC charge.
func TestBuild_ChargeEnergyAdded(t *testing.T) {
	cases := []struct {
		name    string
		acPower any
		dcPower any
		acEnrgy any
		dcEnrgy any
		want    float64
		absent  bool
	}{
		{
			name:    "dc charging uses dc counter",
			acPower: float64(0), dcPower: float64(88),
			acEnrgy: float64(0), dcEnrgy: float64(14.3),
			want: 14.3,
		},
		{
			name:    "ac charging ignores stale dc counter",
			acPower: float64(7), dcPower: float64(0),
			acEnrgy: float64(6.2), dcEnrgy: float64(14.34),
			want: 6.2,
		},
		{
			name:    "idle trickle below floor is not dc charging",
			acPower: float64(7), dcPower: float64(0.099),
			acEnrgy: float64(3.1), dcEnrgy: float64(28.92),
			want: 3.1,
		},
		{
			name:    "not charging falls back to ac counter",
			acPower: float64(0), dcPower: float64(0),
			acEnrgy: float64(2.5), dcEnrgy: float64(14.34),
			want: 2.5,
		},
		{
			name:    "no counters at all omits the key",
			acPower: float64(0), dcPower: float64(0),
			acEnrgy: nil, dcEnrgy: nil,
			absent: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vin := "5YJ3TESTVIN000002"
			st := store.New(vin)
			st.SetField(vin, store.FieldSoc, float64(50))
			if tc.acPower != nil {
				st.SetField(vin, store.FieldACChargingPower, tc.acPower)
			}
			if tc.dcPower != nil {
				st.SetField(vin, store.FieldDCChargingPower, tc.dcPower)
			}
			if tc.acEnrgy != nil {
				st.SetField(vin, store.FieldChargeEnergyIn, tc.acEnrgy)
			}
			if tc.dcEnrgy != nil {
				st.SetField(vin, store.FieldDCChargingEnergyIn, tc.dcEnrgy)
			}
			st.SetConnectivity(vin, "online")

			snap, _ := st.Snapshot(vin)
			now := time.Now()
			d := store.Derive(snap, config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}, now)
			veh := config.Vehicle{VIN: vin, ID: 1, VehicleID: 1, DisplayName: "Test"}
			tmpl, _ := LoadTemplate("")
			units := config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}
			cs := Build(snap, d, veh, tmpl, units, now)["charge_state"].(map[string]any)

			got, present := cs["charge_energy_added"]
			if tc.absent {
				if present {
					t.Fatalf("charge_energy_added = %v, want key absent", got)
				}
				return
			}
			if !present {
				t.Fatalf("charge_energy_added missing")
			}
			if g := got.(float64); g != tc.want {
				t.Errorf("charge_energy_added = %v, want %v", g, tc.want)
			}
		})
	}
}

// TestBuild_FastChargerAndCable covers the two fields the mapper never emitted:
// 0 of 2075 charge rows carried fast_charger_present or conn_charge_cable after
// the gateway took over, so a Supercharger stop was indistinguishable from a home
// charge.
func TestBuild_FastChargerAndCable(t *testing.T) {
	cases := []struct {
		name      string
		dcPower   float64
		cable     any
		wantFast  bool
		wantCable string
	}{
		{name: "supercharging", dcPower: 88, cable: "CableTypeSAE", wantFast: true, wantCable: "SAE"},
		{name: "ac charging", dcPower: 0, cable: "CableTypeSAE", wantFast: false, wantCable: "SAE"},
		{name: "idle trickle", dcPower: 0.099, cable: "CableTypeSAE", wantFast: false, wantCable: "SAE"},
		{name: "unprefixed cable passes through", dcPower: 0, cable: "SAE", wantFast: false, wantCable: "SAE"},
		{name: "no cable field", dcPower: 0, cable: nil, wantFast: false, wantCable: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vin := "5YJ3TESTVIN000004"
			st := store.New(vin)
			st.SetField(vin, store.FieldSoc, float64(50))
			st.SetField(vin, store.FieldDCChargingPower, tc.dcPower)
			if tc.cable != nil {
				st.SetField(vin, store.FieldChargingCableType, tc.cable)
			}
			st.SetConnectivity(vin, "online")

			snap, _ := st.Snapshot(vin)
			now := time.Now()
			d := store.Derive(snap, config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}, now)
			veh := config.Vehicle{VIN: vin, ID: 1, VehicleID: 1, DisplayName: "Test"}
			tmpl, _ := LoadTemplate("")
			units := config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}
			cs := Build(snap, d, veh, tmpl, units, now)["charge_state"].(map[string]any)

			if got := cs["fast_charger_present"]; got != tc.wantFast {
				t.Errorf("fast_charger_present = %v, want %v", got, tc.wantFast)
			}
			if tc.wantCable == "" {
				if got, ok := cs["conn_charge_cable"]; ok {
					t.Errorf("conn_charge_cable = %v, want key absent", got)
				}
				return
			}
			if got := cs["conn_charge_cable"]; got != tc.wantCable {
				t.Errorf("conn_charge_cable = %v, want %v", got, tc.wantCable)
			}
		})
	}
}
