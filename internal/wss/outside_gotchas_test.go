package wss

import (
	"strings"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// teslamate-org/teslamate#5583 on the streaming path: TeslaMate starts a drive
// from a stream frame, and a frame without the odometer leaves the drive with
// no start_km. Frames are rendered from the store's merged state, so a
// speed-only update must still carry the odometer, state of charge and range.
func TestGotcha5583_StreamFrameKeepsMergedState(t *testing.T) {
	vin := "5YJ3TESTVIN005583"
	st := store.New(vin)
	st.SetField(vin, store.FieldOdometer, float64(12345.6))
	st.SetField(vin, store.FieldSoc, float64(64))
	st.SetField(vin, store.FieldRatedRange, float64(200))
	st.SetField(vin, store.FieldGear, "P")
	st.SetConnectivity(vin, "online")
	st.SetField(vin, store.FieldVehicleSpeed, float64(12))
	st.SetField(vin, store.FieldGear, "D")

	now := time.Now()
	snap, _ := st.Snapshot(vin)
	d := store.Derive(snap, config.State{OnlineGraceSeconds: 300, StaleAfterSeconds: 660}, now)
	units := config.Units{RangeInput: "mi", SpeedInput: "mph", OdometerInput: "mi"}
	parts := strings.Split(buildCSV(snap, d, units, now), ",")
	if len(parts) != 13 {
		t.Fatalf("expected 13 fields, got %d", len(parts))
	}
	if parts[2] != "12345.60" {
		t.Errorf("odometer = %q, want 12345.60 from the merged state", parts[2])
	}
	if parts[3] != "64" {
		t.Errorf("soc = %q, want 64", parts[3])
	}
	if parts[9] != "D" {
		t.Errorf("shift_state = %q, want D", parts[9])
	}
	if parts[10] != "200" {
		t.Errorf("range = %q, want 200 (mi) from the merged state", parts[10])
	}
}
