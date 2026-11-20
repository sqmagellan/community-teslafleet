package store

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// teslamate-org/teslamate#5648: TeslaMate drops its stream when vehicle_data
// says the car is offline mid-drive. A stray "offline" connectivity event while
// frames keep arriving must therefore still derive "online", and a drive must
// stay "online" however the connectivity flag reads.
func TestGotcha5648_OfflineEventWhileFramesArriveStaysOnline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, asleepWhenIdle := range []bool{false, true} {
		cfg := config.State{OnlineGraceSeconds: 300, StaleAfterSeconds: 660, ReportAsleepWhenIdle: asleepWhenIdle}

		parked := Snapshot{Fields: map[string]FieldValue{}, Connectivity: "offline", LastV: now.Add(-10 * time.Second)}
		if got := Derive(parked, cfg, now).State; got != "online" {
			t.Errorf("asleepWhenIdle=%v: offline flag with a frame 10 s ago = %q, want online", asleepWhenIdle, got)
		}

		driving := Snapshot{
			Fields:       map[string]FieldValue{FieldGear: {Value: "D"}},
			Connectivity: "offline",
			LastV:        now.Add(-10 * time.Second),
		}
		if got := Derive(driving, cfg, now).State; got != "online" {
			t.Errorf("asleepWhenIdle=%v: offline flag mid-drive = %q, want online", asleepWhenIdle, got)
		}

		// The gear must carry it on its own: the last frame is past the online
		// grace (300 s) but not yet stale (660 s).
		drivingQuiet := driving
		drivingQuiet.LastV = now.Add(-400 * time.Second)
		if got := Derive(drivingQuiet, cfg, now).State; got != "online" {
			t.Errorf("asleepWhenIdle=%v: offline flag mid-drive, frame 400 s old = %q, want online", asleepWhenIdle, got)
		}
	}
}
