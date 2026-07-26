package hadiscovery

import "testing"

// A broker that has lost its retained messages must get everything back, not just
// the curated entities: generic per-field discovery used to be published once per
// process, so those entities stayed missing until the gateway itself restarted.
func TestResetAnnouncedForgetsGenericDiscoveryToo(t *testing.T) {
	p := newTestPublisher()
	p.announced = map[string]bool{"car1": true}
	p.discovered = map[string]map[string]bool{}
	p.markFieldDiscovered("VIN1", "PackVoltage")

	if !p.fieldDiscovered("VIN1", "PackVoltage") {
		t.Fatal("markFieldDiscovered did not record the field")
	}
	p.resetAnnounced()
	if len(p.announced) != 0 {
		t.Errorf("announced = %v, want empty", p.announced)
	}
	if p.fieldDiscovered("VIN1", "PackVoltage") {
		t.Error("generic discovery still marked as published after reset")
	}
}

func TestFieldDiscoveredHandlesUnknownVIN(t *testing.T) {
	p := newTestPublisher()
	p.discovered = map[string]map[string]bool{}
	if p.fieldDiscovered("never-seen", "Soc") {
		t.Error("unknown VIN reported as already discovered")
	}
}
