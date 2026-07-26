package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Persist must write a snapshot when its context is cancelled, not only on the
// interval tick — main relies on that for the shutdown flush, and the interval is
// 30s in production.
func TestPersistSavesOnCancel(t *testing.T) {
	const vin = "5YJ3TESTVIN000001"
	s := New(vin)
	s.SetField(vin, FieldSoc, float64(64))

	path := filepath.Join(t.TempDir(), "state.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// An interval far longer than the test: only the cancel path can save.
		s.Persist(ctx, path, time.Hour, slog.New(slog.DiscardHandler))
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Persist did not return after ctx cancel")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no snapshot written on cancel: %v", err)
	}
	restored := New(vin)
	n, err := restored.Load(path)
	if err != nil || n != 1 {
		t.Fatalf("Load = %d, %v", n, err)
	}
	if snap, ok := restored.Snapshot(vin); !ok {
		t.Fatal("restored snapshot missing")
	} else if v, ok := snap.Num(FieldSoc); !ok || v != 64 {
		t.Errorf("restored Soc = %v (ok=%v), want 64", v, ok)
	}
}
