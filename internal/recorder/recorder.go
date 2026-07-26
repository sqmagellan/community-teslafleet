// Package recorder appends every telemetry field update to a JSONL file so value
// changes can be debugged over time (e.g. reviewing a whole drive afterwards).
// Each line: {"t":"<RFC3339>","vin":"...","field":"Soc","value":58.3}.
package recorder

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Recorder struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	w        *bufio.Writer
	size     int64
	log      *slog.Logger
	closed   bool
	failing  bool // last write/flush failed; used to log transitions, not every tick
}

// note reports a write or flush failure, logging only when the state CHANGES.
//
// bufio.Writer keeps its first error sticky, so a full disk or a closed file
// makes every subsequent call fail too. Logging each one would emit a line per
// telemetry field -- thousands a minute -- and bury the first failure, which is
// the only one that says anything. So: one line when recording breaks, one when
// it recovers.
func (r *Recorder) note(op string, err error) {
	if err == nil {
		if r.failing {
			r.failing = false
			r.log.Info("telemetry recording recovered", "path", r.path)
		}
		return
	}
	if !r.failing {
		r.failing = true
		r.log.Error("telemetry recording is failing", "op", op, "path", r.path, "err", err)
	}
}

type line struct {
	T     string `json:"t"`
	VIN   string `json:"vin"`
	Field string `json:"field"`
	Value any    `json:"value"`
}

// New opens (appends to) the JSONL file. maxMB rotates the file to <path>.1 when
// exceeded (one backup kept). A path of "" disables recording (returns nil, nil).
func New(path string, maxMB int, log *slog.Logger) (*Recorder, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, _ := f.Stat()
	var size int64
	if info != nil {
		size = info.Size()
	}
	if maxBytes := int64(maxMB) * 1024 * 1024; maxBytes <= 0 {
		maxMB = 100
	}
	r := &Recorder{
		path:     path,
		maxBytes: int64(maxMB) * 1024 * 1024,
		f:        f,
		w:        bufio.NewWriter(f),
		size:     size,
		log:      log,
	}
	// Flush periodically so a crash/drive-review doesn't lose the buffer tail.
	go r.flushLoop()
	log.Info("telemetry recording enabled", "path", path, "max_mb", maxMB)
	return r, nil
}

// Record appends one field update. Safe to call with a nil *Recorder (no-op).
func (r *Recorder) Record(vin, field string, value any) {
	if r == nil {
		return
	}
	b, err := json.Marshal(line{T: time.Now().UTC().Format(time.RFC3339Nano), VIN: vin, Field: field, Value: value})
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	n, err := r.w.Write(b)
	if err != nil {
		r.note("write", err)
		return
	}
	if err := r.w.WriteByte('\n'); err != nil {
		r.note("write", err)
		return
	}
	r.note("write", nil)
	r.size += int64(n) + 1
	if r.size >= r.maxBytes {
		r.rotate()
	}
}

func (r *Recorder) rotate() {
	// Flush before rotating or the buffered tail of the old file is dropped on
	// the floor, which is exactly the data someone reviewing a drive wants.
	r.note("flush", r.w.Flush())
	if err := r.f.Close(); err != nil {
		r.log.Warn("recorder close before rotate failed", "path", r.path, "err", err)
	}
	_ = os.Rename(r.path, r.path+".1") // keep one backup
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.log.Warn("recorder rotate reopen failed", "err", err)
		return
	}
	r.f = f
	r.w = bufio.NewWriter(f)
	r.size = 0
	r.failing = false // a fresh file and a fresh writer: no sticky error
}

func (r *Recorder) flushLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		r.mu.Lock()
		if r.closed {
			// Stop ticking after Close. Flushing a closed file can only fail, so
			// the old unchecked loop ran forever discarding an error every 2s.
			r.mu.Unlock()
			return
		}
		r.note("flush", r.w.Flush())
		r.mu.Unlock()
	}
}

func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	// This flush is the one that matters: it is the only thing standing between
	// a clean shutdown and losing the buffered tail, so report it even though
	// there is nothing left to retry with.
	if err := r.w.Flush(); err != nil {
		r.log.Error("recorder final flush failed — buffered telemetry was lost", "path", r.path, "err", err)
	}
	if err := r.f.Close(); err != nil {
		r.log.Warn("recorder close failed", "path", r.path, "err", err)
	}
}
