package vehicledata

import (
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Section timestamps are the observation time. TeslaMate stores a streamed
// position's date verbatim and compares drive_state.timestamp across the HTTP
// and WebSocket paths, so stamping render time on a frozen snapshot claimed a
// fresh fix at old coordinates every time the document was built.
func TestBuild_TimestampsAreObservationTime(t *testing.T) {
	obs := time.Unix(1_800_000_000, 0).UTC()
	render := obs.Add(2 * time.Hour)
	snap := store.Snapshot{
		VIN:    "V",
		LastV:  obs,
		Fields: map[string]store.FieldValue{store.FieldSoc: {Value: 61.0}},
	}
	resp := Build(snap, store.Derived{State: "online"}, config.Vehicle{VIN: "V"}, nil, config.Units{}, render)

	for _, section := range []string{"drive_state", "charge_state", "climate_state", "vehicle_state"} {
		got := resp[section].(map[string]any)["timestamp"]
		if got != obs.UnixMilli() {
			t.Errorf("%s.timestamp = %v, want %d (observation), not %d (render)",
				section, got, obs.UnixMilli(), render.UnixMilli())
		}
	}
}

// Two builds of an unchanged snapshot must produce the SAME timestamp. Both of
// TeslaMate's guards accept equality and reject only a strictly older fetch or
// a strictly newer stored value, so a repeat is safe -- but a moving one is not.
func TestBuild_UnchangedSnapshotRepeatsItsTimestamp(t *testing.T) {
	obs := time.Unix(1_800_000_000, 0).UTC()
	snap := store.Snapshot{VIN: "V", LastV: obs, Fields: map[string]store.FieldValue{}}
	veh := config.Vehicle{VIN: "V"}

	first := Build(snap, store.Derived{}, veh, nil, config.Units{}, obs.Add(time.Second))
	second := Build(snap, store.Derived{}, veh, nil, config.Units{}, obs.Add(time.Hour))

	a := first["drive_state"].(map[string]any)["timestamp"]
	b := second["drive_state"].(map[string]any)["timestamp"]
	if a != b {
		t.Errorf("timestamp advanced without new telemetry: %v then %v", a, b)
	}
}

// Nothing streamed yet: fall back to the render clock rather than emitting the
// zero time, which TeslaMate would read as 1970.
func TestBuild_NoTelemetryFallsBackToNow(t *testing.T) {
	render := time.Unix(1_800_000_000, 0).UTC()
	snap := store.Snapshot{VIN: "V", Fields: map[string]store.FieldValue{}}
	resp := Build(snap, store.Derived{}, config.Vehicle{VIN: "V"}, nil, config.Units{}, render)
	if got := resp["drive_state"].(map[string]any)["timestamp"]; got != render.UnixMilli() {
		t.Errorf("timestamp = %v, want the render clock %d", got, render.UnixMilli())
	}
}
