package onboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Sign in with Tesla: the wizard runs the authorization-code flow itself, so
// the operator never handles a refresh token or the client secret. Tesla
// redirects the browser to the app's registered redirect URI; that page need
// not exist, the operator pastes the address back into the wizard, and the
// wizard exchanges the code.

const testRedirect = "https://keys.example.com/oauth/callback"

type fakeTokenEndpoint struct {
	mu    sync.Mutex
	forms []url.Values
}

func (f *fakeTokenEndpoint) calls() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.forms...)
}

func newSignInServer(t *testing.T, opts Options) (*Server, *fakeTokenEndpoint) {
	t.Helper()
	fake := &fakeTokenEndpoint{}
	tesla := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/v3/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		fake.mu.Lock()
		fake.forms = append(fake.forms, r.PostForm)
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "access-1", "refresh_token": "refresh-1",
		})
	}))
	t.Cleanup(tesla.Close)
	dir := t.TempDir()
	opts.DataDir = dir
	opts.AuthHost = tesla.URL
	opts.FleetAPI = "https://fleet-api.example.com"
	opts.ClientID = "client-1"
	opts.ClientSecret = "secret-1"
	opts.TokenCache = filepath.Join(dir, "refresh_token")
	if opts.RedirectURI == "" {
		opts.RedirectURI = testRedirect
	}
	s, err := NewServer(opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s, fake
}

func postForm(s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	r := loopbackReq(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// startSignIn posts the sign-in button and returns Tesla's authorize URL.
func startSignIn(t *testing.T, s *Server) *url.URL {
	t.Helper()
	w := postForm(s, "/signin", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /signin = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	return u
}

func finish(s *Server, redirected string) *httptest.ResponseRecorder {
	return postForm(s, "/signin/finish", url.Values{"redirected_url": {redirected}})
}

func callback(state string) string {
	return testRedirect + "?locale=en-US&code=code-1&state=" + url.QueryEscape(state) +
		"&issuer=https%3A%2F%2Fauth.tesla.com%2Foauth2%2Fv3"
}

func savedToken(t *testing.T, s *Server) string {
	t.Helper()
	b, err := os.ReadFile(s.opts.TokenCache)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestSignInRedirectsToTeslaAuthorize(t *testing.T) {
	s, _ := newSignInServer(t, Options{})
	u := startSignIn(t, s)
	if u.Path != "/oauth2/v3/authorize" {
		t.Errorf("authorize path = %q", u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code",
		"client_id":     "client-1",
		"redirect_uri":  testRedirect,
		"scope":         "openid offline_access vehicle_device_data vehicle_location",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(q.Get("state")) < 32 {
		t.Errorf("state %q is too short to be unguessable", q.Get("state"))
	}
}

// With commands on, the token also drives the command relay, so it asks for
// the command scopes; read-only it never holds them.
func TestSignInAsksForCommandScopesOnlyWithCommands(t *testing.T) {
	s, _ := newSignInServer(t, Options{Commands: true})
	got := startSignIn(t, s).Query().Get("scope")
	want := "openid offline_access vehicle_device_data vehicle_location vehicle_cmds vehicle_charging_cmds"
	if got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
}

func TestSignInStateIsNewEachTime(t *testing.T) {
	s, _ := newSignInServer(t, Options{})
	a := startSignIn(t, s).Query().Get("state")
	b := startSignIn(t, s).Query().Get("state")
	if a == b {
		t.Error("two sign-ins share a state")
	}
}

func TestSignInFinishExchangesCodeAndSavesToken(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	state := startSignIn(t, s).Query().Get("state")
	w := finish(s, callback(state))
	if !strings.Contains(w.Body.String(), "✓") {
		t.Fatalf("finish did not report success: %s", w.Body.String())
	}
	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("token endpoint called %d times, want 1", len(calls))
	}
	for k, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "code-1",
		"redirect_uri":  testRedirect,
		"client_id":     "client-1",
		"client_secret": "secret-1",
		"audience":      "https://fleet-api.example.com",
	} {
		if got := calls[0].Get(k); got != want {
			t.Errorf("token form %s = %q, want %q", k, got, want)
		}
	}
	if got := savedToken(t, s); got != "refresh-1" {
		t.Errorf("saved refresh token = %q, want refresh-1", got)
	}
	if !s.store.st.TokenSet {
		t.Error("step 5 not marked done")
	}
	if strings.Contains(w.Body.String(), "refresh-1") || strings.Contains(w.Body.String(), "code-1") {
		t.Error("the page shows the token or the code")
	}
}

// The state is single use: the same address pasted twice exchanges once.
func TestSignInStateIsSingleUse(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	state := startSignIn(t, s).Query().Get("state")
	finish(s, callback(state))
	w := finish(s, callback(state))
	if strings.Contains(w.Body.String(), "✓") {
		t.Error("a used state was accepted again")
	}
	if n := len(fake.calls()); n != 1 {
		t.Errorf("token endpoint called %d times, want 1", n)
	}
}

func TestSignInRefusesForeignState(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	state := startSignIn(t, s).Query().Get("state")
	finish(s, callback("not-"+state))
	// A mismatch spends the pending sign-in, so a guess cannot be retried.
	w := finish(s, callback(state))
	if strings.Contains(w.Body.String(), "✓") {
		t.Error("the sign-in survived a mismatched state")
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("token endpoint called %d times, want 0", n)
	}
	if savedToken(t, s) != "" {
		t.Error("a token was saved")
	}
}

func TestSignInRefusesExpiredState(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	now := time.Now()
	s.now = func() time.Time { return now }
	state := startSignIn(t, s).Query().Get("state")
	s.now = func() time.Time { return now.Add(signInTTL + time.Second) }
	w := finish(s, callback(state))
	if strings.Contains(w.Body.String(), "✓") {
		t.Error("an expired sign-in was accepted")
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("token endpoint called %d times, want 0", n)
	}
}

// A pasted address that is not the redirect URI is a slip, not an attack on
// the state: refuse it without spending the pending sign-in.
func TestSignInRefusesOtherAddressAndKeepsSignIn(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	state := startSignIn(t, s).Query().Get("state")
	w := finish(s, "https://elsewhere.example.com/oauth/callback?code=code-1&state="+state)
	if strings.Contains(w.Body.String(), "✓") {
		t.Error("an address other than the redirect URI was accepted")
	}
	if n := len(fake.calls()); n != 0 {
		t.Fatalf("token endpoint called %d times, want 0", n)
	}
	if w := finish(s, callback(state)); !strings.Contains(w.Body.String(), "✓") {
		t.Errorf("the right address after a slip failed: %s", w.Body.String())
	}
}

func TestSignInReportsTeslaError(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	state := startSignIn(t, s).Query().Get("state")
	w := finish(s, testRedirect+"?error=access_denied&error_description=User+cancelled&state="+state)
	if !strings.Contains(w.Body.String(), "access_denied") {
		t.Errorf("Tesla's error is not shown: %s", w.Body.String())
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("token endpoint called %d times, want 0", n)
	}
}

func TestSignInFinishWithoutStartIsRefused(t *testing.T) {
	s, fake := newSignInServer(t, Options{})
	w := finish(s, callback("anything"))
	if strings.Contains(w.Body.String(), "✓") {
		t.Error("a finish without a start was accepted")
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("token endpoint called %d times, want 0", n)
	}
}

func TestSignInOfferedOnlyWithRedirectURI(t *testing.T) {
	s, _ := newSignInServer(t, Options{})
	w := httptest.NewRecorder()
	s.page(w, loopbackReq(http.MethodGet, "/", nil))
	if !strings.Contains(w.Body.String(), `action="/signin"`) {
		t.Error("the page does not offer Sign in with Tesla")
	}
	if !strings.Contains(w.Body.String(), testRedirect) {
		t.Error("the page does not name the redirect URI to register")
	}

	s = newSecretServer(t, "x")
	w = httptest.NewRecorder()
	s.page(w, loopbackReq(http.MethodGet, "/", nil))
	if strings.Contains(w.Body.String(), `action="/signin"`) {
		t.Error("sign-in offered without a redirect URI")
	}
	if w := postForm(s, "/signin", nil); w.Code == http.StatusSeeOther {
		t.Error("POST /signin redirected without a redirect URI")
	}
}
