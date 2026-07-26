package vehicledata

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// buildFor is a minimal Build harness: no telemetry, just identity, so the only
// thing under test is how vehicle_config.car_type is decided.
func buildFor(t *testing.T, veh config.Vehicle, templatePath string) map[string]any {
	t.Helper()
	st := store.New(veh.VIN)
	snap, _ := st.Snapshot(veh.VIN)
	now := time.Now()
	d := store.Derive(snap, config.State{}, now)
	tmpl, err := LoadTemplate(templatePath)
	if err != nil && templatePath != "" {
		t.Fatalf("LoadTemplate(%q): %v", templatePath, err)
	}
	return Build(snap, d, veh, tmpl, config.Units{RangeInput: "mi"}, now)
}

func carType(t *testing.T, resp map[string]any) any {
	t.Helper()
	vc, ok := resp["vehicle_config"].(map[string]any)
	if !ok {
		t.Fatalf("vehicle_config missing or wrong type: %#v", resp["vehicle_config"])
	}
	return vc["car_type"]
}

// All VINs here are synthetic but carry valid check digits. See internal/vin.
func TestBuildCarTypeFromVIN(t *testing.T) {
	tests := []struct {
		name string
		veh  config.Vehicle
		want any
	}{
		{
			name: "Model Y derived from VIN",
			veh:  config.Vehicle{VIN: "5YJYGDEF5LF001234", ID: 1, VehicleID: 1},
			want: "modely",
		},
		{
			name: "Model 3 derived from VIN",
			veh:  config.Vehicle{VIN: "5YJ3E1EA2KF301234", ID: 1, VehicleID: 1},
			want: "model3",
		},
		{
			name: "Cybertruck derived from VIN",
			veh:  config.Vehicle{VIN: "7G2CEHED0RA101234", ID: 1, VehicleID: 1},
			want: "cybertruck",
		},
		{
			name: "lower-cased VIN still decodes",
			veh:  config.Vehicle{VIN: "5yjygdef5lf001234", ID: 1, VehicleID: 1},
			want: "modely",
		},
		{
			// An explicit override is for models the decoder does not know; it
			// must beat the VIN even when the VIN decodes cleanly.
			name: "explicit config override wins over VIN",
			veh:  config.Vehicle{VIN: "5YJYGDEF5LF001234", ID: 1, VehicleID: 1, CarType: "semi"},
			want: "semi",
		},
		{
			// The regression that motivated this: an undecodable VIN must leave
			// car_type ABSENT rather than claim "model3".
			name: "undecodable VIN yields no car_type",
			veh:  config.Vehicle{VIN: "5YJ3TESTVIN000001", ID: 1, VehicleID: 1},
			want: nil,
		},
		{
			name: "bad check digit is not trusted",
			veh:  config.Vehicle{VIN: "5YJYGDEF6LF001234", ID: 1, VehicleID: 1},
			want: nil,
		},
		{
			name: "empty VIN yields no car_type",
			veh:  config.Vehicle{VIN: "", ID: 1, VehicleID: 1},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := carType(t, buildFor(t, tt.veh, ""))
			if got != tt.want {
				t.Errorf("car_type = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// A captured template may carry a hand-set car_type. It must still be honoured
// when the VIN cannot be decoded, so upgrading does not regress an operator who
// worked around the old hardcoded default by editing their template.
func TestBuildCarTypeTemplateFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmpl.json")
	body := `{"response":{"vehicle_config":{"car_type":"models","trim_badging":"p90d"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("template used when VIN undecodable", func(t *testing.T) {
		veh := config.Vehicle{VIN: "5YJ3TESTVIN000001", ID: 1, VehicleID: 1}
		if got := carType(t, buildFor(t, veh, path)); got != "models" {
			t.Errorf("car_type = %#v, want models (from template)", got)
		}
	})

	t.Run("VIN corrects a stale template value", func(t *testing.T) {
		// The template says Model S; the VIN says Model Y. The VIN is
		// authoritative -- this is the case where a stale hand-edited template
		// would otherwise keep mislabelling the car forever.
		veh := config.Vehicle{VIN: "5YJYGDEF5LF001234", ID: 1, VehicleID: 1}
		if got := carType(t, buildFor(t, veh, path)); got != "modely" {
			t.Errorf("car_type = %#v, want modely (VIN beats stale template)", got)
		}
	})

	t.Run("unrelated template fields survive", func(t *testing.T) {
		veh := config.Vehicle{VIN: "5YJYGDEF5LF001234", ID: 1, VehicleID: 1}
		vc := buildFor(t, veh, path)["vehicle_config"].(map[string]any)
		if vc["trim_badging"] != "p90d" {
			t.Errorf("trim_badging = %#v, want p90d preserved", vc["trim_badging"])
		}
	})
}

// The default skeleton must not assert a model. Upstream hardcoded model3 here,
// which mislabelled every other vehicle for anyone without a captured template.
func TestDefaultTemplateAssertsNoModel(t *testing.T) {
	tmpl, err := LoadTemplate("")
	if err != nil {
		t.Fatalf("LoadTemplate(\"\"): %v", err)
	}
	m := tmpl.Clone()
	vc, ok := m["vehicle_config"].(map[string]any)
	if !ok {
		t.Fatalf("vehicle_config missing from default template: %#v", m["vehicle_config"])
	}
	if v, present := vc["car_type"]; present {
		t.Errorf("default template asserts car_type = %#v; it must be absent so the VIN decides", v)
	}
	if v, present := vc["trim_badging"]; present {
		t.Errorf("default template asserts trim_badging = %#v; it is not derivable and must be absent", v)
	}
}
