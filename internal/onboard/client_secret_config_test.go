package onboard

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// The client secret can come from config (TGW_TESLA_CLIENT_SECRET or its
// _FILE form), so it never has to be typed into the wizard. A secret saved in
// the wizard still wins, as the wizard's Fleet API URL does over config.
func newSecretServer(t *testing.T, cfgSecret string) *Server {
	t.Helper()
	s, err := NewServer(Options{DataDir: t.TempDir(), ClientSecret: cfgSecret},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

func TestClientSecretFromConfig(t *testing.T) {
	s := newSecretServer(t, "from-config")
	if got := s.clientSecret(); got != "from-config" {
		t.Errorf("clientSecret() = %q, want the configured secret", got)
	}
	if got := s.client().clientSecret; got != "from-config" {
		t.Errorf("Tesla client secret = %q, want the configured secret", got)
	}
}

func TestClientSecretSavedInWizardWins(t *testing.T) {
	s := newSecretServer(t, "from-config")
	if err := s.store.saveSecret("from-wizard"); err != nil {
		t.Fatal(err)
	}
	if got := s.clientSecret(); got != "from-wizard" {
		t.Errorf("clientSecret() = %q, want the wizard's secret", got)
	}
}

func TestClientSecretFromConfigShowsAsSet(t *testing.T) {
	s := newSecretServer(t, "from-config")
	w := httptest.NewRecorder()
	s.page(w, loopbackReq("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `<span class="badge ok">set</span>`) {
		t.Error("step 1 does not show the configured client secret as set")
	}
	if strings.Contains(w.Body.String(), "from-config") {
		t.Error("the page shows the secret itself")
	}
}
