package store

import (
	"path/filepath"
	"testing"
)

// TestSaveLoadRoundTrip is the core guarantee: last-known values survive a
// restart. A fresh store (no telemetry streamed yet, as when the car is
// asleep/away) serves the restored battery/charge-limit/plugged-in/location
// exactly as if freshly streamed.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	src := New("VIN1")
	src.SetField("VIN1", FieldBatteryLevel, float64(54))
	src.SetField("VIN1", FieldChargeLimitSoc, float64(80))
	src.SetField("VIN1", FieldChargePortLatch, "ChargePortLatchEngaged")
	src.SetField("VIN1", FieldLocation, map[string]any{"latitude": 56.09, "longitude": 10.04})
	src.SetConnectivity("VIN1", "offline")

	if err := src.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	dst := New("VIN1") // simulates a restart: empty store
	n, err := dst.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n != 1 {
		t.Fatalf("restored %d vehicles, want 1", n)
	}

	snap, ok := dst.Snapshot("VIN1")
	if !ok {
		t.Fatal("no snapshot after load")
	}
	if v, _ := snap.Num(FieldBatteryLevel); v != 54 {
		t.Errorf("battery_level = %v, want 54", v)
	}
	if v, _ := snap.Num(FieldChargeLimitSoc); v != 80 {
		t.Errorf("charge_limit = %v, want 80", v)
	}
	if s := snap.Str(FieldChargePortLatch); s != "ChargePortLatchEngaged" {
		t.Errorf("latch = %q, want ChargePortLatchEngaged", s)
	}
	if la, lo, ok := snap.LocationField(FieldLocation); !ok || la != 56.09 || lo != 10.04 {
		t.Errorf("location = %v,%v ok=%v, want 56.09,10.04 true", la, lo, ok)
	}
	if snap.Connectivity != "offline" {
		t.Errorf("connectivity = %q, want offline", snap.Connectivity)
	}
}

func TestLoadRespectsAllowList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	src := New() // no allow-list → auto-registers any VIN
	src.SetField("VINX", FieldBatteryLevel, float64(30))
	if err := src.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	dst := New("VIN1") // allow-list excludes VINX
	n, err := dst.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n != 0 {
		t.Errorf("restored %d vehicles, want 0 (VINX excluded)", n)
	}
	if _, ok := dst.Snapshot("VINX"); ok {
		t.Error("VINX should not have been restored")
	}
}

func TestLoadMissingFileIsNoError(t *testing.T) {
	dst := New("VIN1")
	n, err := dst.Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil || n != 0 {
		t.Errorf("missing file: n=%d err=%v, want 0 nil", n, err)
	}
}

func TestSaveLoadEmptyPathNoOp(t *testing.T) {
	if err := New("VIN1").Save(""); err != nil {
		t.Errorf("Save(\"\") = %v, want nil", err)
	}
	n, err := New("VIN1").Load("")
	if err != nil || n != 0 {
		t.Errorf("Load(\"\") = %d,%v, want 0,nil", n, err)
	}
}
