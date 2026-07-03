package commands

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestIsAsleepErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"observed proxy 500", fmt.Errorf(`status 500: {"response":null,"error":"vehicle unavailable: vehicle is offline or asleep","error_description":""}`), true},
		{"bad request", fmt.Errorf("status 400: invalid_request"), false},
		{"success masquerade", fmt.Errorf("some other failure"), false},
	}
	for _, c := range cases {
		if got := isAsleepErr(c.err); got != c.want {
			t.Errorf("%s: isAsleepErr = %v, want %v", c.name, got, c.want)
		}
	}
}

// fakeCar is an httptest fixture emulating the vehicle-command proxy + Fleet API:
// commands fail with the "offline or asleep" error until wake_up flips it online.
type fakeCar struct {
	awake        atomic.Bool
	wakeCalls    atomic.Int32
	commandCalls atomic.Int32
	proxy        *httptest.Server
	fleet        *httptest.Server
}

func newFakeCar() *fakeCar {
	f := &fakeCar{}

	proxyMux := http.NewServeMux()
	proxyMux.HandleFunc("POST /api/1/vehicles/{vin}/wake_up", func(w http.ResponseWriter, _ *http.Request) {
		f.wakeCalls.Add(1)
		f.awake.Store(true)
		fmt.Fprint(w, `{"response":{"state":"online"}}`)
	})
	proxyMux.HandleFunc("POST /api/1/vehicles/{vin}/command/{name}", func(w http.ResponseWriter, _ *http.Request) {
		f.commandCalls.Add(1)
		if !f.awake.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"response":null,"error":"vehicle unavailable: vehicle is offline or asleep","error_description":""}`)
			return
		}
		fmt.Fprint(w, `{"response":{"result":true}}`)
	})
	f.proxy = httptest.NewServer(proxyMux)

	fleetMux := http.NewServeMux()
	fleetMux.HandleFunc("GET /api/1/vehicles/{vin}", func(w http.ResponseWriter, _ *http.Request) {
		state := "asleep"
		if f.awake.Load() {
			state = "online"
		}
		fmt.Fprintf(w, `{"response":{"state":%q,"display_name":"Serenity"}}`, state)
	})
	f.fleet = httptest.NewServer(fleetMux)
	return f
}

func (f *fakeCar) close() {
	f.proxy.Close()
	f.fleet.Close()
}

func (f *fakeCar) relay() *Relay {
	return &Relay{
		tm:        &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
		proxy:     f.proxy.URL,
		client:    http.DefaultClient,
		apiClient: http.DefaultClient,
		fleetAPI:  f.fleet.URL,
		log:       testLogger(),
	}
}

// fastWake shrinks the poll timing for tests and restores it on cleanup.
func fastWake(t *testing.T) {
	t.Helper()
	pi, to, buf := wakePollInterval, wakeTimeout, wakeReadyBuffer
	wakePollInterval, wakeTimeout, wakeReadyBuffer = 20*time.Millisecond, 2*time.Second, 10*time.Millisecond
	t.Cleanup(func() { wakePollInterval, wakeTimeout, wakeReadyBuffer = pi, to, buf })
}

func TestCommandWakesSleepingCarThenRetries(t *testing.T) {
	fastWake(t)
	f := newFakeCar()
	defer f.close()

	if err := f.relay().command("VIN1", "charge_start", nil); err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if got := f.wakeCalls.Load(); got != 1 {
		t.Errorf("wake_up called %d times, want 1", got)
	}
	if got := f.commandCalls.Load(); got != 2 {
		t.Errorf("command called %d times (want 2: initial asleep failure + retry)", got)
	}
}

func TestConcurrentCommandsCoalesceIntoOneWake(t *testing.T) {
	fastWake(t)
	f := newFakeCar()
	defer f.close()

	const n = 5
	r := f.relay()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = r.command("VIN1", "charge_start", nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("command %d returned error: %v", i, err)
		}
	}
	if got := f.wakeCalls.Load(); got != 1 {
		t.Errorf("a burst of %d commands triggered %d wakes, want 1 (single-flight)", n, got)
	}
}

func TestCommandNonSleepErrorIsNotRetried(t *testing.T) {
	fastWake(t)
	f := newFakeCar()
	defer f.close()
	f.awake.Store(true) // car is awake

	// Swap in a proxy that always 400s — a non-sleep failure must not wake or retry.
	badMux := http.NewServeMux()
	badMux.HandleFunc("POST /api/1/vehicles/{vin}/command/{name}", func(w http.ResponseWriter, _ *http.Request) {
		f.commandCalls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_command"}`)
	})
	bad := httptest.NewServer(badMux)
	defer bad.Close()

	r := f.relay()
	r.proxy = bad.URL

	if err := r.command("VIN1", "charge_start", nil); err == nil {
		t.Fatal("expected error for a 400 command, got nil")
	}
	if got := f.wakeCalls.Load(); got != 0 {
		t.Errorf("non-sleep error triggered %d wakes, want 0", got)
	}
	if got := f.commandCalls.Load(); got != 1 {
		t.Errorf("non-sleep error caused %d command attempts, want 1 (no retry)", got)
	}
}
