// Package streamcfg generates the fleet-telemetry SERVER config (config.json) for
// the embedded "all-in-one" add-on. This is the config the fleet-telemetry binary
// itself reads (listen host/port, namespace, record routing, ZMQ dispatcher, TLS) —
// distinct from the per-vehicle enrollment config the car receives (see internal/
// enroll). It is fully derivable from the gateway config, so the gateway owns it and
// regenerates it on boot.
package streamcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerConfig mirrors the fleet-telemetry server config schema.
type ServerConfig struct {
	Host                   string              `json:"host"`
	Port                   int                 `json:"port"`
	LogLevel               string              `json:"log_level"`
	JSONLogEnable          bool                `json:"json_log_enable"`
	Namespace              string              `json:"namespace"`
	ReliableAck            bool                `json:"reliable_ack"`
	TransmitDecodedRecords bool                `json:"transmit_decoded_records"`
	Records                map[string][]string `json:"records"`
	ZMQ                    ZMQ                 `json:"zmq"`
	TLS                    *TLS                `json:"tls,omitempty"`
}

type ZMQ struct {
	Addr string `json:"addr"`
}

type TLS struct {
	ServerCert string `json:"server_cert"`
	ServerKey  string `json:"server_key"`
}

// Build assembles the server config. connectivity + V records are routed to ZMQ
// (the gateway's brokerless ingest); alerts/errors go to the fleet-telemetry log.
// transmit_decoded_records=true makes payloads protojson, which the ingest parser
// expects. cert and key are the server certificate fleet-telemetry presents to
// the car; without them it has no TLS config and will not start.
func Build(namespace, zmqBind, cert, key string, port int) ServerConfig {
	c := ServerConfig{
		Host:                   "0.0.0.0",
		Port:                   port,
		LogLevel:               "info",
		JSONLogEnable:          true,
		Namespace:              namespace,
		ReliableAck:            false,
		TransmitDecodedRecords: true,
		Records: map[string][]string{
			"alerts":       {"logger"},
			"errors":       {"logger"},
			"connectivity": {"zmq"},
			"V":            {"zmq"},
		},
		ZMQ: ZMQ{Addr: zmqBind},
	}
	if cert != "" && key != "" {
		c.TLS = &TLS{ServerCert: cert, ServerKey: key}
	}
	return c
}

// Write marshals c to path (pretty-printed), creating the parent directory. The
// write is atomic (temp file + rename) so a reading fleet-telemetry never sees a
// half-written config.
func Write(path string, c ServerConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}
