package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// persistedVehicle is the on-disk form of one vehicle's last-known state.
type persistedVehicle struct {
	Fields       map[string]FieldValue `json:"fields"`
	Connectivity string                `json:"connectivity,omitempty"`
	ConnAt       time.Time             `json:"conn_at,omitempty"`
	LastV        time.Time             `json:"last_v,omitempty"`
}

type persistedState struct {
	Vehicles map[string]persistedVehicle `json:"vehicles"`
}

// Save atomically writes the store's current field values to path. A no-op for
// an empty path. Field values round-trip through JSON as float64/string/bool/
// map — the same shapes the ingest produces — so restored fields behave
// identically to freshly-streamed ones.
func (s *Store) Save(path string) error {
	if path == "" {
		return nil
	}
	ps := persistedState{Vehicles: map[string]persistedVehicle{}}
	for _, vin := range s.VINs() {
		snap, ok := s.Snapshot(vin)
		if !ok || len(snap.Fields) == 0 {
			continue // nothing worth persisting yet
		}
		ps.Vehicles[vin] = persistedVehicle{
			Fields:       snap.Fields,
			Connectivity: snap.Connectivity,
			ConnAt:       snap.ConnAt,
			LastV:        snap.LastV,
		}
	}
	data, err := json.Marshal(ps)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path) // atomic replace
}

// Load restores field values previously written by Save, merging them into the
// store. VINs excluded by the allow-list are skipped. A missing file is not an
// error (first run). Returns the number of vehicles restored.
func (s *Store) Load(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var ps persistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		return 0, err
	}
	n := 0
	for vin, pv := range ps.Vehicles {
		v := s.getOrCreate(vin)
		if v == nil {
			continue // excluded by allow-list
		}
		v.mu.Lock()
		for name, fv := range pv.Fields {
			v.fields[name] = fv
		}
		// Restore LastV/connectivity so Derive still reports the car as
		// asleep/offline after a restart (the timestamps are old on purpose).
		if pv.Connectivity != "" {
			v.connectivity = pv.Connectivity
			v.connAt = pv.ConnAt
		}
		if !pv.LastV.IsZero() {
			v.lastV = pv.LastV
		}
		v.mu.Unlock()
		n++
	}
	return n, nil
}

// Persist periodically saves the store to path until ctx is cancelled, then
// saves once more on the way out. Runs in its own goroutine. A no-op for an
// empty path or non-positive interval.
func (s *Store) Persist(ctx context.Context, path string, interval time.Duration, log *slog.Logger) {
	if path == "" || interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := s.Save(path); err != nil {
				log.Warn("state snapshot save failed on shutdown", "path", path, "err", err)
			}
			return
		case <-t.C:
			if err := s.Save(path); err != nil {
				log.Warn("state snapshot save failed", "path", path, "err", err)
			}
		}
	}
}
