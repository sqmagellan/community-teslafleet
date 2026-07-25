package recorder

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRecorder(t *testing.T, maxMB int) (*Recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	r, err := New(path, maxMB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Close must flush: the buffered tail is the whole point of recording a drive,
// and it used to be dropped whenever the flush failed silently.
func TestCloseFlushesBufferedTail(t *testing.T) {
	r, path := newTestRecorder(t, 100)
	r.Record("VIN", "Soc", 61.5)

	// Still buffered — the 2s flush ticker has not run.
	if got := read(t, path); got != "" {
		t.Logf("already flushed (harmless): %q", got)
	}
	r.Close()

	got := read(t, path)
	if !strings.Contains(got, `"field":"Soc"`) || !strings.Contains(got, `"vin":"VIN"`) {
		t.Errorf("recorded line = %q, want the Soc record", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("recorded line %q does not end in a newline", got)
	}
}

// Writing after Close used to hit a closed file and discard the error on every
// single telemetry field.
func TestRecordAfterCloseIsNoop(t *testing.T) {
	r, path := newTestRecorder(t, 100)
	r.Record("VIN", "Soc", 61.5)
	r.Close()
	before := read(t, path)

	r.Record("VIN", "Odometer", 12345.6)
	if after := read(t, path); after != before {
		t.Errorf("file changed after Close: %q -> %q", before, after)
	}
}

// Close is called from a defer in main and may also run on a shutdown path.
func TestCloseIsIdempotent(t *testing.T) {
	r, path := newTestRecorder(t, 100)
	r.Record("VIN", "Soc", 61.5)
	r.Close()
	r.Close() // must not panic or double-close the file
	if read(t, path) == "" {
		t.Error("file is empty after Close")
	}
}

// A nil *Recorder is the "recording disabled" case and every method must accept
// it: New returns (nil, nil) for an empty path.
func TestNilRecorderIsSafe(t *testing.T) {
	r, err := New("", 100, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if r != nil || err != nil {
		t.Fatalf("New(\"\") = (%v, %v), want (nil, nil)", r, err)
	}
	r.Record("VIN", "Soc", 61.5) // nil receiver
	r.Close()
}

// Rotation must flush the old file before renaming it, or the buffered tail of
// the rotated-out file is lost.
func TestRotateFlushesBeforeRename(t *testing.T) {
	r, path := newTestRecorder(t, 0) // maxMB<=0 is clamped, so force it below
	r.mu.Lock()
	r.maxBytes = 200
	r.mu.Unlock()

	for i := 0; i < 40; i++ {
		r.Record("VIN", "Soc", float64(i))
	}
	r.Close()

	rotated := path + ".1"
	if _, err := os.Stat(rotated); err != nil {
		t.Fatalf("no rotated file at %s: %v", rotated, err)
	}
	old := read(t, rotated)
	if !strings.Contains(old, `"field":"Soc"`) {
		t.Errorf("rotated file has no records: %q", old)
	}
	// Every line must be complete JSON: a rotation that renamed before flushing
	// would leave a truncated final line.
	for _, ln := range strings.Split(strings.TrimRight(old, "\n"), "\n") {
		if !strings.HasPrefix(ln, "{") || !strings.HasSuffix(ln, "}") {
			t.Errorf("truncated line in rotated file: %q", ln)
		}
	}
}
