package onboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeOwner stands in for the command relay.
type fakeOwner struct {
	minted   atomic.Int32
	adopted  atomic.Value // string
	mintErr  error
	adoptErr error
}

func (f *fakeOwner) AccessToken() (string, error) {
	if f.mintErr != nil {
		return "", f.mintErr
	}
	f.minted.Add(1)
	return "access-from-owner", nil
}

func (f *fakeOwner) SetRefreshToken(tok string) error {
	if f.adoptErr != nil {
		return f.adoptErr
	}
	f.adopted.Store(tok)
	return nil
}

// With a relay present it owns the credential. The wizard must not refresh
// alongside it: Tesla rotates the refresh token on every use, so the loser of
// that race spends a token the winner still holds.
func TestAccessToken_DelegatesToOwner(t *testing.T) {
	s, dir := newTestServer(t)
	owner := &fakeOwner{}
	s.opts.Tokens = owner
	// A stale cache file must be ignored entirely when an owner is present.
	s.opts.TokenCache = dir + "/refresh_token"

	got, err := s.accessToken()
	if err != nil {
		t.Fatalf("accessToken: %v", err)
	}
	if got != "access-from-owner" {
		t.Errorf("token = %q, want the owner's", got)
	}
	if owner.minted.Load() != 1 {
		t.Errorf("owner minted %d times, want 1", owner.minted.Load())
	}
}

// A pasted token goes through the owner, so the relay serves it immediately
// rather than the one it read at startup.
func TestPasteToken_AdoptedByOwner(t *testing.T) {
	s, _ := newTestServer(t)
	owner := &fakeOwner{}
	s.opts.Tokens = owner

	rr := httptest.NewRecorder()
	req := loopbackReq(http.MethodPost, "/token",
		strings.NewReader("refresh_token=pasted-by-operator"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Handler().ServeHTTP(rr, req)

	if got, _ := owner.adopted.Load().(string); got != "pasted-by-operator" {
		t.Errorf("owner adopted %q, want pasted-by-operator", got)
	}
	if body := rr.Body.String(); strings.Contains(body, "Restart the gateway") {
		t.Error("still telling the operator to restart when the relay adopted the token")
	}
}

// Standalone, with commands disabled, there is no relay to defer to and the
// wizard refreshes for itself.
func TestAccessToken_StandaloneNeedsAToken(t *testing.T) {
	dir := t.TempDir()
	s, err := NewServer(Options{DataDir: dir}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if _, err := s.accessToken(); err == nil {
		t.Error("want an error with no relay and no saved token")
	}
}
