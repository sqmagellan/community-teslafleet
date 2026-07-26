package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// DetectSupervisorMQTT auto-configures the HA MQTT broker when running as a Home
// Assistant add-on. It is a no-op unless SUPERVISOR_TOKEN is set (i.e. not an add-on)
// or the broker is already configured via env. On success it sets TGW_HA_BROKER (and
// username/password) so the normal env-load picks them up — HA-only users need not
// enter broker details. Requires `hassio_api: true` + `services: [mqtt:want]` in the
// add-on manifest. Best-effort: any error leaves config untouched.
func DetectSupervisorMQTT() {
	tok := os.Getenv("SUPERVISOR_TOKEN")
	if tok == "" {
		return // not running as an HA add-on
	}
	if os.Getenv("TGW_HA_BROKER") != "" {
		return // user configured the broker explicitly — respect it
	}
	req, err := http.NewRequest(http.MethodGet, "http://supervisor/services/mqtt", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		slog.Warn("supervisor mqtt lookup failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("supervisor mqtt unavailable", "status", resp.StatusCode)
		return
	}
	var out struct {
		Data struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			SSL      bool   `json:"ssl"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data.Host == "" {
		return
	}
	scheme := "tcp"
	if out.Data.SSL {
		scheme = "ssl"
	}
	port := out.Data.Port
	if port == 0 {
		port = 1883
	}
	// These Setenv calls ARE the output of this function -- the normal env load
	// reads them back. If one fails the add-on silently starts with no broker and
	// the user sees "MQTT not configured" having configured nothing wrong, so a
	// failure has to be loud rather than best-effort.
	setenv := func(k, v string) bool {
		if err := os.Setenv(k, v); err != nil {
			slog.Error("failed to set auto-detected MQTT env var", "key", k, "err", err)
			return false
		}
		return true
	}
	if !setenv("TGW_HA_BROKER", fmt.Sprintf("%s://%s:%d", scheme, out.Data.Host, port)) {
		return
	}
	if out.Data.Username != "" && !setenv("TGW_HA_USERNAME", out.Data.Username) {
		return
	}
	if out.Data.Password != "" && !setenv("TGW_HA_PASSWORD", out.Data.Password) {
		return
	}
	slog.Info("auto-configured MQTT from HA Supervisor", "host", out.Data.Host, "port", port, "ssl", out.Data.SSL)
}
