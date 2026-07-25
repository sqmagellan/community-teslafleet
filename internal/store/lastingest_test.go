package store

import (
	"sync"
	"testing"
	"time"
)

func TestLastIngestAcrossVehicles(t *testing.T) {
	st := New("VIN1", "VIN2")
	base := time.Unix(1_800_000_000, 0).UTC()
	clock := base
	st.SetClock(func() time.Time { return clock })

	if got := st.LastIngest(); !got.IsZero() {
		t.Errorf("LastIngest = %v on a fresh store, want the zero time", got)
	}

	st.SetField("VIN1", FieldSoc, float64(61))
	if got := st.LastIngest(); !got.Equal(base) {
		t.Errorf("LastIngest = %v, want %v", got, base)
	}

	// The fleet-wide answer is the most RECENT vehicle, not the oldest: one car
	// still streaming means ingest is alive even if the other has been asleep
	// for hours.
	clock = base.Add(time.Hour)
	st.SetField("VIN2", FieldSoc, float64(74))
	if got := st.LastIngest(); !got.Equal(clock) {
		t.Errorf("LastIngest = %v, want the newer %v", got, clock)
	}

	// An older update must not move it backwards.
	clock = base.Add(30 * time.Minute)
	st.SetField("VIN1", FieldSoc, float64(60))
	if want := base.Add(time.Hour); !st.LastIngest().Equal(want) {
		t.Errorf("LastIngest = %v after an older write, want %v", st.LastIngest(), want)
	}
}

// LastIngest is read by HTTP handlers while the ingest goroutine writes, so it
// has to be safe under -race. It takes the store lock and each vehicle lock in
// sequence rather than together; this asserts that ordering does not deadlock.
func TestLastIngestConcurrent(t *testing.T) {
	st := New() // no allow-list: writers auto-register VINs
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		vin := string(rune('A' + i))
		go func() {
			defer wg.Done()
			for range 200 {
				st.SetField(vin, FieldSoc, float64(i))
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = st.LastIngest()
			}
		}()
	}
	wg.Wait()
	if st.LastIngest().IsZero() {
		t.Error("LastIngest is zero after concurrent writes")
	}
}
