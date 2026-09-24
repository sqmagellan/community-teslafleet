package commands

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

type fakeLocator map[string]store.Snapshot

func (f fakeLocator) Snapshot(vin string) (store.Snapshot, bool) {
	s, ok := f[vin]
	return s, ok
}

func doors(frunk, trunk bool) fakeLocator {
	return fakeLocator{"VIN1": {Fields: map[string]store.FieldValue{
		store.FieldDoorState: {Value: map[string]any{"TrunkFront": frunk, "TrunkRear": trunk}},
	}}}
}

// actuate_trunk toggles. Before this check, any payload reached it, so a
// CLOSE for the frunk opened it.
func TestFrunkAndTrunkOnlyActuateWhenTheyWouldMove(t *testing.T) {
	cases := []struct {
		name    string
		loc     fakeLocator
		key     string
		payload string
		send    bool
	}{
		{"frunk close is impossible", doors(true, false), "frunk", "CLOSE", false},
		{"frunk open when open", doors(true, false), "frunk", "OPEN", false},
		{"frunk open when closed", doors(false, false), "frunk", "OPEN", true},
		{"frunk open, state unknown", fakeLocator{}, "frunk", "OPEN", true},
		{"frunk junk payload", doors(false, false), "frunk", "STOP", false},
		{"trunk open when closed", doors(false, false), "trunk", "OPEN", true},
		{"trunk open when open", doors(false, true), "trunk", "OPEN", false},
		{"trunk close when open", doors(false, true), "trunk", "CLOSE", true},
		{"trunk close when closed", doors(false, false), "trunk", "CLOSE", false},
		{"trunk, state unknown", fakeLocator{}, "trunk", "CLOSE", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, sent := relayTo(t)
			r.store = c.loc
			res := r.Handle("VIN1", c.key, c.payload)
			if got := sent() == "actuate_trunk"; got != c.send {
				t.Errorf("sent=%v, want %v (result %+v)", got, c.send, res)
			}
			if !c.send && (res.Outcome != OutcomeRejected || res.Reason == "") {
				t.Errorf("refusal reported as %+v", res)
			}
		})
	}
}

func TestWakeBudget(t *testing.T) {
	fastWake(t)
	f := newFakeCar()
	defer f.close()
	r := f.relay()
	r.maxWakes = 2

	for i := 0; i < 2; i++ {
		if res := r.Handle("VIN1", "wake", "PRESS"); res.Outcome != OutcomeSent {
			t.Fatalf("wake %d: %+v", i+1, res)
		}
	}
	res := r.Handle("VIN1", "wake", "PRESS")
	if res.Outcome != OutcomeRejected || !strings.Contains(res.Reason, "wake budget") {
		t.Errorf("third wake: %+v, want rejected by the budget", res)
	}
	if n := f.wakeCalls.Load(); n != 2 {
		t.Errorf("car was woken %d times, want 2", n)
	}

	// A command to a sleeping car needs a wake too, and must not get one.
	f.awake.Store(false)
	err := r.command("VIN1", "charge_start", nil)
	if !errors.Is(err, errWakeBudget) {
		t.Errorf("command to a sleeping car with no budget: %v, want errWakeBudget", err)
	}
	if n := f.wakeCalls.Load(); n != 2 {
		t.Errorf("car was woken %d times, want 2", n)
	}
}

func TestRateLimitedCommandIsRetriedOnce(t *testing.T) {
	old := maxRetryDelay
	maxRetryDelay = 2 * time.Second
	t.Cleanup(func() { maxRetryDelay = old })

	cases := []struct {
		name      string
		responses []func(http.ResponseWriter)
		wantErr   bool
		wantCalls int32
	}{
		{"retry-after header, then success", []func(http.ResponseWriter){
			func(w http.ResponseWriter) { w.Header().Set("Retry-After", "0"); w.WriteHeader(429) },
			func(w http.ResponseWriter) { fmt.Fprint(w, `{"response":{"result":true}}`) },
		}, false, 2},
		{"delay in the body, then success", []func(http.ResponseWriter){
			func(w http.ResponseWriter) { w.WriteHeader(429); fmt.Fprint(w, "Retry in 1 seconds") },
			func(w http.ResponseWriter) { fmt.Fprint(w, `{"response":{"result":true}}`) },
		}, false, 2},
		{"delay too long to wait out", []func(http.ResponseWriter){
			func(w http.ResponseWriter) { w.WriteHeader(429); fmt.Fprint(w, "Retry in 30 seconds") },
		}, true, 1},
		{"still limited after one retry", []func(http.ResponseWriter){
			func(w http.ResponseWriter) { w.Header().Set("Retry-After", "0"); w.WriteHeader(429) },
			func(w http.ResponseWriter) { w.Header().Set("Retry-After", "0"); w.WriteHeader(429) },
		}, true, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(calls.Add(1))
				if n > len(c.responses) {
					t.Errorf("unexpected call %d", n)
					return
				}
				c.responses[n-1](w)
			}))
			defer srv.Close()
			r, _ := relayTo(t)
			r.proxy = srv.URL
			err := r.wake("VIN1")
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr %v", err, c.wantErr)
			}
			if calls.Load() != c.wantCalls {
				t.Errorf("%d requests, want %d", calls.Load(), c.wantCalls)
			}
		})
	}
}

// Tesla rotates the refresh token on every use. If the new one cannot be
// written, the old one on disk is already spent, and the next restart
// strands the account. That must be visible, and retried.
func TestUnsavedRotatedTokenIsReportedAndRetried(t *testing.T) {
	a := newRotatingAuth("seed")
	defer a.close()

	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configCommandsFor(a.srv.URL, filepath.Join(notADir, "refresh_token"))
	cfg.RefreshToken = "seed"
	tm := newTokenManager(cfg, testLogger())

	if _, err := tm.token(); err != nil {
		t.Fatalf("token: %v", err)
	}
	if st := tm.status(); !strings.HasPrefix(st, "rotated refresh token not saved") {
		t.Fatalf("status after a failed save = %q", st)
	}

	good := filepath.Join(dir, "refresh_token")
	tm.cachePath = good
	if _, err := tm.token(); err != nil {
		t.Fatalf("token: %v", err)
	}
	if st := tm.status(); st != "ok" {
		t.Errorf("status after the retry = %q, want ok", st)
	}
	b, err := os.ReadFile(good)
	if err != nil || string(b) != "refresh-1" {
		t.Errorf("cache holds %q (%v), want the rotated token refresh-1", b, err)
	}
	if a.issued.Load() != 1 {
		t.Errorf("%d refreshes, want 1: the retry must not spend another token", a.issued.Load())
	}
}

func TestRetryDelay(t *testing.T) {
	h := http.Header{}
	if d, ok := retryDelay(h, []byte("Retry in 3 seconds")); !ok || d != 3*time.Second {
		t.Errorf("body delay: %v %v", d, ok)
	}
	h.Set("Retry-After", "2")
	if d, ok := retryDelay(h, []byte("Retry in 9 seconds")); !ok || d != 2*time.Second {
		t.Errorf("header must win: %v %v", d, ok)
	}
	if _, ok := retryDelay(http.Header{}, []byte("slow down")); ok {
		t.Error("no delay given, but retry allowed")
	}
}
