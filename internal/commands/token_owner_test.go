package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// rotatingAuth is Tesla's actual behaviour: a refresh token is single-use, and
// replaying a spent one fails. This is the fixture that catches a second
// component refreshing the same credential.
type rotatingAuth struct {
	mu      sync.Mutex
	valid   string
	issued  atomic.Int32
	rejects atomic.Int32
	srv     *httptest.Server
}

func newRotatingAuth(seed string) *rotatingAuth {
	a := &rotatingAuth{valid: seed}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/v3/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got := r.Form.Get("refresh_token")
		a.mu.Lock()
		defer a.mu.Unlock()
		if got != a.valid {
			a.rejects.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"login_required"}`))
			return
		}
		n := a.issued.Add(1)
		a.valid = fmt.Sprintf("refresh-%d", n)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-%d", n),
			"refresh_token": a.valid,
			"expires_in":    28800,
		})
	})
	a.srv = httptest.NewServer(mux)
	return a
}

func (a *rotatingAuth) close() { a.srv.Close() }

func configCommandsFor(authHost, cache string) config.Commands {
	return config.Commands{
		AuthHost:   authHost,
		AuthPath:   "/oauth2/v3",
		ClientID:   "test-client",
		TokenCache: cache,
	}
}

func newRotatingTM(t *testing.T, a *rotatingAuth, seed string) (*tokenManager, string) {
	t.Helper()
	cache := filepath.Join(t.TempDir(), "refresh_token")
	if err := os.WriteFile(cache, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := newTokenManager(configCommandsFor(a.srv.URL, cache), testLogger())
	return tm, cache
}

// A rotated token must be on disk before the next caller reads it, or a restart
// replays a spent credential.
func TestTokenManager_PersistsRotation(t *testing.T) {
	a := newRotatingAuth("seed")
	defer a.close()
	tm, cache := newRotatingTM(t, a, "seed")

	if _, err := tm.token(); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	onDisk, _ := os.ReadFile(cache)
	if string(onDisk) == "seed" {
		t.Fatal("rotated refresh token was not persisted")
	}

	// A restart reads the file and must still authenticate.
	tm2 := newTokenManager(configCommandsFor(a.srv.URL, cache), testLogger())
	if _, err := tm2.token(); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}
	if a.rejects.Load() != 0 {
		t.Errorf("auth server rejected %d spent token(s)", a.rejects.Load())
	}
}

// The whole point of a single owner: concurrent callers must serialize onto one
// refresh, not race each other into spending the same token twice.
func TestTokenManager_ConcurrentCallersDoNotDoubleSpend(t *testing.T) {
	a := newRotatingAuth("seed")
	defer a.close()
	tm, _ := newRotatingTM(t, a, "seed")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = tm.token()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if got := a.issued.Load(); got != 1 {
		t.Errorf("issued %d tokens for %d concurrent callers, want 1", got, n)
	}
	if a.rejects.Load() != 0 {
		t.Errorf("auth server rejected %d spent token(s)", a.rejects.Load())
	}
}

// A pasted token has to take effect without a restart, and must invalidate the
// access token minted from the old one.
func TestRelay_SetRefreshToken(t *testing.T) {
	a := newRotatingAuth("seed")
	defer a.close()
	tm, cache := newRotatingTM(t, a, "seed")
	r := &Relay{tm: tm, log: testLogger()}

	if _, err := r.AccessToken(); err != nil {
		t.Fatalf("initial: %v", err)
	}
	a.mu.Lock()
	a.valid = "operator-pasted"
	a.mu.Unlock()

	if err := r.SetRefreshToken("  operator-pasted\n"); err != nil {
		t.Fatalf("SetRefreshToken: %v", err)
	}
	if onDisk, _ := os.ReadFile(cache); string(onDisk) != "operator-pasted" {
		t.Errorf("cache = %q, want the pasted token trimmed", onDisk)
	}
	if _, err := r.AccessToken(); err != nil {
		t.Fatalf("after paste: %v", err)
	}
	if a.rejects.Load() != 0 {
		t.Errorf("auth server rejected %d token(s)", a.rejects.Load())
	}
	if err := r.SetRefreshToken("   "); err == nil {
		t.Error("SetRefreshToken accepted an empty token")
	}
}
