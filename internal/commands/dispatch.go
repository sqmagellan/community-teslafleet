package commands

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Limits bounds how many commands one car receives. A zero field disables
// that limit.
type Limits struct {
	// PerKeyPerHour caps how often one command key may be dispatched to one
	// car in any rolling hour. Media keys are exempt: pressing volume-up ten
	// times is normal use.
	PerKeyPerHour int
	// DuplicateWindow drops a set-state command whose key and payload match
	// one that was sent to the same car this recently.
	DuplicateWindow time.Duration
}

// Dispatcher runs commands for each car one at a time, in the order they
// arrived.
//
// Before it existed every MQTT message got its own goroutine, so two
// commands for one car raced. Dragging the charge-limit slider from 60 to 80
// could leave the car at 70, depending on which request Tesla saw last.
// Nothing limited volume either: on 2026-09-19 a looping automation sent 80
// window commands and 32 wakes to one car in four hours, and every one went
// out.
type Dispatcher struct {
	handle func(vin, key, payload string) Result
	limits Limits
	log    *slog.Logger
	now    func() time.Time

	mu   sync.Mutex
	cars map[string]*carQueue
}

type job struct {
	key, payload string
	done         func(Result)
}

type carQueue struct {
	pending []*job
	running bool
	perKey  map[string]*window
	lastOK  map[string]sentAt
}

type sentAt struct {
	payload string
	at      time.Time
}

// NewDispatcher wraps handle, which is normally (*Relay).Handle.
func NewDispatcher(handle func(vin, key, payload string) Result, lim Limits, log *slog.Logger) *Dispatcher {
	return &Dispatcher{handle: handle, limits: lim, log: log, now: time.Now, cars: map[string]*carQueue{}}
}

// Submit queues one command. done is called exactly once with the outcome,
// from the car's worker goroutine or, for a superseded command, from the
// caller's.
//
// A set-state command that is still waiting when a newer one with the same
// key arrives takes the newer payload, and the older request is reported as
// rejected. Momentary commands (horn, lights, media, wake) are never merged.
func (d *Dispatcher) Submit(vin, key, payload string, done func(Result)) {
	var replaced *job
	d.mu.Lock()
	q := d.car(vin)
	if !momentary(key) {
		for _, j := range q.pending {
			if j.key == key {
				replaced = &job{key: j.key, payload: j.payload, done: j.done}
				j.payload, j.done = payload, done
				break
			}
		}
	}
	if replaced == nil {
		q.pending = append(q.pending, &job{key: key, payload: payload, done: done})
	}
	start := !q.running
	q.running = true
	d.mu.Unlock()

	if replaced != nil && replaced.done != nil {
		replaced.done(Result{Key: key, Payload: replaced.payload, Outcome: OutcomeRejected,
			Reason: "replaced by a newer value before it was sent"})
	}
	if start {
		go d.run(vin, q)
	}
}

func (d *Dispatcher) car(vin string) *carQueue {
	q := d.cars[vin]
	if q == nil {
		q = &carQueue{perKey: map[string]*window{}, lastOK: map[string]sentAt{}}
		d.cars[vin] = q
	}
	return q
}

func (d *Dispatcher) run(vin string, q *carQueue) {
	for {
		d.mu.Lock()
		if len(q.pending) == 0 {
			q.running = false
			d.mu.Unlock()
			return
		}
		j := q.pending[0]
		q.pending = q.pending[1:]
		res, refused := d.refuse(q, j)
		d.mu.Unlock()

		if refused {
			d.log.Warn("command refused by limit", "vin", vin, "key", j.key, "reason", res.Reason)
		} else {
			res = d.handle(vin, j.key, j.payload)
			if res.Outcome == OutcomeSent {
				d.mu.Lock()
				q.lastOK[j.key] = sentAt{payload: normPayload(j.payload), at: d.now()}
				d.mu.Unlock()
			}
		}
		if j.done != nil {
			j.done(res)
		}
	}
}

// refuse applies the limits to one job. Called with d.mu held. A job that
// passes is counted against the hourly cap.
func (d *Dispatcher) refuse(q *carQueue, j *job) (Result, bool) {
	now := d.now()
	res := Result{Key: j.key, Payload: j.payload, Outcome: OutcomeRejected}

	if w := d.limits.DuplicateWindow; w > 0 && !momentary(j.key) {
		if last, ok := q.lastOK[j.key]; ok && last.payload == normPayload(j.payload) {
			if ago := now.Sub(last.at); ago < w {
				res.Reason = fmt.Sprintf("same command was sent %ds ago", int(ago.Seconds()))
				return res, true
			}
		}
	}
	if n := d.limits.PerKeyPerHour; n > 0 && !strings.HasPrefix(j.key, "media_") {
		w := q.perKey[j.key]
		if w == nil {
			w = &window{max: n, span: time.Hour}
			q.perKey[j.key] = w
		}
		if !w.allow(now) {
			res.Reason = fmt.Sprintf("more than %d %s commands in the last hour", n, j.key)
			return res, true
		}
	}
	return res, false
}

// momentary keys are presses, not states. Each press is its own request.
func momentary(key string) bool {
	switch key {
	case "flash_lights", "honk", "wake", "remote_start", "homelink":
		return true
	}
	return strings.HasPrefix(key, "media_")
}

func normPayload(p string) string { return strings.ToUpper(strings.TrimSpace(p)) }

// window counts events in a rolling time span. It is not safe for concurrent
// use; callers hold their own lock. max <= 0 means no limit.
type window struct {
	max    int
	span   time.Duration
	events []time.Time
}

// allow records one event and returns true if it fits under max.
func (w *window) allow(now time.Time) bool {
	if w.max <= 0 {
		return true
	}
	cut := now.Add(-w.span)
	i := 0
	for i < len(w.events) && !w.events[i].After(cut) {
		i++
	}
	w.events = w.events[i:]
	if len(w.events) >= w.max {
		return false
	}
	w.events = append(w.events, now)
	return true
}
