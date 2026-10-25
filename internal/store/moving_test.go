package store

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// TestIsDriving_SpeedBackstop guards the 2026-10-25 miss: the car reported
// Gear P, then drove with Gear never leaving P while VehicleSpeed streamed.
// Speed that arrives after the gear reading must count as driving; a P that
// arrives after it (parking) must not, and neither must an old reading.
func TestIsDriving_SpeedBackstop(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	type step struct {
		at    time.Duration
		field string
		value any
	}
	cases := []struct {
		name  string
		steps []step
		check time.Duration
		want  bool
	}{
		{"gear P, then moving: driving", []step{{0, FieldGear, "ShiftStateP"}, {30 * time.Second, FieldVehicleSpeed, 25.0}}, 40 * time.Second, true},
		{"no gear at all, moving: driving", []step{{0, FieldVehicleSpeed, 25.0}}, 10 * time.Second, true},
		{"stopped at a light inside the hold: driving", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 25.0}, {20 * time.Second, FieldVehicleSpeed, 0.0}}, 90 * time.Second, true},
		{"moving, then P (parked): not driving", []step{{0, FieldVehicleSpeed, 25.0}, {30 * time.Second, FieldGear, "P"}}, 40 * time.Second, false},
		{"last moving reading past the hold: not driving", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 25.0}, {20 * time.Second, FieldVehicleSpeed, 0.6}}, 10*time.Second + movingHold + time.Second, false},
		{"parked residual after P: not driving", []step{{0, FieldGear, "P"}, {5 * time.Second, FieldVehicleSpeed, 0.621}}, 10 * time.Second, false},
		{"gear invalid, moving: driving", []step{{0, FieldGear, "P"}, {5 * time.Second, FieldGear, nil}, {10 * time.Second, FieldVehicleSpeed, 25.0}}, 15 * time.Second, true},
		{"steady speed past the hold, still streaming: driving", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 62.0}, {5 * time.Minute, FieldLocation, map[string]any{"latitude": 1.0, "longitude": 2.0}}}, 5*time.Minute + 10*time.Second, true},
		{"steady speed past the hold, stream silent: not driving", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 62.0}}, 10*time.Second + movingHold + time.Second, false},
		{"parked without a new P: driving only until the hold ends", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 25.0}, {20 * time.Second, FieldVehicleSpeed, 0.621}, {3 * time.Minute, FieldLocation, map[string]any{"latitude": 1.0, "longitude": 2.0}}}, 3*time.Minute + 5*time.Second, false},
		{"charging inside the hold: not driving", []step{{0, FieldGear, "P"}, {10 * time.Second, FieldVehicleSpeed, 25.0}, {30 * time.Second, FieldChargeState, "DetailedChargeStateCharging"}}, 40 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New("VIN")
			for _, st := range tc.steps {
				at := t0.Add(st.at)
				s.SetClock(func() time.Time { return at })
				s.SetField("VIN", st.field, st.value)
			}
			snap, _ := s.Snapshot("VIN")
			if got := isDriving(snap, t0.Add(tc.check)); got != tc.want {
				t.Errorf("isDriving = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShiftState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660}
	s := New("VIN")
	s.SetClock(func() time.Time { return now.Add(-30 * time.Second) })
	s.SetField("VIN", FieldGear, "ShiftStateP")
	s.SetClock(func() time.Time { return now.Add(-5 * time.Second) })
	s.SetField("VIN", FieldVehicleSpeed, 40.0)
	snap, _ := s.Snapshot("VIN")
	d := Derive(snap, cfg, now)
	if !d.Driving || d.State != "online" {
		t.Fatalf("Derive = %+v, want driving and online", d)
	}
	if got := ShiftState(snap, d); got != "D" {
		t.Errorf("backstop ShiftState = %q, want D", got)
	}

	// The car's own gear is passed through untouched otherwise.
	s.SetClock(func() time.Time { return now })
	s.SetField("VIN", FieldGear, "ShiftStateR")
	snap, _ = s.Snapshot("VIN")
	if got := ShiftState(snap, Derive(snap, cfg, now)); got != "R" {
		t.Errorf("ShiftState with gear R = %q, want R", got)
	}
	s.SetField("VIN", FieldGear, "ShiftStateP")
	snap, _ = s.Snapshot("VIN")
	d = Derive(snap, cfg, now)
	if d.Driving {
		t.Error("P after the speed reading still reads as driving")
	}
	if got := ShiftState(snap, d); got != "P" {
		t.Errorf("parked ShiftState = %q, want P", got)
	}
}
