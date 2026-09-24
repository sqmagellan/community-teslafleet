package commands

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// collect gathers the Results the dispatcher reports, keyed by payload.
type collect struct {
	mu  sync.Mutex
	wg  sync.WaitGroup
	got map[string]Result
}

func newCollect() *collect { return &collect{got: map[string]Result{}} }

func (c *collect) submit(d *Dispatcher, key, payload string) {
	c.wg.Add(1)
	d.Submit("VIN1", key, payload, func(r Result) {
		c.mu.Lock()
		c.got[key+"="+payload] = r
		c.mu.Unlock()
		c.wg.Done()
	})
}

func (c *collect) wait(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not report every command")
	}
}

func sentOK(vin, key, payload string) Result {
	return Result{Key: key, Payload: payload, Outcome: OutcomeSent}
}

func TestDispatcherRunsOneCarAtATimeInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var running, peak atomic.Int32
	d := NewDispatcher(func(vin, key, payload string) Result {
		if n := running.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		order = append(order, key)
		mu.Unlock()
		running.Add(-1)
		return sentOK(vin, key, payload)
	}, Limits{}, testLogger())

	c := newCollect()
	keys := []string{"charging", "sentry", "lock", "windows", "charge_port"}
	for _, k := range keys {
		c.submit(d, k, "ON")
	}
	c.wait(t)

	if peak.Load() != 1 {
		t.Errorf("%d commands ran at once for one car, want 1", peak.Load())
	}
	if strings.Join(order, ",") != strings.Join(keys, ",") {
		t.Errorf("ran in order %v, want %v", order, keys)
	}
}

// The slider case: 60 is running, then 70 and 80 arrive. 70 never needs to
// reach the car.
func TestDispatcherLatestValueWins(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var handled []string
	d := NewDispatcher(func(vin, key, payload string) Result {
		if payload == "60" {
			<-release
		}
		mu.Lock()
		handled = append(handled, payload)
		mu.Unlock()
		return sentOK(vin, key, payload)
	}, Limits{}, testLogger())

	c := newCollect()
	c.submit(d, "charge_limit", "60")
	time.Sleep(20 * time.Millisecond) // let 60 start
	c.submit(d, "charge_limit", "70")
	c.submit(d, "charge_limit", "80")
	close(release)
	c.wait(t)

	if strings.Join(handled, ",") != "60,80" {
		t.Errorf("car received %v, want [60 80]", handled)
	}
	if r := c.got["charge_limit=70"]; r.Outcome != OutcomeRejected || !strings.Contains(r.Reason, "newer value") {
		t.Errorf("superseded 70 reported %+v, want rejected as replaced", r)
	}
}

func TestDispatcherMomentaryCommandsAreNeverMerged(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	d := NewDispatcher(func(vin, key, payload string) Result {
		if calls.Add(1) == 1 {
			<-release
		}
		return sentOK(vin, key, payload)
	}, Limits{DuplicateWindow: time.Minute}, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		d.Submit("VIN1", "honk", "PRESS", func(Result) { wg.Done() })
	}
	close(release)
	wg.Wait()
	if calls.Load() != 3 {
		t.Errorf("3 honks reached the car %d times, want 3", calls.Load())
	}
}

func TestDispatcherDropsARecentDuplicate(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	d := NewDispatcher(func(vin, key, payload string) Result {
		calls.Add(1)
		return sentOK(vin, key, payload)
	}, Limits{DuplicateWindow: time.Minute}, testLogger())
	d.now = func() time.Time { return now }

	run := func(payload string) Result {
		ch := make(chan Result, 1)
		d.Submit("VIN1", "cabin_overheat", payload, func(r Result) { ch <- r })
		return <-ch
	}

	if r := run("ON"); r.Outcome != OutcomeSent {
		t.Fatalf("first ON: %+v", r)
	}
	now = now.Add(17 * time.Second)
	if r := run(" on "); r.Outcome != OutcomeRejected {
		t.Errorf("same command 17s later: %+v, want rejected", r)
	}
	if r := run("OFF"); r.Outcome != OutcomeSent {
		t.Errorf("a different payload must go through: %+v", r)
	}
	now = now.Add(2 * time.Minute)
	if r := run("OFF"); r.Outcome != OutcomeSent {
		t.Errorf("a repeat after the window must go through: %+v", r)
	}
	if calls.Load() != 3 {
		t.Errorf("car received %d commands, want 3", calls.Load())
	}
}

func TestDispatcherHourlyCap(t *testing.T) {
	now := time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	d := NewDispatcher(func(vin, key, payload string) Result {
		calls.Add(1)
		return sentOK(vin, key, payload)
	}, Limits{PerKeyPerHour: 3}, testLogger())
	d.now = func() time.Time { return now }

	run := func(key, payload string) Result {
		ch := make(chan Result, 1)
		d.Submit("VIN1", key, payload, func(r Result) { ch <- r })
		return <-ch
	}

	// The 2026-09-19 loop: a window command every three minutes.
	var refused int
	for i := 0; i < 5; i++ {
		if run("windows", []string{"OPEN", "CLOSE"}[i%2]).Outcome == OutcomeRejected {
			refused++
		}
		now = now.Add(3 * time.Minute)
	}
	if refused != 2 {
		t.Errorf("5 window commands in 15 minutes with a cap of 3: %d refused, want 2", refused)
	}
	for i := 0; i < 5; i++ {
		if r := run("media_vol_up", "PRESS"); r.Outcome != OutcomeSent {
			t.Errorf("media key capped: %+v", r)
		}
	}
	now = now.Add(time.Hour)
	if r := run("windows", "OPEN"); r.Outcome != OutcomeSent {
		t.Errorf("cap did not reset after an hour: %+v", r)
	}
}

func TestWindowAllow(t *testing.T) {
	t0 := time.Now()
	w := &window{max: 2, span: time.Hour}
	if !w.allow(t0) || !w.allow(t0.Add(time.Minute)) {
		t.Fatal("first two events refused")
	}
	if w.allow(t0.Add(2 * time.Minute)) {
		t.Error("third event inside the hour allowed")
	}
	if !w.allow(t0.Add(time.Hour + time.Second)) {
		t.Error("event after the oldest one expired was refused")
	}
	if !(&window{}).allow(t0) {
		t.Error("zero window must not limit")
	}
}
