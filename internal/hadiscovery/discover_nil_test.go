package hadiscovery

import (
	"encoding/json"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

type doneToken struct{}

func (doneToken) Wait() bool                     { return true }
func (doneToken) WaitTimeout(time.Duration) bool { return true }
func (doneToken) Done() <-chan struct{}          { c := make(chan struct{}); close(c); return c }
func (doneToken) Error() error                   { return nil }

// recordingClient keeps every Publish by topic. Other paho.Client methods are
// not used by discoverNewFields and panic through the nil embedded interface.
type recordingClient struct {
	paho.Client
	published map[string][]byte
}

func (c *recordingClient) Publish(topic string, _ byte, _ bool, payload any) paho.Token {
	c.published[topic], _ = payload.([]byte)
	return doneToken{}
}

// A field whose first value is nil (reported invalid, or restored that way) must
// not be published yet: the retained config would stay classless once real numbers
// arrive. It is published on the first real value instead.
func TestDiscoverNewFieldsWaitsForNonNilValue(t *testing.T) {
	p := newTestPublisher()
	p.discovered = map[string]map[string]bool{}
	p.covered = map[string]bool{}
	p.log = testLogger()
	rc := &recordingClient{published: map[string][]byte{}}
	p.client = rc
	v := config.Vehicle{VIN: "VINNIL", DisplayName: "Car"}

	p.discoverNewFields(v, store.Snapshot{VIN: v.VIN, Fields: map[string]store.FieldValue{
		"ModuleTempMax": {Value: nil},
		"ServiceMode":   {Value: nil},
	}})
	if len(rc.published) != 0 {
		t.Fatalf("published for nil values: %v", keys(rc.published))
	}
	if p.fieldDiscovered(v.VIN, "ModuleTempMax") || p.fieldDiscovered(v.VIN, "ServiceMode") {
		t.Fatal("nil-valued field marked discovered")
	}

	p.discoverNewFields(v, store.Snapshot{VIN: v.VIN, Fields: map[string]store.FieldValue{
		"ModuleTempMax": {Value: float64(21.5)},
		"ServiceMode":   {Value: false},
	}})
	raw, ok := rc.published["homeassistant/sensor/car/raw_ModuleTempMax/config"]
	if !ok {
		t.Fatalf("numeric field not published as sensor; topics: %v", keys(rc.published))
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["device_class"] != "temperature" || cfg["unit_of_measurement"] != "°C" || cfg["state_class"] != "measurement" {
		t.Errorf("heuristics missing after nil first value: %v %v %v", cfg["device_class"], cfg["unit_of_measurement"], cfg["state_class"])
	}
	if _, ok := rc.published["homeassistant/binary_sensor/car/raw_ServiceMode/config"]; !ok {
		t.Errorf("bool field not published as binary_sensor; topics: %v", keys(rc.published))
	}
	if !p.fieldDiscovered(v.VIN, "ModuleTempMax") || !p.fieldDiscovered(v.VIN, "ServiceMode") {
		t.Error("fields not marked discovered after a real value")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
