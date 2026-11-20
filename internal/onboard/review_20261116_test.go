package onboard

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// same-site is a sibling origin (another subdomain), not this wizard. It
// carries cached Basic Auth just as cross-site does.
func TestServer_RejectsSameSitePost(t *testing.T) {
	s, dir := newTestServer(t)
	r := form(http.MethodPost, "/generate", nil)
	r.Header.Set("Sec-Fetch-Site", "same-site")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Errorf("same-site POST = %d, want 403", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "private-key.pem")); err == nil {
		t.Error("same-site POST generated a key")
	}
}

// The replace confirmation keyed off state.json alone. With that file lost or
// corrupt and the key files intact, /generate replaced the paired keypair.
func TestServer_GenerateConfirmsWhenKeyFileExistsWithoutState(t *testing.T) {
	s, dir := newTestServer(t)
	h := s.Handler()
	h.ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/generate", nil))
	key := filepath.Join(dir, "private-key.pem")
	first, err := os.ReadFile(key)
	if err != nil {
		t.Fatalf("no key generated: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := NewServer(Options{DataDir: dir}, s.log)
	if err != nil {
		t.Fatal(err)
	}
	s2.Handler().ServeHTTP(httptest.NewRecorder(), form(http.MethodPost, "/generate", nil))
	if now, _ := os.ReadFile(key); !bytes.Equal(now, first) {
		t.Fatal("an unconfirmed /generate replaced an existing private key")
	}
}

// With no token owner and no token_cache path the token went nowhere, and the
// wizard still said it was saved.
func TestPasteToken_NowhereToSaveIsAnError(t *testing.T) {
	s, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, form(http.MethodPost, "/token", url.Values{"refresh_token": {"rt"}}))
	if strings.Contains(rr.Body.String(), "Refresh token saved") || !strings.Contains(rr.Body.String(), "token_cache") {
		t.Fatalf("reported success with nowhere to store the token: %.300s", rr.Body.String())
	}
}

// atomicWrite used one fixed temp name, so two writers could interleave in it.
func TestAtomicWrite_ConcurrentWritersLeaveOneWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ftc.json")
	var wg sync.WaitGroup
	payloads := map[string]bool{}
	for i := 0; i < 16; i++ {
		b := []byte(strings.Repeat(fmt.Sprintf("%x", i), 4096*(i+1)))
		payloads[string(b)] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := atomicWrite(path, b, 0o600); err != nil {
				t.Errorf("atomicWrite: %v", err)
			}
		}()
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !payloads[string(got)] {
		t.Fatalf("file holds a mix of writes (%d bytes)", len(got))
	}
	if left, _ := filepath.Glob(path + "*.tmp"); len(left) > 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}
