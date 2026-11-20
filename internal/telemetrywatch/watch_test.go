package telemetrywatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestClassify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) float64 { return float64(now.Add(d).Unix()) }
	cfg := map[string]any{"fields": map[string]any{}, "exp": at(60 * 24 * time.Hour)}
	wrap := func(r map[string]any) map[string]any { return map[string]any{"response": r} }
	withExp := func(e float64) map[string]any {
		return wrap(map[string]any{"synced": true, "key_paired": true, "config": map[string]any{"exp": e}})
	}
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{"healthy", wrap(map[string]any{"synced": true, "config": cfg, "limit_reached": false, "key_paired": true}), "ok"},
		{"unwrapped body", map[string]any{"synced": true, "config": cfg, "key_paired": true}, "ok"},
		{"no exp never expires", wrap(map[string]any{"synced": true, "config": map[string]any{"fields": map[string]any{}}}), "ok"},
		{"config deleted (null)", wrap(map[string]any{"synced": true, "config": nil, "limit_reached": false, "key_paired": true}), "missing"},
		{"config absent", wrap(map[string]any{"synced": true}), "missing"},
		{"config of the wrong type", wrap(map[string]any{"synced": true, "config": "x"}), "malformed"},
		{"billing limit wins over a deleted config", wrap(map[string]any{"synced": true, "config": nil, "limit_reached": true}), "limit_reached"},
		{"key unpaired", wrap(map[string]any{"synced": true, "config": cfg, "key_paired": false}), "key_not_paired"},
		{"queued, not adopted", wrap(map[string]any{"synced": false, "config": cfg, "key_paired": true}), "not_synced"},
		{"exp exactly now is expired", withExp(at(0)), "expired"},
		{"exp an hour ago", withExp(at(-time.Hour)), "expired"},
		{"under a day left", withExp(at(time.Hour)), "expires_in_0d"},
		{"three days and an hour left", withExp(at(3*24*time.Hour + time.Hour)), "expires_in_3d"},
		{"one second under seven days", withExp(at(ExpiryWarning - time.Second)), "expires_in_6d"},
		{"exactly seven days is ok", withExp(at(ExpiryWarning)), "ok"},
	}
	for _, tc := range tests {
		if got := Classify(tc.body, now); got != tc.want {
			t.Errorf("%s: Classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestErrorVerdictKeepsTheHTTPStatus(t *testing.T) {
	if got := errorVerdict(errors.New(`status 404: {"error":"not_found"}`)); got != "error_404" {
		t.Errorf("got %q, want error_404", got)
	}
	if got := errorVerdict(errors.New("dial tcp: timeout")); got != "error" {
		t.Errorf("got %q, want error", got)
	}
}

func TestWatcherPublishesWholeSweeps(t *testing.T) {
	ok := map[string]any{"response": map[string]any{"synced": true, "config": map[string]any{}}}
	gone := map[string]any{"response": map[string]any{"synced": true, "config": nil}}
	var mu sync.Mutex
	bodies := map[string]map[string]any{"VIN00000000000001": ok, "VIN00000000000002": gone}
	var failWith error
	w := New(func(_ context.Context, vin string) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		if failWith != nil {
			return nil, failWith
		}
		return bodies[vin], nil
	}, []string{"VIN00000000000001", "VIN00000000000002"}, quiet)

	if got := w.Status(); got != "" {
		t.Fatalf("before any sweep Status = %q, want empty", got)
	}
	w.CheckOnce(context.Background())
	if got := w.Status(); got != "0002: missing" {
		t.Fatalf("Status = %q, want %q", got, "0002: missing")
	}
	mu.Lock()
	bodies["VIN00000000000002"] = ok
	mu.Unlock()
	w.CheckOnce(context.Background())
	if got := w.Status(); got != "ok" {
		t.Fatalf("Status = %q, want ok", got)
	}
	mu.Lock()
	failWith = errors.New("status 503: busy")
	mu.Unlock()
	w.CheckOnce(context.Background())
	if got := w.Status(); got != "0001: error_503; 0002: error_503" {
		t.Fatalf("Status = %q, want both error_503", got)
	}
}

// Sol review 2026-11-20: a car still being fetched must not let the first
// car's "ok" read as the whole fleet's.
func TestStatusNeverShowsAPartialSweep(t *testing.T) {
	release := make(chan struct{})
	reached := make(chan struct{})
	w := New(func(_ context.Context, vin string) (map[string]any, error) {
		if vin == "VIN00000000000002" {
			close(reached)
			<-release
			return map[string]any{"config": nil}, nil
		}
		return map[string]any{"config": map[string]any{}}, nil
	}, []string{"VIN00000000000001", "VIN00000000000002"}, quiet)

	done := make(chan struct{})
	go func() { w.CheckOnce(context.Background()); close(done) }()
	<-reached
	if got := w.Status(); got != "" {
		t.Errorf("mid-sweep Status = %q, want empty (nothing published yet)", got)
	}
	close(release)
	<-done
	if got := w.Status(); got != "0002: missing" {
		t.Errorf("after the sweep Status = %q, want %q", got, "0002: missing")
	}
}

func TestCancelStopsTheSweepAndPublishesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls []string
	w := New(func(ctx context.Context, vin string) (map[string]any, error) {
		calls = append(calls, vin)
		cancel() // shutdown arrives during the first car's request
		return nil, ctx.Err()
	}, []string{"VIN00000000000001", "VIN00000000000002", "VIN00000000000003"}, quiet)

	if w.CheckOnce(ctx) {
		t.Error("CheckOnce reported a complete sweep after cancellation")
	}
	if len(calls) != 1 {
		t.Errorf("fetched %d cars after cancellation, want 1", len(calls))
	}
	if got := w.Status(); got != "" {
		t.Errorf("Status = %q after a cancelled first sweep, want empty", got)
	}
}

func TestRunStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := New(func(context.Context, string) (map[string]any, error) {
		return map[string]any{"config": map[string]any{}}, nil
	}, []string{"VIN00000000000001"}, quiet)
	stopped := make(chan struct{})
	go func() { w.Run(ctx, time.Millisecond, time.Hour); close(stopped) }()
	deadline := time.Now().Add(2 * time.Second)
	for w.Status() != "ok" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if w.Status() != "ok" {
		t.Fatal("the first sweep never ran")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
