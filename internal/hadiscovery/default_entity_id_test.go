package hadiscovery

import (
	"strings"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// Home Assistant 2026.4 stopped reading object_id; it names a discovered
// entity from default_entity_id, a full entity_id with its domain. Without it
// the entity_id follows the device and entity names ("Battery" gives
// sensor.<car>_battery, not sensor.<car>_battery_level). object_id stays for
// older Home Assistant.
func assertDefaultEntityID(t *testing.T, what, component string, c map[string]any) {
	t.Helper()
	obj, _ := c["object_id"].(string)
	if obj == "" {
		t.Fatalf("%s: no object_id", what)
	}
	want := component + "." + strings.ToLower(obj)
	if c["default_entity_id"] != want {
		t.Errorf("%s: default_entity_id = %v, want %s", what, c["default_entity_id"], want)
	}
}

func TestDefaultEntityIDCurated(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VIN1", DisplayName: "Tesla Model S"}
	for _, e := range p.entities {
		c := p.discoveryConfig(v, e, nil, nil)
		assertDefaultEntityID(t, "curated "+e.Key, e.Component, c)
	}
	c := p.discoveryConfig(v, findEntity(t, "battery_level"), nil, nil)
	if c["default_entity_id"] != "sensor.tesla_model_s_battery_level" {
		t.Errorf("battery_level default_entity_id = %v", c["default_entity_id"])
	}
}

func TestDefaultEntityIDCommands(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VIN1", DisplayName: "Tesla Model S"}
	for _, ce := range commands.Entities(config.Commands{}) {
		c := p.commandDiscoveryConfig(v, ce, nil, nil)
		assertDefaultEntityID(t, "command "+ce.Key, ce.Component, c)
	}
}

// A generic entity's key is Tesla's field name in snake_case, so its
// default_entity_id is lowercase like every curated one.
func TestDefaultEntityIDGenericIsLowercase(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VIN1", DisplayName: "Tesla Model S"}
	component, c := p.genericDiscoveryConfig(v, "ChargePortDoorOpen", true, nil, nil)
	assertDefaultEntityID(t, "generic", component, c)
	if c["default_entity_id"] != "binary_sensor.tesla_model_s_charge_port_door_open" {
		t.Errorf("generic default_entity_id = %v", c["default_entity_id"])
	}
}
