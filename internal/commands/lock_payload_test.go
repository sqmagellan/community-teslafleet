package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The lock handler used to read "anything that isn't LOCK" as unlock. A
// trailing newline from `mosquitto_pub -m LOCK` — or any typo, or an empty
// retained payload — therefore unlocked a real car.
func TestHandleLock_RejectsUnrecognizedPayloads(t *testing.T) {
	tests := []struct {
		payload string
		want    string // "" = no request must reach the car
	}{
		{"LOCK", "door_lock"},
		{"lock", "door_lock"},
		{"LOCK\n", "door_lock"},
		{" UNLOCK ", "door_unlock"},
		{"unlock", "door_unlock"},
		{"", ""},
		{"LOKC", ""},
		{"OFF", ""},
		{"true", ""},
	}
	for _, tc := range tests {
		t.Run(strings.ReplaceAll(tc.payload, "\n", "\\n"), func(t *testing.T) {
			f := newFakeCar()
			defer f.close()
			f.awake.Store(true)

			var sent string
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/1/vehicles/{vin}/command/{name}", func(w http.ResponseWriter, r *http.Request) {
				sent = r.PathValue("name")
				_, _ = w.Write([]byte(`{"response":{"result":true}}`))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			r := &Relay{
				tm:        &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
				proxy:     srv.URL,
				client:    http.DefaultClient,
				apiClient: http.DefaultClient,
				fleetAPI:  f.fleet.URL,
				log:       testLogger(),
			}
			r.Handle("VIN1", "lock", tc.payload)

			if sent != tc.want {
				t.Errorf("payload %q sent %q, want %q", tc.payload, sent, tc.want)
			}
		})
	}
}
