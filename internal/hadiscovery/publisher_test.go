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
		key           string
		wantOn        string
		wantOff       string
		wantInTpl     string // substring the template must contain
		wantWhenTrue  string // HA binary_sensor state for telemetry true
		wantWhenFalse string
	}{
		{"charging", "true", "false", "value_json.charging", "on", "off"},
		{"online", "true", "false", "value_json.online", "on", "off"},
		{"sentry", "true", "false", "value_json.sentry", "on", "off"},
		// locked is inverted: lock device_class on=unlocked, so payload_on is
		// "false" and a LOCKED car must read OFF.
		{"locked", "false", "true", "value_json.locked", "off", "on"},
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
			// The template renders the UNDERLYING boolean; payload_on/payload_off
			// carry the device_class inversion. Asserting the literal payload here
			// is what let the lock sensor ship inverted: the template rendered
			// payload_on for locked==true, so a locked car read as unlocked in HA.
			//
			// default(false) tolerates a key the gateway omits from snapshots that
			// carry no data for it; it does not change what a present value renders.
			wantTpl := "{{ 'true' if " + tc.wantInTpl + " | default(false) else 'false' }}"
			if tpl != wantTpl {
				t.Errorf("value_template = %q, want %q", tpl, wantTpl)
			}
			// Semantic check: telemetry true must map to the payload HA reads as
			// the physical state named by the key.
			wantState := map[bool]string{true: tc.wantWhenTrue, false: tc.wantWhenFalse}
			for val, want := range wantState {
				rendered := "false"
				if val {
					rendered = "true"
				}
				var got string
				switch rendered {
				case c["payload_on"]:
					got = "on"
				case c["payload_off"]:
					got = "off"
				}
				if got != want {
					t.Errorf("telemetry %v renders %q -> HA %q, want %q", val, rendered, got, want)
				}
			}
		})
	}
}
