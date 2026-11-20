package commands

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Review 2026-11-16. Each case is a payload or VIN that used to reach the car.
func TestHandle_ReviewGuards(t *testing.T) {
	tests := []struct {
		name, vin, key, payload, want string
	}{
		// remote_start and software_update ran for ANY payload.
		{"remote_start press", "VIN1", "remote_start", "PRESS", "remote_start_drive"},
		{"remote_start junk", "VIN1", "remote_start", "OFF", ""},
		{"remote_start empty", "VIN1", "remote_start", "", ""},
		{"update install", "VIN1", "software_update", "INSTALL", "schedule_software_update"},
		{"update junk", "VIN1", "software_update", "OFF", ""},
		{"update empty", "VIN1", "software_update", "", ""},
		// non-finite numbers parsed fine and reached int()/JSON.
		{"charge_limit NaN", "VIN1", "charge_limit", "NaN", ""},
		{"charge_limit Inf", "VIN1", "charge_limit", "+Inf", ""},
		{"charging_amps NaN", "VIN1", "charging_amps", "nan", ""},
		{"climate_temp Inf", "VIN1", "climate_temp", "-Inf", ""},
		{"charge_limit ok", "VIN1", "charge_limit", "80", "set_charge_limit"},
		// a VIN is interpolated into the proxy path; with no allow-list it was unchecked.
		{"vin with path", "VIN1/command/door_unlock?x=", "honk", "PRESS", ""},
		{"vin with dot", "../VIN1", "honk", "PRESS", ""},
		{"vin empty", "", "honk", "PRESS", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, sent := relayTo(t)
			res := r.Handle(tc.vin, tc.key, tc.payload)
			if got := sent(); got != tc.want {
				t.Errorf("sent %q, want %q (result %+v)", got, tc.want, res)
			}
			if tc.want == "" && (res.Outcome != OutcomeRejected || res.Reason == "") {
				t.Errorf("refusal reported as %+v", res)
			}
		})
	}
}

// speed_limit checked its PIN; speed_limit_value did not, although discovery
// hides both without one.
func TestSpeedLimitValueNeedsPIN(t *testing.T) {
	r, sent := relayTo(t)
	r.speedPIN = ""
	res := r.Handle("VIN1", "speed_limit_value", "65")
	if sent() != "" || res.Outcome != OutcomeRejected {
		t.Fatalf("sent %q without a speed-limit PIN (%+v)", sent(), res)
	}
	r2, sent2 := relayTo(t)
	r2.Handle("VIN1", "speed_limit_value", "65")
	if sent2() != "speed_limit_set_limit" {
		t.Fatalf("with a PIN sent %q", sent2())
	}
}

// actuate_trunk toggles. State restored from the state file after a restart is
// not an observation: the lid may have moved since. Treat it as unknown.
func TestTrunkIgnoresRestoredDoorState(t *testing.T) {
	r, sent := relayTo(t)
	loc := doors(false, true)
	snap := loc["VIN1"]
	snap.Restored = true
	loc["VIN1"] = snap
	r.store = loc
	res := r.Handle("VIN1", "trunk", "CLOSE")
	if sent() != "" {
		t.Fatalf("toggled the trunk on restored state (%+v)", res)
	}
}

// Momentary presses were appended without bound while the car's worker was
// stuck behind a slow command, then delivered long after the caller stopped.
func TestDispatcherBoundsPendingQueue(t *testing.T) {
	release := make(chan struct{})
	var sent atomic.Int32
	d := NewDispatcher(func(vin, key, payload string) Result {
		<-release
		sent.Add(1)
		return sentOK(vin, key, payload)
	}, Limits{}, testLogger())
	var full atomic.Int32
	var wg sync.WaitGroup
	total := maxPending + 10
	for i := 0; i < total; i++ {
		wg.Add(1)
		d.Submit("VIN1", "media_vol_up", "PRESS", func(r Result) {
			if r.Outcome == OutcomeRejected && strings.Contains(r.Reason, "queue") {
				full.Add(1)
			}
			wg.Done()
		})
	}
	close(release)
	wg.Wait()
	// The first job may already be running, so up to maxPending+1 are sent.
	if n := int(sent.Load()); n > maxPending+1 {
		t.Fatalf("sent %d presses, want at most %d", n, maxPending+1)
	}
	if int(full.Load())+int(sent.Load()) != total {
		t.Fatalf("full=%d sent=%d, want them to add up to %d", full.Load(), sent.Load(), total)
	}
}
