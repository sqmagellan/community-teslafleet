package hadiscovery

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// TestBuildState_ShiftStateFollowsDriving: during a drive found only by the
// speed backstop, Gear still reads P. The sensor must report D, as TeslaMate
// is told, and fall back to the car's own gear once parked.
func TestBuildState_ShiftStateFollowsDriving(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}
	s := store.New("VIN")
	s.SetClock(func() time.Time { return now.Add(-30 * time.Second) })
	s.SetField("VIN", store.FieldGear, "ShiftStateP")
	s.SetClock(func() time.Time { return now.Add(-5 * time.Second) })
	s.SetField("VIN", store.FieldVehicleSpeed, 40.0)
	snap, _ := s.Snapshot("VIN")

	d := store.Derive(snap, cfg, now)
	if !d.Driving {
		t.Fatalf("Derive = %+v, want driving", d)
	}
	if got := buildState(snap, d, config.Units{})["shift_state"]; got != "D" {
		t.Errorf("backstop drive shift_state = %v, want D", got)
	}

	s.SetClock(func() time.Time { return now })
	s.SetField("VIN", store.FieldGear, "ShiftStateP")
	snap, _ = s.Snapshot("VIN")
	if got := buildState(snap, store.Derive(snap, cfg, now), config.Units{})["shift_state"]; got != "P" {
		t.Errorf("parked shift_state = %v, want P", got)
	}
}
