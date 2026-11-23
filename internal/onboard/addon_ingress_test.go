package onboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// In the Home Assistant add-on the wizard is reached through the Supervisor's
// ingress proxy, which connects from 172.30.32.2, not from loopback, and the
// add-on has no password option. Issue #13: every sidebar request got 403.

func ingressServer(t *testing.T, ingress bool) *Server {
	t.Helper()
	s, err := NewServer(Options{DataDir: t.TempDir(), Ingress: ingress},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

func getFrom(s *Server, addr string) int {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = addr
	r.Header.Set("X-Ingress-Path", "/api/hassio_ingress/abc")
	s.Handler().ServeHTTP(rr, r)
	return rr.Code
}

func TestAddon_IngressProxyIsServed(t *testing.T) {
	if code := getFrom(ingressServer(t, true), "172.30.32.2:40000"); code != http.StatusOK {
		t.Fatalf("GET / from the ingress proxy = %d, want 200", code)
	}
}

func TestAddon_OtherHassioPeersAreRefused(t *testing.T) {
	// Other add-ons and HA Core share the hassio network; only the Supervisor
	// proxies ingress.
	for _, addr := range []string{"172.30.32.1:40000", "172.30.33.5:40000", "192.168.1.50:40000"} {
		if code := getFrom(ingressServer(t, true), addr); code != http.StatusForbidden {
			t.Errorf("GET / from %s = %d, want 403", addr, code)
		}
	}
}

func TestAddon_IngressAddressNotTrustedOutsideTheAddon(t *testing.T) {
	if code := getFrom(ingressServer(t, false), "172.30.32.2:40000"); code != http.StatusForbidden {
		t.Fatalf("GET / from 172.30.32.2 without ingress = %d, want 403", code)
	}
}

func TestAddon_ForwardedHeadersDoNotGrantAccess(t *testing.T) {
	s := ingressServer(t, true)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.50:40000"
	r.Header.Set("X-Forwarded-For", "172.30.32.2")
	r.Header.Set("X-Real-IP", "172.30.32.2")
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("LAN request claiming the proxy address = %d, want 403", rr.Code)
	}
}

func TestAddon_IngressPostStillChecksTheSite(t *testing.T) {
	s := ingressServer(t, true)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/generate", nil)
	r.RemoteAddr = "172.30.32.2:40000"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST through ingress = %d, want 403", rr.Code)
	}
}

func TestAddon_IPv4MappedProxyAddress(t *testing.T) {
	if code := getFrom(ingressServer(t, true), "[::ffff:172.30.32.2]:40000"); code != http.StatusOK {
		t.Fatalf("GET / from the IPv4-mapped proxy address = %d, want 200", code)
	}
}
