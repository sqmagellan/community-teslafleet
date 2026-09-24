package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// The last process saw the car driving and charging. The car then fell asleep
// while the gateway was down, so it never sends Gear P or the end of the
// session. Restored values alone must not make it driving, charging or online.
func TestRestoredDrivingOrChargingDoesNotPinTheCar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	src := New("VIN1")
	src.SetField("VIN1", FieldGear, "ShiftStateD")
	src.SetField("VIN1", FieldChargeState, "DetailedChargeStateCharging")
	if err := src.Save(path); err != nil {
		t.Fatal(err)
	}

	dst := New("VIN1")
	if _, err := dst.Load(path); err != nil {
		t.Fatal(err)
	}
	cfg := config.State{StaleAfterSeconds: 660, OnlineGraceSeconds: 300}
	snap, _ := dst.Snapshot("VIN1")
	d := Derive(snap, cfg, time.Now().Add(time.Hour))
	if d.Driving || d.Charging || d.State == "online" {
		t.Errorf("restored state derived as %+v, want not driving, not charging, not online", d)
	}
	if _, ok := snap.Field(FieldGear); ok {
		t.Error("a restored Gear D is still visible to HA and TeslaMate")
	}

	// Once the car sends anything, its fields are live again.
	dst.SetField("VIN1", FieldBatteryLevel, float64(70))
	snap, _ = dst.Snapshot("VIN1")
	if snap.Restored {
		t.Error("still marked restored after a live update")
	}
	if d := Derive(snap, cfg, time.Now()); !d.Charging {
		t.Errorf("live DetailedChargeState Charging not charging: %+v", d)
	}
}

func TestRestoredParkIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	src := New("VIN1")
	src.SetField("VIN1", FieldGear, "ShiftStateP")
	if err := src.Save(path); err != nil {
		t.Fatal(err)
	}
	dst := New("VIN1")
	if _, err := dst.Load(path); err != nil {
		t.Fatal(err)
	}
	snap, _ := dst.Snapshot("VIN1")
	if g, ok := snap.Field(FieldGear); !ok || GearString(g.Value) != "P" {
		t.Errorf("restored Gear P lost: %+v %v", g, ok)
	}
}

func TestInvalidFieldIsAbsent(t *testing.T) {
	s := New("VIN1")
	s.SetField("VIN1", FieldGear, "ShiftStateD")
	s.SetField("VIN1", FieldGear, nil)
	snap, _ := s.Snapshot("VIN1")
	if _, ok := snap.Field(FieldGear); ok {
		t.Error("Field reports a value the car marked invalid")
	}
	if isDriving(snap) {
		t.Error("an invalid Gear still reads as driving")
	}
}
