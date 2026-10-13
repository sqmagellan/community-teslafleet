package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// A key the car stopped reporting keeps its last value instead of vanishing and
// rendering as closed/off; a key it still reports always wins.
func TestCarryForwardFillsOnlyMissingStickyKeys(t *testing.T) {
	prev := map[string]any{"charge_port": true, "climate_on": true, "soc": 80}
	st := map[string]any{"climate_on": false}
	carryForward(st, prev, []string{"charge_port", "climate_on"})
	if st["charge_port"] != true {
		t.Errorf("charge_port = %v, want carried-forward true", st["charge_port"])
	}
	if st["climate_on"] != false {
		t.Errorf("climate_on = %v, want the fresh false", st["climate_on"])
	}
	if _, ok := st["soc"]; ok {
		t.Errorf("soc carried forward but is not sticky (its template defaults to none)")
	}
	carryForward(st, nil, []string{"windows"}) // first tick: nothing to carry
	if _, ok := st["windows"]; ok {
		t.Errorf("windows invented with no previous state")
	}
}

func TestStickyKeysCoverDefaultlessTemplates(t *testing.T) {
	got := map[string]bool{}
	for _, k := range stickyKeys(config.Commands{}) {
		got[k] = true
	}
	for _, k := range []string{"climate_on", "locked", "charge_port", "windows", "frunk", "trunk", "sentry", "charging"} {
		if !got[k] {
			t.Errorf("sticky keys miss %q", k)
		}
	}
	if got["charge_limit"] {
		t.Errorf("charge_limit is a number with a default; it must not be sticky")
	}
}
