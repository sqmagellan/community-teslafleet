package hadiscovery

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/enroll"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// One key style. A field with no curated entity is keyed by its Tesla name in
// snake_case and named in the same words, sentence case. Topic, unique_id and
// entity_id follow the curated scheme: <component>/<car>/<key>, <car>_<key>.

func TestFieldKeyAndName(t *testing.T) {
	for _, tc := range []struct{ field, key, name string }{
		{"ChargeEnableRequest", "charge_enable_request", "Charge enable request"},
		{"TpmsLastSeenPressureTimeFl", "tpms_last_seen_pressure_time_fl", "TPMS last seen pressure time fl"},
		{"HvacACEnabled", "hvac_ac_enabled", "HVAC AC enabled"},
		{"DiStatorTempF", "di_stator_temp_f", "DI stator temp F"},
		{"Setting24HourTime", "setting_24_hour_time", "Setting 24 hour time"},
		{"Experimental_5", "experimental_5", "Experimental 5"},
		{"BMSState", "bms_state", "BMS state"},
	} {
		if got := fieldKey(tc.field); got != tc.key {
			t.Errorf("fieldKey(%s) = %q, want %q", tc.field, got, tc.key)
		}
		if got := fieldName(tc.field); got != tc.name {
			t.Errorf("fieldName(%s) = %q, want %q", tc.field, got, tc.name)
		}
	}
}

func TestGenericConfigUsesTheCuratedScheme(t *testing.T) {
	p := newTestPublisher()
	v := config.Vehicle{VIN: "VINGEN", DisplayName: "Car"}
	comp, c := p.genericDiscoveryConfig(v, "ChargeEnableRequest", true, map[string]any{}, map[string]any{})
	if comp != "binary_sensor" {
		t.Fatalf("component = %q", comp)
	}
	for k, want := range map[string]any{
		"name":              "Charge enable request",
		"unique_id":         "car_charge_enable_request",
		"object_id":         "car_charge_enable_request",
		"default_entity_id": "binary_sensor.car_charge_enable_request",
	} {
		if c[k] != want {
			t.Errorf("%s = %v, want %v", k, c[k], want)
		}
	}
}

func TestGenericTopicHasNoRawPrefix(t *testing.T) {
	p := newTestPublisher()
	p.discovered = map[string]map[string]bool{}
	p.covered = coveredFields()
	p.log = testLogger()
	rc := &recordingClient{published: map[string][]byte{}}
	p.client = rc
	v := config.Vehicle{VIN: "VINGEN", DisplayName: "Car"}
	p.discoverNewFields(v, store.Snapshot{VIN: v.VIN, Fields: map[string]store.FieldValue{
		"ModuleTempMax": {Value: float64(21)},
	}})
	if _, ok := rc.published["homeassistant/sensor/car/module_temp_max/config"]; !ok {
		t.Errorf("topics: %v", keys(rc.published))
	}
}

// A derived key must never equal a curated one, nor another derived one.
func TestDerivedKeysNeverCollide(t *testing.T) {
	curated := map[string]bool{}
	for _, e := range catalog(config.Units{}) {
		curated[e.Key] = true
	}
	covered := coveredFields()
	seen := map[string]string{}
	for _, f := range enroll.Fields {
		if covered[f] {
			continue
		}
		k := fieldKey(f)
		if curated[k] {
			t.Errorf("field %s derives curated key %q", f, k)
		}
		if other, dup := seen[k]; dup {
			t.Errorf("fields %s and %s both derive %q", f, other, k)
		}
		seen[k] = f
	}
}

// The name names the thing; the device_class supplies the state words.
func TestBinarySensorNames(t *testing.T) {
	for key, want := range map[string]struct{ name, class string }{
		"lock":         {"Lock", "lock"},
		"sentry_mode":  {"Sentry mode", ""},
		"charge_cable": {"Charge cable", "plug"},
		"driver_seat":  {"Driver seat", "occupancy"},
		"doors":        {"Doors", "door"},
	} {
		e := findEntity(t, key)
		if e.Component != "binary_sensor" || e.Name != want.name || e.DeviceClass != want.class {
			t.Errorf("%s = %s %q class %q, want binary_sensor %q class %q",
				key, e.Component, e.Name, e.DeviceClass, want.name, want.class)
		}
	}
}

// One entity per fact.
func TestOneEntityPerFact(t *testing.T) {
	keysOf := map[string]bool{}
	for _, e := range catalog(config.Units{}) {
		keysOf[e.Component+"."+e.Key] = true
	}
	for _, gone := range []string{
		"binary_sensor.locked", "binary_sensor.sentry", "binary_sensor.plugged_in",
		"binary_sensor.driver_present", "binary_sensor.online", "binary_sensor.charging",
		"binary_sensor.at_home", "sensor.sentry_mode",
	} {
		if keysOf[gone] {
			t.Errorf("%s is still published", gone)
		}
	}
	if !coveredFields()[store.FieldLocatedAtHome] {
		t.Error("LocatedAtHome would come back as a generic entity")
	}
	for _, key := range []string{"sentry_mode_state", "soc_raw", "charge_state_raw"} {
		e := findEntity(t, key)
		if e.Category != "diagnostic" || !e.Disabled {
			t.Errorf("%s: category %q disabled %v, want diagnostic and disabled by default", key, e.Category, e.Disabled)
		}
	}
	p := newTestPublisher()
	c := p.discoveryConfig(config.Vehicle{VIN: "V", DisplayName: "Car"}, findEntity(t, "soc_raw"), map[string]any{}, map[string]any{})
	if c["enabled_by_default"] != false {
		t.Errorf("soc_raw enabled_by_default = %v, want false", c["enabled_by_default"])
	}
}

func TestStateKeysFollowTheEntities(t *testing.T) {
	st := store.New("V")
	st.SetField("V", store.FieldLocked, true)
	st.SetField("V", store.FieldSentryMode, "SentryModeStateArmed")
	st.SetField("V", store.FieldDriverSeatOccupied, false)
	snap, _ := st.Snapshot("V")
	s := buildState(snap, store.Derived{State: "online"}, config.Units{})
	if s["lock"] != true || s["sentry_mode"] != true || s["sentry_mode_state"] == nil || s["driver_seat"] != false {
		t.Errorf("state = %v", s)
	}
	for _, gone := range []string{"locked", "sentry", "driver_present", "online", "at_home"} {
		if _, ok := s[gone]; ok {
			t.Errorf("state still has %q", gone)
		}
	}
}

// Landing: every retired topic gets an empty retained payload, so HA deletes
// the entity and the broker forgets the config. No new topic is retired.
func TestRetiredTopicsAreCleared(t *testing.T) {
	p := newTestPublisher()
	p.log = testLogger()
	rc := &recordingClient{published: map[string][]byte{}}
	p.client = rc
	v := config.Vehicle{VIN: "V", DisplayName: "Car"}
	if err := p.publishVehicleDiscovery(v); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{
		"homeassistant/binary_sensor/car/locked/config",
		"homeassistant/binary_sensor/car/sentry/config",
		"homeassistant/sensor/car/sentry_mode/config",
		"homeassistant/binary_sensor/car/plugged_in/config",
		"homeassistant/binary_sensor/car/driver_present/config",
		"homeassistant/binary_sensor/car/online/config",
		"homeassistant/binary_sensor/car/charging/config",
		"homeassistant/binary_sensor/car/at_home/config",
		"homeassistant/sensor/car/raw_ModuleTempMax/config",
		"homeassistant/binary_sensor/car/raw_ServiceMode/config",
	} {
		b, ok := rc.published[topic]
		if !ok || len(b) != 0 {
			t.Errorf("%s: published %v, payload %q; want an empty payload", topic, ok, b)
		}
	}
	for _, e := range p.entities {
		topic := "homeassistant/" + e.Component + "/car/" + e.Key + "/config"
		var cfg map[string]any
		if err := json.Unmarshal(rc.published[topic], &cfg); err != nil {
			t.Errorf("%s: no config after retiring (%v)", topic, err)
		}
	}
	retired := retiredTopics("homeassistant", "car")
	for _, e := range p.entities {
		if slices.Contains(retired, "homeassistant/"+e.Component+"/car/"+e.Key+"/config") {
			t.Errorf("live entity %s.%s is retired", e.Component, e.Key)
		}
	}
}
