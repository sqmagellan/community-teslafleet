package config

import "testing"

// Embedded mode now supervises the vehicle-command proxy on loopback, so the
// two listeners must not fight over a port. A clash surfaces at runtime as one
// of them crash-looping on bind, which is a slow way to learn about a typo.
func TestValidate_EmbeddedProxyPort(t *testing.T) {
	base := func() Config {
		c := Defaults()
		c.Ingest.ZMQAddr = "tcp://127.0.0.1:5284"
		c.Ingest.Namespace = "tesla_telemetry"
		c.Stream.Embedded = true
		c.HA.Enabled = true
		c.HA.Broker = "tcp://127.0.0.1:1883"
		c.Commands.Enabled = true
		c.Commands.ClientID = "id"
		c.Commands.RefreshToken = "seed"
		c.Commands.ProxyURL = "https://127.0.0.1:4444"
		return c
	}

	t.Run("defaults are valid", func(t *testing.T) {
		c := base()
		if err := c.validate(); err != nil {
			t.Errorf("validate: %v", err)
		}
	})

	t.Run("port collision is rejected", func(t *testing.T) {
		c := base()
		c.Stream.ProxyPort = c.Stream.TelemetryPort
		if err := c.validate(); err == nil {
			t.Error("want an error when proxy_port equals telemetry_port")
		}
	})

	t.Run("unset port is rejected", func(t *testing.T) {
		c := base()
		c.Stream.ProxyPort = 0
		if err := c.validate(); err == nil {
			t.Error("want an error when proxy_port is unset")
		}
	})

	t.Run("standalone is unaffected", func(t *testing.T) {
		c := base()
		c.Stream.Embedded = false
		c.Stream.ProxyPort = 0
		c.Commands.ProxyURL = "https://vehicle-command-proxy:4443"
		if err := c.validate(); err != nil {
			t.Errorf("validate: %v", err)
		}
	})
}
