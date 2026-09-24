package hadiscovery

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeMsg struct {
	topic    string
	payload  string
	retained bool
}

func (m fakeMsg) Duplicate() bool   { return false }
func (m fakeMsg) Qos() byte         { return 1 }
func (m fakeMsg) Retained() bool    { return m.retained }
func (m fakeMsg) Topic() string     { return m.topic }
func (m fakeMsg) MessageID() uint16 { return 0 }
func (m fakeMsg) Payload() []byte   { return []byte(m.payload) }
func (m fakeMsg) Ack()              {}

// A retained command is an old request. The broker replays it on every
// subscribe, and the publisher subscribes again on every reconnect.
func TestRetainedCommandIsIgnored(t *testing.T) {
	cfg := &config.Config{
		HA:       config.HA{StateTopicBase: "tgw", IdentifierMode: "name"},
		Vehicles: []config.Vehicle{{VIN: "VIN1", DisplayName: "Car"}},
	}
	var calls atomic.Int32
	p := &Publisher{
		cfg:   cfg,
		store: store.New("VIN1"),
		log:   testLogger(),
		relay: &commands.Relay{},
		dispatch: commands.NewDispatcher(func(vin, key, payload string) commands.Result {
			calls.Add(1)
			return commands.Result{Key: key, Payload: payload, Outcome: commands.OutcomeSent}
		}, commands.Limits{}, testLogger()),
	}

	p.handleCommand(nil, fakeMsg{topic: "tgw/car/cmd/trunk/set", payload: "OPEN", retained: true})
	p.handleCommand(nil, fakeMsg{topic: "tgw/car/cmd/honk/set", payload: "PRESS"})

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d commands dispatched, want 1 (the fresh honk, not the retained trunk)", n)
	}
}

func TestFrunkAndTrunkCoversUseDoorState(t *testing.T) {
	snap := store.Snapshot{Fields: map[string]store.FieldValue{
		store.FieldDoorState: {Value: map[string]any{"TrunkFront": false, "TrunkRear": true}},
	}}
	st := buildState(snap, store.Derived{}, config.Units{})
	if st["frunk"] != false || st["trunk"] != true {
		t.Errorf("state frunk=%v trunk=%v, want false/true", st["frunk"], st["trunk"])
	}
	p := newTestPublisher()
	for _, ce := range commands.Entities(config.Commands{}) {
		if ce.Key != "frunk" && ce.Key != "trunk" {
			continue
		}
		c := p.commandDiscoveryConfig(config.Vehicle{VIN: "VIN1"}, ce, nil, nil)
		if _, optimistic := c["optimistic"]; optimistic || c["state_topic"] == nil {
			t.Errorf("%s cover is optimistic; it has real state now", ce.Key)
		}
	}
}
