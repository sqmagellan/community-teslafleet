package hadiscovery

import (
	"strings"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// A lock or binary sensor with no reading must not claim a state. HA treats
// a rendered "None" as unknown.
func TestMissingBooleansRenderNone(t *testing.T) {
	p := newTestPublisher()
	p.relay = &commands.Relay{}
	v := config.Vehicle{VIN: "VIN1"}
	var lock map[string]any
	for _, ce := range commands.Entities(config.Commands{}) {
		if ce.Key == "lock" {
			lock = p.commandDiscoveryConfig(v, ce, nil, nil)
		}
	}
	tpl, _ := lock["value_template"].(string)
	if !strings.Contains(tpl, "none if v is none") || strings.Contains(tpl, "if value_json.locked else") {
		t.Fatalf("lock template still maps a missing value to a state: %q", tpl)
	}
	_, cfg := p.genericDiscoveryConfig(v, "SomeFlag", true, nil, nil)
	if tpl, _ := cfg["value_template"].(string); !strings.Contains(tpl, "default(none)") {
		t.Fatalf("generic bool template: %q", tpl)
	}
	e := findEntity(t, "doors")
	if tpl, _ := p.discoveryConfig(v, e, nil, nil)["value_template"].(string); !strings.Contains(tpl, "none if v is none") {
		t.Fatalf("binary_sensor template: %q", tpl)
	}
}

func TestChargeCableOmittedWithoutLatch(t *testing.T) {
	st := buildState(store.Snapshot{Fields: map[string]store.FieldValue{}}, store.Derived{}, config.Units{})
	if _, ok := st["charge_cable"]; ok {
		t.Fatalf("charge_cable = %v with no latch reading, want absent", st["charge_cable"])
	}
	st = buildState(store.Snapshot{Fields: map[string]store.FieldValue{
		store.FieldChargePortLatch: {Value: "ChargePortLatchEngaged"},
	}}, store.Derived{}, config.Units{})
	if st["charge_cable"] != true {
		t.Fatalf("charge_cable = %v with an engaged latch", st["charge_cable"])
	}
}

// HA's number sliders advertise the bounds the relay enforces, per car.
func TestNumberDiscoveryUsesCarRange(t *testing.T) {
	p := newTestPublisher()
	snap := store.New("VIN1")
	p.relay = commands.NewRelayForTest(snap)
	v := config.Vehicle{VIN: "VIN1"}
	find := func() map[string]any {
		for _, ce := range commands.Entities(config.Commands{}) {
			if ce.Key == "charging_amps" {
				return p.commandDiscoveryConfig(v, ce, nil, nil)
			}
		}
		t.Fatal("no charging_amps entity")
		return nil
	}
	if c := find(); c["max"] != 48.0 {
		t.Fatalf("static max = %v, want 48", c["max"])
	}
	before := p.rangeSig(v)
	snap.SetField("VIN1", "ChargeCurrentRequestMax", 32.0)
	if c := find(); c["max"] != 32.0 {
		t.Fatalf("car-reported max = %v, want 32", c["max"])
	}
	if p.rangeSig(v) == before {
		t.Fatal("range signature did not change, so discovery would not be re-announced")
	}
	for _, ce := range commands.Entities(config.Commands{}) {
		if ce.Key == "climate" {
			c := p.commandDiscoveryConfig(v, ce, nil, nil)
			if c["min_temp"] != 15.0 || c["max_temp"] != 28.0 {
				t.Fatalf("climate range %v..%v", c["min_temp"], c["max_temp"])
			}
		}
	}
}
