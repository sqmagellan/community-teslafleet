package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The point of _FILE is that a credential never has to be visible in the
// process environment, so these tests assert on precedence and on the failure
// modes, which is where a secret-loading mechanism actually hurts you.
func TestSetStrFromFile(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("reads the file", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE_FILE", write("plain", "s3cret"))
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if got != "s3cret" {
			t.Errorf("got %q, want s3cret", got)
		}
	})

	t.Run("strips the trailing newline echo adds", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE_FILE", write("nl", "s3cret\n"))
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if got != "s3cret" {
			t.Errorf("got %q, want s3cret with the newline stripped", got)
		}
	})

	t.Run("keeps a trailing space, which may be part of a password", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE_FILE", write("sp", "s3cret \n"))
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if got != "s3cret " {
			t.Errorf("got %q, want %q", got, "s3cret ")
		}
	})

	// The migration case: the inline value is still there but is being removed.
	// Reading it instead of the file would make the removal a no-op that looks
	// like it worked.
	t.Run("file wins over the plain env var", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE", "stale-inline-value")
		t.Setenv("TGW_TEST_VALUE_FILE", write("wins", "from-file"))
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if got != "from-file" {
			t.Errorf("got %q, want from-file", got)
		}
	})

	// An explicitly configured _FILE that cannot be read is FATAL. Falling back
	// to the plain variable is fail-open for a credential: an unreadable
	// TGW_DEBUG_TOKEN_FILE left the token empty, and an empty token means allow.
	t.Run("unreadable file is an error, not a fallback", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE", "fallback")
		t.Setenv("TGW_TEST_VALUE_FILE", filepath.Join(dir, "does-not-exist"))
		fileErr = nil
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if fileErr == nil {
			t.Error("want an error for an unreadable _FILE, got none")
		}
		if got != "" {
			t.Errorf("got %q, want the value left unset", got)
		}
	})

	t.Run("unreadable file with no plain env var also errors", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE_FILE", filepath.Join(dir, "does-not-exist"))
		fileErr = nil
		got := "default"
		setStr(&got, "TGW_TEST_VALUE")
		if fileErr == nil {
			t.Error("want an error for an unreadable _FILE, got none")
		}
		if got != "default" {
			t.Errorf("got %q, want the default to survive", got)
		}
	})

	t.Run("empty _FILE is ignored", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE", "plain")
		t.Setenv("TGW_TEST_VALUE_FILE", "")
		var got string
		setStr(&got, "TGW_TEST_VALUE")
		if got != "plain" {
			t.Errorf("got %q, want plain", got)
		}
	})

	t.Run("an empty file is a real empty value", func(t *testing.T) {
		t.Setenv("TGW_TEST_VALUE_FILE", write("empty", ""))
		got := "default"
		setStr(&got, "TGW_TEST_VALUE")
		if got != "" {
			t.Errorf("got %q, want the empty file to clear the value", got)
		}
	})
}
