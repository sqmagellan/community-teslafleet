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
// issue #1). A payload that is already wrapped is checked the same way and
// returned unchanged; its own VINs are used.
func Wrap(payload []byte, vins []string) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil {
		return nil, fmt.Errorf("parse fleet_telemetry_config: %w", err)
	}
	if cfg, wrapped := probe["config"]; wrapped {
		var env struct {
			VINs []string `json:"vins"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			return nil, fmt.Errorf("parse fleet_telemetry_config: %w", err)
		}
		if !hasFields(cfg) {
			return nil, fmt.Errorf("fleet_telemetry_config has no fields")
		}
		if len(env.VINs) == 0 {
			return nil, ErrNoVINs
		}
		return payload, nil
	}
	if !hasFields(payload) {
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

// hasFields reports whether cfg is a JSON object with a non-empty "fields" object.
func hasFields(cfg json.RawMessage) bool {
	var c struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	return json.Unmarshal(cfg, &c) == nil && len(c.Fields) > 0
}

// CA returns the "ca" of a config, bare or wrapped, or "" if it has none.
func CA(payload []byte) string {
	var probe struct {
		CA     string `json:"ca"`
		Config *struct {
			CA string `json:"ca"`
		} `json:"config"`
	}
	if json.Unmarshal(payload, &probe) != nil {
		return ""
	}
	if probe.Config != nil {
		return probe.Config.CA
	}
	return probe.CA
}

// HasCA reports whether a config, bare or wrapped, carries a non-empty "ca".
// Tesla rejects a config without one ("ca is not a valid PEM"), even for a
// publicly trusted certificate.
func HasCA(payload []byte) bool {
	return CA(payload) != ""
}
