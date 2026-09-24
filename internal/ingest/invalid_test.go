package ingest

import (
	"io"
	"log/slog"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// A field the car reports as invalid used to be skipped, so HA and TeslaMate
// kept its last good value forever.
func TestInvalidValueClearsTheField(t *testing.T) {
	st := store.New("VIN1")
	c := &Consumer{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), seenVINs: map[string]bool{}}

	c.handleV([]byte(`{"vin":"VIN1","data":[{"key":"MilesToArrival","value":{"doubleValue":12.5}}]}`))
	if snap, _ := st.Snapshot("VIN1"); !hasNum(snap, "MilesToArrival", 12.5) {
		t.Fatal("valid value not stored")
	}

	c.handleV([]byte(`{"vin":"VIN1","data":[{"key":"MilesToArrival","value":{"invalid":true}}]}`))
	snap, _ := st.Snapshot("VIN1")
	if _, ok := snap.Field("MilesToArrival"); ok {
		t.Error("an invalid value left the old one in place")
	}
	if _, ok := snap.Num("MilesToArrival"); ok {
		t.Error("Num still reads a value for an invalid field")
	}
}

func hasNum(s store.Snapshot, f string, want float64) bool {
	v, ok := s.Num(f)
	return ok && v == want
}
