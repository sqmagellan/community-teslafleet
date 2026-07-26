// Package ingest consumes Tesla fleet-telemetry's brokerless ZMQ stream.
// fleet-telemetry's zmq dispatcher PUB-binds an address; we SUB-connect. With
// transmit_decoded_records=true the payload is protojson of the Payload proto:
//
//	{"vin":"...","data":[{"key":"Soc","value":{"intValue":61}},
//	                     {"key":"Location","value":{"locationValue":{"latitude":..,"longitude":..}}}],...}
//
// Each value object has exactly one variant key; we take it generically.
package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-zeromq/zmq4"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/recorder"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

type Consumer struct {
	addr     string
	store    *store.Store
	rec      *recorder.Recorder // optional JSONL recorder (may be nil)
	log      *slog.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	seenVINs map[string]bool // first-record breadcrumb (loop goroutine only)

	// mu guards sub. The socket is REPLACED on every reconnect, by the ingest
	// goroutine, while Stop() closes it from whichever goroutine is shutting the
	// process down -- so the pointer itself is shared state. Unguarded it is a
	// data race that the detector reports the moment a stop lands during a
	// reconnect, and in the worst case a double close or a close of the socket
	// the loop is about to Recv() on.
	mu  sync.Mutex
	sub zmq4.Socket
}

type vPayload struct {
	Vin  string `json:"vin"`
	Data []struct {
		Key   string                     `json:"key"`
		Value map[string]json.RawMessage `json:"value"`
	} `json:"data"`
}

type connPayload struct {
	Vin    string `json:"vin"`
	Status string `json:"status"`
}

func NewConsumer(cfg config.Ingest, st *store.Store, rec *recorder.Recorder, log *slog.Logger) *Consumer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Consumer{addr: cfg.ZMQAddr, store: st, rec: rec, log: log, ctx: ctx, cancel: cancel, seenVINs: map[string]bool{}}
}

// Start dials the fleet-telemetry PUB socket and begins consuming. If the stream
// isn't reachable yet (fleet-telemetry not up, DNS not resolving, network blip), it
// does NOT fail — it keeps retrying in the background with backoff and starts
// consuming once connected. A telemetry gateway must survive its upstream being
// temporarily unavailable rather than crash-loop.
func (c *Consumer) Start() error {
	if err := c.dial(); err != nil {
		c.log.Warn("zmq ingest not reachable yet — retrying in background", "addr", c.addr, "err", err)
		go func() {
			if !c.reconnect() { // blocks (with backoff) until connected or ctx cancelled
				return
			}
			c.loop()
		}()
		return nil
	}
	go c.loop()
	return nil
}

// dial creates a fresh SUB socket, dials with retries and subscribes to all
// topics. It is used both at startup and on reconnect.
func (c *Consumer) dial() error {
	sub := zmq4.NewSub(c.ctx)
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		if err = sub.Dial(c.addr); err == nil {
			break
		}
		c.log.Warn("zmq dial retry", "addr", c.addr, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		_ = sub.Close()
		return err
	}
	if err := sub.SetOption(zmq4.OptionSubscribe, ""); err != nil {
		_ = sub.Close()
		return err
	}
	c.setSocket(sub)
	c.log.Info("zmq ingest connected", "addr", c.addr)
	return nil
}

// reconnect tears down the faulted socket and re-dials with backoff. Returns
// false if the context was cancelled while reconnecting.
func (c *Consumer) reconnect() bool {
	c.closeSocket()
	backoff := time.Second
	for {
		if c.ctx.Err() != nil {
			return false
		}
		if err := c.dial(); err == nil {
			c.log.Info("zmq ingest reconnected", "addr", c.addr)
			return true
		} else {
			c.log.Warn("zmq reconnect failed", "addr", c.addr, "err", err, "retry_in", backoff)
		}
		select {
		case <-c.ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// setSocket publishes a freshly dialled socket, closing any predecessor. The
// close happens OUTSIDE the lock: closing a zmq socket can block, and a blocked
// close while holding mu would stall Stop().
func (c *Consumer) setSocket(s zmq4.Socket) {
	c.mu.Lock()
	old := c.sub
	c.sub = s
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// closeSocket closes the current socket and clears it, so a subsequent close is
// a no-op rather than a double close.
func (c *Consumer) closeSocket() {
	c.mu.Lock()
	old := c.sub
	c.sub = nil
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// socket returns the current socket, or nil once it has been closed. Callers do
// their blocking Recv() on the returned value, never on the field.
func (c *Consumer) socket() zmq4.Socket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sub
}

func (c *Consumer) Stop() {
	c.cancel()
	c.closeSocket()
}

func (c *Consumer) loop() {
	for {
		sock := c.socket()
		if sock == nil {
			// Stop() closed and cleared the socket while we were in flight.
			return
		}
		msg, err := sock.Recv()
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			// A recv error means the PUB peer's connection faulted (e.g.
			// fleet-telemetry restarted). go-zeromq surfaces the fault exactly
			// once and then blocks forever on the next Recv(), so we must
			// re-dial on the FIRST error — waiting for repeated errors would
			// wedge ingest permanently on a dead socket. reconnect() has its
			// own backoff, so an unnecessary reconnect on a transient error is
			// cheap.
			c.log.Warn("zmq recv error — reconnecting", "err", err)
			if !c.reconnect() {
				return // context cancelled
			}
			continue
		}
		c.handle(msg)
	}
}

func (c *Consumer) handle(msg zmq4.Msg) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("panic handling zmq message", "recover", r)
		}
	}()
	if len(msg.Frames) == 0 {
		return
	}
	payload := msg.Frames[len(msg.Frames)-1]

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil {
		c.log.Warn("zmq top-level json unmarshal failed", "err", err)
		return
	}
	if _, ok := probe["data"]; ok {
		c.handleV(payload)
		return
	}
	if _, ok := probe["status"]; ok {
		c.handleConnectivity(payload)
		return
	}
	c.log.Debug("zmq message matched no known shape")
}

func (c *Consumer) handleV(payload []byte) {
	var p vPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.log.Warn("zmq v-payload unmarshal failed", "err", err)
		return
	}
	if p.Vin == "" {
		return
	}
	if !c.seenVINs[p.Vin] {
		c.seenVINs[p.Vin] = true
		c.log.Info("telemetry ingested", "vin", p.Vin)
	}
	for _, d := range p.Data {
		if d.Key == "" {
			continue
		}
		if val, ok := extractValue(d.Value); ok {
			c.store.SetField(p.Vin, d.Key, val)
			c.rec.Record(p.Vin, d.Key, val)
			// Do not log raw GPS coordinates; redact the Location value.
			if d.Key == store.FieldLocation {
				c.log.Debug("telemetry", "vin", p.Vin, "field", d.Key, "value", "<redacted>")
			} else {
				c.log.Debug("telemetry", "vin", p.Vin, "field", d.Key, "value", val)
			}
		}
	}
}

func (c *Consumer) handleConnectivity(payload []byte) {
	var p connPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.Vin == "" {
		return
	}
	status := "online"
	switch strings.ToUpper(p.Status) {
	case "DISCONNECTED":
		status = "offline"
	case "CONNECTED":
		status = "online"
	default:
		status = strings.ToLower(p.Status)
	}
	c.store.SetConnectivity(p.Vin, status)
	c.log.Info("connectivity", "vin", p.Vin, "status", status)
}

// extractValue pulls the single variant out of a protojson Value object.
// Scalars come back as float64/string/bool; Location comes back as a
// map[string]any{latitude,longitude}; enum variants come back as their name string.
func extractValue(m map[string]json.RawMessage) (any, bool) {
	for k, raw := range m {
		if k == "invalid" {
			return nil, false
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false
		}
		return v, true
	}
	return nil, false
}
