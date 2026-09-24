package enroll

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNoVINs means there is no car to send the config to.
var ErrNoVINs = errors.New("no VINs to enroll")

// Wrap puts a bare fleet_telemetry_config in the envelope Tesla's
// vehicle-command proxy expects:
//
//	{"vins": ["..."], "config": {...}}
//
// The proxy unmarshals into struct{VINs []string; Config jwt.MapClaims}. A
// bare config leaves Config nil, and signing it panics the proxy (upstream
// issue #1). A payload that already has a "config" key is returned unchanged.
func Wrap(payload []byte, vins []string) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil {
		return nil, fmt.Errorf("parse fleet_telemetry_config: %w", err)
	}
	if _, wrapped := probe["config"]; wrapped {
		return payload, nil
	}
	if len(probe["fields"]) == 0 {
		return nil, fmt.Errorf("fleet_telemetry_config has no fields")
	}
	if len(vins) == 0 {
		return nil, ErrNoVINs
	}
	return json.Marshal(map[string]any{
		"vins":   vins,
		"config": json.RawMessage(payload),
	})
}

// HasCA reports whether a config, bare or wrapped, carries a non-empty "ca".
// Tesla rejects a config without one ("ca is not a valid PEM"), even for a
// publicly trusted certificate.
func HasCA(payload []byte) bool {
	var probe struct {
		CA     string `json:"ca"`
		Config *struct {
			CA string `json:"ca"`
		} `json:"config"`
	}
	if json.Unmarshal(payload, &probe) != nil {
		return false
	}
	if probe.Config != nil {
		return probe.Config.CA != ""
	}
	return probe.CA != ""
}
