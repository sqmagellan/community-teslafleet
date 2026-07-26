package config

import (
	"os"
	"path/filepath"
	"testing"
)

// commandsCfg is the minimum valid config with commands enabled, so each test
// varies only the credential source.
func commandsCfg(refresh, tokenCache string) Config {
	c := Defaults()
	c.Vehicles = []Vehicle{{VIN: "5YJYGDEF5LF001234"}}
	c.HA.Enabled = true
	c.HA.Broker = "tcp://broker:1883"
	c.Commands.Enabled = true
	c.Commands.ClientID = "client-id"
	c.Commands.RefreshToken = refresh
	c.Commands.TokenCache = tokenCache
	return c
}

func TestCommandsCredentialValidation(t *testing.T) {
	dir := t.TempDir()
	cached := filepath.Join(dir, "refresh_token")
	if err := os.WriteFile(cached, []byte("rotated-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty_token")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("seed token alone is valid", func(t *testing.T) {
		c := commandsCfg("seed", "")
		if err := c.validate(); err != nil {
			t.Errorf("validate() = %v, want nil", err)
		}
	})

	// The point of the change: once the token has rotated into the cache, the
	// stale seed can be deleted from the config that holds it.
	t.Run("cached token alone is valid", func(t *testing.T) {
		c := commandsCfg("", cached)
		if err := c.validate(); err != nil {
			t.Errorf("validate() = %v, want nil", err)
		}
	})

	t.Run("neither is invalid", func(t *testing.T) {
		c := commandsCfg("", "")
		if err := c.validate(); err == nil {
			t.Error("validate() = nil, want an error when there is no credential at all")
		}
	})

	t.Run("empty cache file does not count", func(t *testing.T) {
		c := commandsCfg("", empty)
		if err := c.validate(); err == nil {
			t.Error("validate() = nil, want an error for a zero-length cache file")
		}
	})

	t.Run("missing cache file does not count", func(t *testing.T) {
		c := commandsCfg("", filepath.Join(dir, "nope"))
		if err := c.validate(); err == nil {
			t.Error("validate() = nil, want an error for a missing cache file")
		}
	})

	t.Run("a directory is not a token cache", func(t *testing.T) {
		c := commandsCfg("", dir)
		if err := c.validate(); err == nil {
			t.Error("validate() = nil, want an error when token_cache is a directory")
		}
	})

	t.Run("client_id is still required", func(t *testing.T) {
		c := commandsCfg("seed", "")
		c.Commands.ClientID = ""
		if err := c.validate(); err == nil {
			t.Error("validate() = nil, want an error with no client_id")
		}
	})
}
