package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tesla's vehicle-command proxy unmarshals this endpoint's body into
// `struct{ VINs []string; Config jwt.MapClaims }`. A bare config leaves Config
// nil, and the proxy hands that nil map to SignMessage, which assigns into it
// and panics — the connection dies mid-response and the only symptom here is an
// unexplained EOF. Measured against tesla/vehicle-command:latest, 2026-09-06.
func TestEnrollBody_WrapsBareConfig(t *testing.T) {
	bare := []byte(`{"hostname":"telemetry.example.com","port":8443,"fields":{"Soc":{"interval_seconds":15}}}`)

	out, err := enrollBody(bare, []string{"VINB", "VINA"})
	if err != nil {
		t.Fatalf("enrollBody: %v", err)
	}
	var got struct {
		VINs   []string       `json:"vins"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result is not the envelope: %v", err)
	}
	if len(got.Config) == 0 {
		t.Fatal("config is empty — this is the shape that panics the proxy")
	}
	if got.Config["hostname"] != "telemetry.example.com" {
		t.Errorf("hostname = %v", got.Config["hostname"])
	}
	if strings.Join(got.VINs, ",") != "VINB,VINA" {
		t.Errorf("vins = %v, want the caller's order preserved", got.VINs)
	}
}

// The VIN set is a map, so iteration order is random. Sorting it is what keeps
// the signed config byte-identical between two runs with no change.
func TestVinList_Sorted(t *testing.T) {
	r := &Relay{knownVINs: map[string]bool{"VINC": true, "VINA": true, "VINB": true}}
	if got := strings.Join(r.vinList(), ","); got != "VINA,VINB,VINC" {
		t.Errorf("vinList = %s, want sorted", got)
	}
}

// A config already in envelope form — copied from Tesla's docs, say — must pass
// through untouched rather than being wrapped twice.
func TestEnrollBody_PassesThroughWrapped(t *testing.T) {
	wrapped := []byte(`{"vins":["VIN1"],"config":{"hostname":"h","fields":{"Soc":{}}}}`)
	out, err := enrollBody(wrapped, []string{"OTHER"})
	if err != nil {
		t.Fatalf("enrollBody: %v", err)
	}
	if string(out) != string(wrapped) {
		t.Errorf("wrapped payload was modified:\n got %s\nwant %s", out, wrapped)
	}
}

func TestEnrollBody_Rejects(t *testing.T) {
	bare := []byte(`{"hostname":"h","fields":{"Soc":{}}}`)
	if _, err := enrollBody(bare, nil); err == nil {
		t.Error("want an error with no VINs: the proxy would sign a config for nothing")
	}
	if _, err := enrollBody([]byte(`{"hostname":"h"}`), []string{"VIN1"}); err == nil {
		t.Error("want an error for a config with no fields")
	}
	if _, err := enrollBody([]byte(`not json`), []string{"VIN1"}); err == nil {
		t.Error("want an error for unparseable JSON")
	}
}

// End to end against a stand-in proxy: assert the bytes on the wire are the
// envelope, since that is what the real proxy crashes without.
func TestEnroll_SendsEnvelope(t *testing.T) {
	var seen []byte
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/1/vehicles/fleet_telemetry_config", func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"response":{"updated_vehicles":2}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &Relay{
		tm:        &tokenManager{access: "tok", expiry: time.Now().Add(time.Hour), log: testLogger(), client: http.DefaultClient},
		proxy:     srv.URL,
		client:    http.DefaultClient,
		log:       testLogger(),
		knownVINs: map[string]bool{"VIN1": true, "VIN2": true},
	}
	if err := r.Enroll([]byte(`{"hostname":"h","port":8443,"fields":{"Soc":{"interval_seconds":15}}}`)); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(seen, &got); err != nil {
		t.Fatalf("body sent is not JSON: %v", err)
	}
	if _, ok := got["vins"]; !ok {
		t.Error("no vins in the body — the proxy signs a config for no vehicle")
	}
	if _, ok := got["config"]; !ok {
		t.Error("no config in the body — this is the nil map that panics the proxy")
	}
}
