package wss

import (
	"strings"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func TestBuildCSV_OrderAndUnits(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := store.Snapshot{
		VIN: "V",
		Fields: map[string]store.FieldValue{
			store.FieldVehicleSpeed:    {Value: float64(100)},   // km/h -> ~62 mph
			store.FieldOdometer:        {Value: float64(10000)}, // km -> ~6213.71 mi
			store.FieldSoc:             {Value: float64(72)},
			store.FieldGpsHeading:      {Value: float64(180)},
			store.FieldLocation:        {Value: map[string]any{"latitude": 55.123456, "longitude": 12.345678}},
			store.FieldGear:            {Value: "D"},
			store.FieldRatedRange:      {Value: float64(300)}, // km -> ~186 mi
			store.FieldEstBatteryRange: {Value: float64(280)}, // km -> ~174 mi
			store.FieldACChargingPower: {Value: float64(0)},
		},
	}
	d := store.Derived{State: "online", Driving: true}
	units := config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}

	csv := buildCSV(snap, d, units, now)
	parts := strings.Split(csv, ",")
	if len(parts) != 13 {
		t.Fatalf("expected 13 fields, got %d: %q", len(parts), csv)
	}

	// field order: time,speed,odometer,soc,elevation,est_heading,est_lat,est_lng,power,shift_state,range,est_range,heading
	checks := map[int]string{
		0:  "1700000000000", // time (ms)
		1:  "62",            // speed mph (100km/h ≈ 62.1)
		3:  "72",            // soc
		4:  "",              // elevation (empty)
		5:  "180",           // est_heading
		6:  "55.123456",     // lat
		7:  "12.345678",     // lng
		8:  "0",             // power
		9:  "D",             // shift_state
		10: "186",           // rated range mi
		11: "174",           // est range mi
		12: "180",           // heading mirrors est_heading
	}
	for idx, want := range checks {
		if parts[idx] != want {
			t.Errorf("field[%d] = %q, want %q (full=%q)", idx, parts[idx], want, csv)
		}
	}
	if !strings.HasPrefix(parts[2], "6213.7") {
		t.Errorf("field[2] odometer = %q, want ~6213.7x", parts[2])
	}
}

// TestBuildCSV_SpeedBackstopSendsD: the stream must carry D too, or TeslaMate,
// suspended after a P poll, never wakes up for the drive.
func TestBuildCSV_SpeedBackstopSendsD(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := store.New("V")
	st.SetClock(func() time.Time { return now.Add(-60 * time.Second) })
	st.SetField("V", store.FieldGear, "ShiftStateP")
	st.SetClock(func() time.Time { return now.Add(-2 * time.Second) })
	st.SetField("V", store.FieldVehicleSpeed, float64(50))
	snap, _ := st.Snapshot("V")
	d := store.Derive(snap, config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}, now)
	parts := strings.Split(buildCSV(snap, d, config.Units{RangeInput: "km", SpeedInput: "kmh", OdometerInput: "km"}, now), ",")
	if parts[9] != "D" || parts[1] != "31" {
		t.Errorf("shift_state=%q speed=%q, want D and 31", parts[9], parts[1])
	}
}
