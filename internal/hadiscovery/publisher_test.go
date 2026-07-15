package hadiscovery

import (
	"strings"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func newTestPublisher() *Publisher {
	cfg := &config.Config{
		HA: config.HA{DiscoveryPrefix: "homeassistant", StateTopicBase: "tgw"},
	}
	return &Publisher{cfg: cfg, entities: catalog(cfg.Units)}
}

func findEntity(t *testing.T, key string) entity {
	t.Helper()
	for _, e := range catalog(config.Units{}) {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("entity %q not found in catalog", key)
	return entity{}
}

func TestBuildState_NewKeys(t *testing.T) {
	vin := "VINSTATE"
	st := store.New(vin)
	st.SetField(vin, store.FieldChargeState, "Charging")
	st.SetField(vin, store.FieldChargeLimitSoc, float64(80))
	st.SetField(vin, store.FieldChargerVoltage, float64(232))
	st.SetField(vin, store.FieldChargeAmps, float64(16))
	st.SetField(vin, store.FieldTimeToFullCharge, float64(1.5))
	st.SetField(vin, store.FieldChargePortDoorOpen, true)
	st.SetField(vin, store.FieldTpmsFL, float64(2.9))
	st.SetField(vin, store.FieldTpmsFR, float64(2.8))
	st.SetField(vin, store.FieldTpmsRL, float64(3.0))
	st.SetField(vin, store.FieldTpmsRR, float64(3.1))

	snap, _ := st.Snapshot(vin)
	// Charging: true — the fixture represents an active charge session, so
	// charger_voltage is reported rather than clamped to 0.
	s := buildState(snap, store.Derived{State: "online", Charging: true}, config.Units{})

	checks := map[string]any{
		"charging_state":  "Charging",
		"charge_limit":    float64(80),
		"charger_voltage": float64(232),
		"charger_current": float64(16),
		"time_to_full":    float64(1.5),
		"tpms_fl":         float64(2.9),
		"tpms_fr":         float64(2.8),
		"tpms_rl":         float64(3.0),
		"tpms_rr":         float64(3.1),
	}
	for k, want := range checks {
		got, ok := s[k]
		if !ok {
			t.Errorf("buildState missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("buildState[%q] = %v (%T), want %v (%T)", k, got, got, want, want)
		}
	}

	// Every new sensor key must have a matching catalog entity.
	cat := catalog(config.Units{})
	have := map[string]bool{}
	for _, e := range cat {
		have[e.Key] = true
	}
	for k := range checks {
		if !have[k] {
			t.Errorf("catalog missing entity for key %q", k)
		}
	}
}

func TestBinarySensorValueTemplate(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VIN1", DisplayName: "Car"}
	dev := map[string]any{}
	origin := map[string]any{}

	tests := []struct {
		key       string
		wantOn    string
		wantOff   string
		wantInTpl string // substring the template must contain
	}{
		{"charging", "true", "false", "value_json.charging"},
		{"online", "true", "false", "value_json.online"},
		{"sentry", "true", "false", "value_json.sentry"},
		// locked is inverted: lock device_class on=unlocked, so payload_on is "false".
		{"locked", "false", "true", "value_json.locked"},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			e := findEntity(t, tc.key)
			c := p.discoveryConfig(v, e, dev, origin)

			if c["payload_on"] != tc.wantOn {
				t.Errorf("payload_on = %v, want %q", c["payload_on"], tc.wantOn)
			}
			if c["payload_off"] != tc.wantOff {
				t.Errorf("payload_off = %v, want %q", c["payload_off"], tc.wantOff)
			}
			tpl, _ := c["value_template"].(string)
			if tpl == "" {
				t.Fatalf("value_template not set")
			}
			if !strings.Contains(tpl, tc.wantInTpl) {
				t.Errorf("value_template = %q, want substring %q", tpl, tc.wantInTpl)
			}
			// Conceptually: a JSON true must render PayloadOn, false must render
			// PayloadOff. Verify the template encodes exactly that mapping.
			wantTpl := "{{ '" + tc.wantOn + "' if " + tc.wantInTpl + " else '" + tc.wantOff + "' }}"
			if tpl != wantTpl {
				t.Errorf("value_template = %q, want %q", tpl, wantTpl)
			}
		})
	}
}
