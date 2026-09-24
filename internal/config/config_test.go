package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyEnv_VINsParsing(t *testing.T) {
	t.Setenv("TGW_VINS", " VIN1 , VIN2 ,, VIN3 ")
	t.Setenv("TGW_TEMPLATE_DIR", "/tmpl/")

	c := Defaults()
	applyEnv(&c)

	if len(c.Vehicles) != 3 {
		t.Fatalf("got %d vehicles, want 3: %+v", len(c.Vehicles), c.Vehicles)
	}
	wantVINs := []string{"VIN1", "VIN2", "VIN3"}
	for i, w := range wantVINs {
		if c.Vehicles[i].VIN != w {
			t.Errorf("vehicle[%d].VIN = %q, want %q", i, c.Vehicles[i].VIN, w)
		}
		if want := "/tmpl/" + w + ".json"; c.Vehicles[i].Template != want {
			t.Errorf("vehicle[%d].Template = %q, want %q", i, c.Vehicles[i].Template, want)
		}
	}
}

func TestDeriveID_Stable(t *testing.T) {
	vin := "5YJ3TESTVIN000001"
	a := deriveID(vin)
	b := deriveID(vin)
	if a != b {
		t.Errorf("deriveID not stable: %d != %d", a, b)
	}
	if a <= 0 {
		t.Errorf("deriveID = %d, want positive", a)
	}
	if other := deriveID("DIFFERENTVIN00001"); other == a {
		t.Errorf("deriveID collision for distinct VINs: %d", a)
	}
}

func TestVehicle_IDString(t *testing.T) {
	v := Vehicle{ID: 123456}
	if got := v.IDString(); got != "123456" {
		t.Errorf("IDString = %q, want 123456", got)
	}
}

func TestVehiclesByKey(t *testing.T) {
	cfg := &Config{Vehicles: []Vehicle{{VIN: "VINA", ID: 11, VehicleID: 22}}}
	m := VehiclesByKey(cfg)
	for _, key := range []string{"VINA", "11", "22"} {
		if _, ok := m[key]; !ok {
			t.Errorf("byKey missing key %q", key)
		}
	}
}

// validate must not mutate — a config loaded for editing keeps ids unset and the
// display name empty (so the VIN never gets baked in).
func TestValidate_NoMutation(t *testing.T) {
	c := Config{
		Ingest:   Ingest{ZMQAddr: "tcp://x:1", Namespace: "ns"},
		HA:       HA{Enabled: false},
		Vehicles: []Vehicle{{VIN: "5YJTESTVIN0000001"}},
	}
	if err := c.validate(); err != nil {
		t.Fatalf("validate error: %v", err)
	}
	v := c.Vehicles[0]
	if v.ID != 0 || v.VehicleID != 0 || v.DisplayName != "" {
		t.Errorf("validate mutated vehicle: %+v", v)
	}
}

// resolve derives runtime fields; only the runtime config carries them.
func TestResolve_Derives(t *testing.T) {
	c := Config{Vehicles: []Vehicle{{VIN: "5YJTESTVIN0000001"}}}
	c.resolve()
	v := c.Vehicles[0]
	if v.ID == 0 || v.VehicleID != v.ID || v.DisplayName != v.VIN {
		t.Errorf("resolve did not derive runtime fields: %+v", v)
	}
}

// Save writes RAW config — no derived ids, no VIN-as-display-name leaks into the file.
func TestSave_NoDerivedLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := Defaults()
	raw.Ingest = Ingest{ZMQAddr: "tcp://x:1", Namespace: "ns"}
	raw.HA.Enabled = false
	raw.Vehicles = []Vehicle{{VIN: "5YJTESTVIN0000001"}} // no DisplayName/ID

	if err := Save(path, raw); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, _ := os.ReadFile(path)
	body := string(b)
	if strings.Contains(body, "5YJTESTVIN0000001\n  display_name") || strings.Contains(body, "display_name: 5YJTESTVIN0000001") {
		t.Errorf("VIN leaked as display_name in saved file:\n%s", body)
	}
	if !strings.Contains(body, "schema_version: 1") {
		t.Errorf("schema_version not stamped:\n%s", body)
	}
	// Reload raw → still no derivation.
	got, err := LoadRaw(path)
	if err != nil {
		t.Fatalf("loadraw: %v", err)
	}
	if got.Vehicles[0].DisplayName != "" || got.Vehicles[0].ID != 0 {
		t.Errorf("LoadRaw should not derive: %+v", got.Vehicles[0])
	}
}

// HA add-on options.json overlays onto config (between file and env).
func TestApplyOptions(t *testing.T) {
	dir := t.TempDir()
	opts := filepath.Join(dir, "options.json")
	os.WriteFile(opts, []byte(`{"units_system":"imperial","device_identifier":"vin","vins":"VINX, VINY"}`), 0o600)
	t.Setenv("TGW_OPTIONS_FILE", opts)

	c := Defaults()
	applyOptions(&c)
	if c.Units.System != "imperial" {
		t.Errorf("units_system = %q, want imperial", c.Units.System)
	}
	if c.HA.IdentifierMode != "vin" {
		t.Errorf("device_identifier = %q, want vin", c.HA.IdentifierMode)
	}
	if len(c.Vehicles) != 2 || c.Vehicles[1].VIN != "VINY" {
		t.Errorf("vins not parsed: %+v", c.Vehicles)
	}
}

func TestRedactAndMergeSecrets(t *testing.T) {
	c := Defaults()
	c.Commands.ClientSecret = "supersecret"
	c.HA.Password = "pw"
	c.Onboard.Password = "wizard-pw"
	c.Debug.Token = "debug-tok"
	r := c.Redact()
	if r.Commands.ClientSecret != secretMask || r.HA.Password != secretMask {
		t.Errorf("Redact did not mask: %+v", r.Commands)
	}
	if r.Onboard.Password != secretMask || r.Debug.Token != secretMask {
		t.Errorf("Redact left the wizard password or debug token visible: %q %q", r.Onboard.Password, r.Debug.Token)
	}
	if c.Commands.ClientSecret != "supersecret" {
		t.Errorf("Redact mutated original")
	}
	// UI sends back the masked value → must keep the existing secret.
	incoming := r
	MergeSecrets(&incoming, c)
	if incoming.Commands.ClientSecret != "supersecret" || incoming.HA.Password != "pw" {
		t.Errorf("MergeSecrets did not restore: %+v", incoming.Commands)
	}
	// A genuine change must overwrite.
	incoming2 := c.Redact()
	incoming2.HA.Password = "newpw"
	MergeSecrets(&incoming2, c)
	if incoming2.HA.Password != "newpw" {
		t.Errorf("MergeSecrets clobbered a real change: %q", incoming2.HA.Password)
	}
}
