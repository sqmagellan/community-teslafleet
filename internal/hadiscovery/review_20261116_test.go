package hadiscovery

import (
	"sync"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

type intake struct {
	mu   sync.Mutex
	vins []string
	keys []string
}

func intakePublisher(base string, vs []config.Vehicle) (*Publisher, *intake) {
	in := &intake{}
	vins := make([]string, 0, len(vs))
	for _, v := range vs {
		vins = append(vins, v.VIN)
	}
	p := &Publisher{
		cfg:   &config.Config{HA: config.HA{StateTopicBase: base, IdentifierMode: "name"}, Vehicles: vs},
		store: store.New(vins...),
		log:   testLogger(),
		relay: &commands.Relay{},
		dispatch: commands.NewDispatcher(func(vin, key, payload string) commands.Result {
			in.mu.Lock()
			in.vins = append(in.vins, vin)
			in.keys = append(in.keys, key)
			in.mu.Unlock()
			return commands.Result{Key: key, Payload: payload, Outcome: commands.OutcomeSent}
		}, commands.Limits{}, testLogger()),
	}
	return p, in
}

func (in *intake) settle(want int) ([]string, []string) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		in.mu.Lock()
		n := len(in.vins)
		in.mu.Unlock()
		if n >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.vins...), append([]string(nil), in.keys...)
}

// The subscription is <base>/+/cmd/+/set for any base, but the parser assumed
// the base was one topic level, so "teslafleet/home" dropped every command.
func TestCommandTopicWithMultiLevelBase(t *testing.T) {
	p, in := intakePublisher("teslafleet/home", []config.Vehicle{{VIN: "VIN1", DisplayName: "Car"}})
	p.handleCommand(nil, fakeMsg{topic: "teslafleet/home/car/cmd/honk/set", payload: "PRESS"})
	vins, keys := in.settle(1)
	if len(vins) != 1 || vins[0] != "VIN1" || keys[0] != "honk" {
		t.Fatalf("dispatched %v %v, want VIN1 honk", vins, keys)
	}
}

// "Road Car" and "Road-Car" share the slug road_car. vinForPubID returned the
// first match, so a command meant for one car went to the other.
func TestAmbiguousPubIDIsRefused(t *testing.T) {
	p, in := intakePublisher("tgw", []config.Vehicle{
		{VIN: "VIN1", DisplayName: "Road Car"},
		{VIN: "VIN2", DisplayName: "Road-Car"},
	})
	p.handleCommand(nil, fakeMsg{topic: "tgw/road_car/cmd/lock/set", payload: "UNLOCK"})
	if vins, _ := in.settle(1); len(vins) != 0 {
		t.Fatalf("an ambiguous device id reached %v", vins)
	}
}
