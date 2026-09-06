package ingest

import (
	"io"
	"log/slog"
	"testing"

	"github.com/go-zeromq/zmq4"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Stop() must be terminal. A dial already in flight when the signal lands used
// to install its socket afterwards and set connected=true, resurrecting a link
// the shutdown path had just cleared.
func TestSetSocket_RefusedAfterStop(t *testing.T) {
	c := NewConsumer(config.Ingest{ZMQAddr: "tcp://127.0.0.1:1"}, store.New(),
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Stop()

	late := zmq4.NewSub(c.ctx)
	if c.setSocket(late) {
		t.Fatal("setSocket accepted a socket after Stop")
	}
	if c.socket() != nil {
		t.Error("a socket was installed after Stop")
	}
	if c.Connected() {
		t.Error("Connected() is true after Stop")
	}
}
