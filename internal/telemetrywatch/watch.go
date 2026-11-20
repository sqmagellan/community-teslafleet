// Package telemetrywatch checks, on a slow timer, that each car still holds a
// fleet_telemetry_config, so a config Tesla removed is seen within a day rather
// than when someone notices TeslaMate has gone quiet.
//
// Why: Tesla deletes a car's telemetry config when the account exceeds its
// billing limit (kevinkinnett/teslamate-nopoll's README, 2026), and a config
// also carries an expiry (`exp`). Either way the car simply stops connecting,
// which on this side looks exactly like a parked, sleeping car: nothing in the
// telemetry stream can tell the two apart. The GET does not wake the car (see
// commands.Relay.TelemetryConfig).
package telemetrywatch

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExpiryWarning is how far ahead of a config's `exp` it is reported.
const ExpiryWarning = 7 * 24 * time.Hour

// Fetch reads one car's fleet_telemetry_config (the Fleet API's JSON body).
type Fetch func(ctx context.Context, vin string) (map[string]any, error)

// Classify reads a fleet_telemetry_config GET body:
//
//	{"response": {"synced": true, "config": {...}|null, "limit_reached": false, "key_paired": true}}
//
// and returns "ok" or the first problem, worst first: limit_reached, missing
// (config null or absent), malformed (config of another type), expired,
// key_not_paired, not_synced, expires_in_<n>d (n = whole days left, so 0 means
// under a day). `exp` is Unix seconds; a config without one does not expire.
func Classify(body map[string]any, now time.Time) string {
	r := body
	if inner, ok := body["response"].(map[string]any); ok {
		r = inner
	}
	if b, ok := r["limit_reached"].(bool); ok && b {
		return "limit_reached"
	}
	raw, present := r["config"]
	if !present || raw == nil {
		return "missing"
	}
	cfg, ok := raw.(map[string]any)
	if !ok {
		return "malformed"
	}
	var exp time.Time
	if e, ok := cfg["exp"].(float64); ok && e > 0 {
		exp = time.Unix(int64(e), 0)
		if !now.Before(exp) {
			return "expired"
		}
	}
	if b, ok := r["key_paired"].(bool); ok && !b {
		return "key_not_paired"
	}
	if b, ok := r["synced"].(bool); ok && !b {
		return "not_synced"
	}
	if !exp.IsZero() && exp.Sub(now) < ExpiryWarning {
		return fmt.Sprintf("expires_in_%dd", int(exp.Sub(now).Hours()/24))
	}
	return "ok"
}

var statusRE = regexp.MustCompile(`^status (\d{3})\b`)

// errorVerdict keeps the HTTP status when there is one: a 404 reads differently
// from a timeout, and whether Tesla answers a deleted config with 404 or with
// `config: null` is not documented.
func errorVerdict(err error) string {
	if m := statusRE.FindStringSubmatch(err.Error()); m != nil {
		return "error_" + m[1]
	}
	return "error"
}

// tail is the last four characters of a VIN: enough to tell two cars apart in
// logs and /healthz without writing the whole VIN.
func tail(vin string) string {
	if len(vin) > 4 {
		return vin[len(vin)-4:]
	}
	return vin
}

// Watcher keeps the verdicts of the last COMPLETE sweep.
type Watcher struct {
	fetch Fetch
	vins  []string
	log   *slog.Logger

	mu      sync.Mutex
	verdict map[string]string // nil until the first sweep completes
}

func New(fetch Fetch, vins []string, log *slog.Logger) *Watcher {
	return &Watcher{fetch: fetch, vins: vins, log: log}
}

// CheckOnce reads every car's config and publishes the verdicts together, so
// /healthz never shows a half-finished sweep. A failed read is an "error"
// verdict (a Fleet API hiccup is not evidence the config is gone). It returns
// false, publishing nothing, when ctx ends mid-sweep.
func (w *Watcher) CheckOnce(ctx context.Context) bool {
	next := make(map[string]string, len(w.vins))
	for _, vin := range w.vins {
		if ctx.Err() != nil {
			return false
		}
		var v string
		body, err := w.fetch(ctx, vin)
		switch {
		case ctx.Err() != nil:
			return false
		case err != nil:
			v = errorVerdict(err)
			w.log.Warn("telemetry config check failed", "vin_tail", tail(vin), "err", err)
		default:
			v = Classify(body, time.Now())
			if v == "ok" {
				w.log.Info("telemetry config check", "vin_tail", tail(vin), "verdict", v)
			} else {
				w.log.Warn("telemetry config check", "vin_tail", tail(vin), "verdict", v)
			}
		}
		next[vin] = v
	}
	w.mu.Lock()
	w.verdict = next
	w.mu.Unlock()
	return true
}

// Run sweeps after first, then every `every` measured start to start, until
// ctx ends.
func (w *Watcher) Run(ctx context.Context, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			if !w.CheckOnce(ctx) {
				return
			}
			t.Reset(max(every-time.Since(start), time.Minute))
		}
	}
}

// Status is "" before the first complete sweep, "ok" when every car is ok,
// else one "<last 4 of VIN>: <verdict>" item per car that is not, joined by "; ".
func (w *Watcher) Status() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.verdict == nil {
		return ""
	}
	var bad []string
	for vin, v := range w.verdict {
		if v != "ok" {
			bad = append(bad, tail(vin)+": "+v)
		}
	}
	if len(bad) == 0 {
		return "ok"
	}
	sort.Strings(bad)
	return strings.Join(bad, "; ")
}
