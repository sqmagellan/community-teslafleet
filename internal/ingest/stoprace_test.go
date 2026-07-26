package ingest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/go-zeromq/zmq4"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Stopping while the ingest goroutine is mid-reconnect used to be a data race on
// the socket field: reconnect replaces it, Stop closes it. It surfaced as a flaky
// -race failure on loaded machines (CI caught it; a quiet workstation did not), so
// this drives the overlap directly instead of waiting for a real dial to fault.
func TestSocketSwapAndStopAreRaceFree(t *testing.T) {
	c := NewConsumer(config.Ingest{ZMQAddr: "tcp://127.0.0.1:1"}, store.New(), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				c.setSocket(zmq4.NewSub(context.Background())) // what reconnect does
				_ = c.socket()                                 // what loop does
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Stop() // what shutdown does
	}()
	wg.Wait()

	// Idempotent: a second Stop must not double-close.
	c.Stop()
	if s := c.socket(); s != nil {
		t.Errorf("socket = %v after Stop, want nil", s)
	}
}
