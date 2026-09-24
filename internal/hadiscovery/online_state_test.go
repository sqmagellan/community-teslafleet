package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

func TestOnlineFollowsState(t *testing.T) {
	for state, want := range map[string]bool{"online": true, "asleep": false, "offline": false} {
		s := buildState(store.Snapshot{}, store.Derived{State: state}, config.Units{})
		if s["online"] != want {
			t.Errorf("state %s: online = %v, want %v", state, s["online"], want)
		}
	}
}
