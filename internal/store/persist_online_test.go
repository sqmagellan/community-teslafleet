package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// "online" describes the PREVIOUS process's live link, and Derive trusts it
// unconditionally. Restoring it across a restart pins a car that fell asleep
// during the downtime permanently online: a sleeping car sends nothing, so no
// event can ever clear the flag. "offline" is still restored — that one is
// safe and is what keeps a parked car from flapping to online on boot.
func TestLoad_DoesNotRestoreOnline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	src := New("VIN1")
	src.SetField("VIN1", FieldBatteryLevel, float64(54))
	src.SetConnectivity("VIN1", "online")
	if err := src.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	dst := New("VIN1")
	if _, err := dst.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	snap, _ := dst.Snapshot("VIN1")
	if snap.Connectivity == "online" {
		t.Fatal("restored connectivity online across a restart")
	}
	if v, _ := snap.Num(FieldBatteryLevel); v != 54 {
		t.Errorf("field values must still survive: battery = %v, want 54", v)
	}

	// The whole point: a long-quiet restored vehicle must not derive as online.
	cfg := config.State{StaleAfterSeconds: 660, OnlineGraceSeconds: 300}
	d := Derive(snap, cfg, time.Now().Add(24*time.Hour))
	if d.State == "online" {
		t.Errorf("Derive = online for a vehicle silent for a day")
	}
}
