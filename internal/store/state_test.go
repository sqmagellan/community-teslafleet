package store

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

func TestDerive_State(t *testing.T) {
	cfg := config.State{OnlineGraceSeconds: 60, StaleAfterSeconds: 660, ReportAsleepWhenIdle: true}
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name     string
		mutate   func(s *Snapshot)
		wantSt   string
		wantDriv bool
		wantChg  bool
	}{
		{
			name: "online via fresh telemetry",
			mutate: func(s *Snapshot) {
				s.Connectivity = "online"
				s.LastV = now.Add(-5 * time.Second)
			},
			wantSt: "online",
		},
		{
			name: "driving forces online",
			mutate: func(s *Snapshot) {
				s.Connectivity = "offline"
				s.LastV = now.Add(-2 * time.Hour) // stale
				s.Fields[FieldGear] = FieldValue{Value: "D"}
			},
			wantSt:   "online",
			wantDriv: true,
		},
		{
			name: "charging forces online",
			mutate: func(s *Snapshot) {
				s.Connectivity = "offline"
				s.LastV = now.Add(-2 * time.Hour)
				s.Fields[FieldACChargingPower] = FieldValue{Value: float64(11)}
			},
			wantSt:  "online",
			wantChg: true,
		},
		{
			name: "asleep when idle (stale, not offline)",
			mutate: func(s *Snapshot) {
				s.Connectivity = "online"
				s.LastV = now.Add(-2 * time.Hour) // > stale
			},
			wantSt: "asleep",
		},
		{
			name: "offline when connectivity offline and stale",
			mutate: func(s *Snapshot) {
				s.Connectivity = "offline"
				s.LastV = now.Add(-2 * time.Hour)
			},
			// ReportAsleepWhenIdle requires Connectivity != "offline", so this falls
			// through to the offline branch.
			wantSt: "offline",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{VIN: "V", Fields: map[string]FieldValue{}}
			tc.mutate(&s)
			d := Derive(s, cfg, now)
			if d.State != tc.wantSt {
				t.Errorf("State = %q, want %q", d.State, tc.wantSt)
			}
			if d.Driving != tc.wantDriv {
				t.Errorf("Driving = %v, want %v", d.Driving, tc.wantDriv)
			}
			if d.Charging != tc.wantChg {
				t.Errorf("Charging = %v, want %v", d.Charging, tc.wantChg)
			}
		})
	}
}

func TestGearString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"P", "P"},
		{"d", "D"},
		{" R ", "R"},
		{"N", "N"},
		{"ShiftStateD", "D"},
		{"DRIVESTATER", "R"},
		{"1", "P"},
		{"2", "R"},
		{"3", "N"},
		{"4", "D"},
		{float64(4), "D"},
		{"", ""},
		{"X", ""},
		{"9", ""},
	}
	for _, tc := range tests {
		if got := GearString(tc.in); got != tc.want {
			t.Errorf("GearString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIsDriving_IgnoresResidualSpeed guards the fix for the car showing
// "driving"/non-zero speed while parked: Tesla stops streaming VehicleSpeed at
// a small non-zero residual and never sends 0, so isDriving must rely on Gear
// (which streams P on park), not speed.
func TestIsDriving_IgnoresResidualSpeed(t *testing.T) {
	cases := []struct {
		name  string
		gear  string
		speed float64
		want  bool
	}{
		{"parked with stale residual speed", "ShiftStateP", 0.621, false},
		{"driving", "ShiftStateD", 30, true},
		{"reverse", "ShiftStateR", 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New("VIN")
			s.SetField("VIN", FieldGear, tc.gear)
			s.SetField("VIN", FieldVehicleSpeed, tc.speed)
			snap, _ := s.Snapshot("VIN")
			if got := isDriving(snap); got != tc.want {
				t.Errorf("isDriving(gear=%s, speed=%v) = %v, want %v", tc.gear, tc.speed, got, tc.want)
			}
		})
	}
}
