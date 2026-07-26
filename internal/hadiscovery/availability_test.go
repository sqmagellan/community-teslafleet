package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

func assertAvailability(t *testing.T, what string, c map[string]any) {
	t.Helper()
	if got := c["availability_topic"]; got != "tgw/availability" {
		t.Errorf("%s: availability_topic = %v, want tgw/availability", what, got)
	}
	if got := c["payload_available"]; got != "online" {
		t.Errorf("%s: payload_available = %v", what, got)
	}
	if got := c["payload_not_available"]; got != "offline" {
		t.Errorf("%s: payload_not_available = %v", what, got)
	}
}

func TestAvailabilityTopic(t *testing.T) {
	if got := newTestPublisher().availabilityTopic(); got != "tgw/availability" {
		t.Errorf("availabilityTopic() = %q", got)
	}
}

// Every entity the gateway announces must carry availability. One that does not
// keeps showing its last value after the gateway dies, which is the exact failure
// this is for -- so this asserts the whole catalog, not a sample.
func TestEveryCuratedEntityHasAvailability(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "5YJ3TESTVIN000001", DisplayName: "Test", ID: 1, VehicleID: 1}
	dev := map[string]any{"identifiers": []string{"test"}}
	origin := map[string]any{"name": "community-teslafleet"}
	if len(p.entities) == 0 {
		t.Fatal("empty catalog")
	}
	for _, e := range p.entities {
		assertAvailability(t, "curated "+e.Key, p.discoveryConfig(v, e, dev, origin))
	}
}

func TestGenericAndCommandEntitiesHaveAvailability(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "5YJ3TESTVIN000001", DisplayName: "Test", ID: 1, VehicleID: 1}
	dev := map[string]any{"identifiers": []string{"test"}}
	origin := map[string]any{"name": "community-teslafleet"}

	// both generic branches: numeric sensor and bool binary_sensor
	_, num := p.genericDiscoveryConfig(v, "PackVoltage", float64(390), dev, origin)
	assertAvailability(t, "generic numeric", num)
	_, b := p.genericDiscoveryConfig(v, "DriverSeatOccupied", true, dev, origin)
	assertAvailability(t, "generic bool", b)

	ce := commands.Entity{Key: "flash_lights", Name: "Flash Lights", Component: "button"}
	assertAvailability(t, "command", p.commandDiscoveryConfig(v, ce, dev, origin))
}
