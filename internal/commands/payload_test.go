package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// relayTo builds a relay pointed at a proxy that records the command name it is
// asked for, and reports what (if anything) reached the car.
func relayTo(t *testing.T) (*Relay, func() string) {
	t.Helper()
	var sent string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/1/vehicles/{vin}/command/{name}", func(w http.ResponseWriter, r *http.Request) {
		sent = r.PathValue("name")
		_, _ = w.Write([]byte(`{"response":{"result":true}}`))
	})
	mux.HandleFunc("POST /api/1/vehicles/{vin}/wake_up", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"state":"online"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r := &Relay{
		tm:        &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
		proxy:     srv.URL,
		client:    http.DefaultClient,
		apiClient: http.DefaultClient,
		log:       testLogger(),
		valetPIN:  "1234",
		speedPIN:  "1234",
		drivePIN:  "1234",
	}
	return r, func() string { return sent }
}

// Every actuator parses its payload explicitly. The lock bug was "anything that
// isn't LOCK means unlock"; the same shape sat under the switches, the covers
// and the selects, where an unparseable payload resolved to off / close / the
// zero-valued option and looked like a deliberate command.
func TestHandle_RejectsUnrecognizedPayloads(t *testing.T) {
	tests := []struct {
		key     string
		payload string
		want    string // "" = nothing must reach the car
	}{
		// switches
		{"charging", "ON", "charge_start"},
		{"charging", "OFF", "charge_stop"},
		{"charging", "", ""},
		{"charging", "PRESS", ""},
		{"sentry", "ON", "set_sentry_mode"},
		{"sentry", "armed", ""},
		{"guest_mode", "OFF", "guest_mode"},
		{"guest_mode", "\n", ""},
		{"pin_to_drive", "ON", "set_pin_to_drive"},
		{"pin_to_drive", "yes", ""},
		{"valet", "OFF", "set_valet_mode"},
		{"valet", "0", "set_valet_mode"},
		{"valet", "maybe", ""},
		// climate_mode used to start the climate for ANY payload but "off"
		{"climate_mode", "ON", "auto_conditioning_start"},
		{"climate_mode", "OFF", "auto_conditioning_stop"},
		{"climate_mode", "heat", ""},
		{"climate_mode", "", ""},
		// covers
		{"charge_port", "OPEN", "charge_port_door_open"},
		{"charge_port", "CLOSE", "charge_port_door_close"},
		{"charge_port", "STOP", ""},
		{"charge_port", "", ""},
		// selects: a map miss used to yield 0, which is a real option
		{"climate_keeper", "dog", "set_climate_keeper_mode"},
		{"climate_keeper", "kamp", ""},
		{"seat_heater_front_left", "high", "remote_seat_heater_request"},
		{"seat_heater_front_left", "hot", ""},
		// lock
		{"lock", "LOCK\n", "door_lock"},
		{"lock", "UNLOCK", "door_unlock"},
		{"lock", "LOKC", ""},
	}
	for _, tc := range tests {
		t.Run(tc.key+"/"+strings.ReplaceAll(tc.payload, "\n", "\\n"), func(t *testing.T) {
			r, sent := relayTo(t)
			r.Handle("VIN1", tc.key, tc.payload)
			if got := sent(); got != tc.want {
				t.Errorf("%s payload %q sent %q, want %q", tc.key, tc.payload, got, tc.want)
			}
		})
	}
}

func TestOnOff(t *testing.T) {
	for _, tc := range []struct {
		in      string
		on, ok  bool
	}{
		{"ON", true, true}, {"on", true, true}, {" TRUE ", true, true}, {"1", true, true},
		{"OFF", false, true}, {"false", false, true}, {"0", false, true},
		{"", false, false}, {"PRESS", false, false}, {"LOCK", false, false}, {"2", false, false},
	} {
		on, ok := onOff(tc.in)
		if on != tc.on || ok != tc.ok {
			t.Errorf("onOff(%q) = %v,%v want %v,%v", tc.in, on, ok, tc.on, tc.ok)
		}
	}
}
