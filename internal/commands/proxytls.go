package commands

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// ProxyTLSConfig builds the TLS settings for the hop to the vehicle-command
// proxy. Every command carries the OAuth token over this hop, so the proxy's
// certificate is verified, host name included, against ProxyCAFile (or the
// system roots when that is empty). It used to be InsecureSkipVerify for
// every deployment.
//
// ProxyInsecureSkipVerify turns that off on purpose. A CA file that cannot be
// read or holds no certificate is an error, never a quiet fallback to
// insecure.
func ProxyTLSConfig(cfg config.Commands) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ProxyServerName}
	if cfg.ProxyInsecureSkipVerify {
		tc.InsecureSkipVerify = true
		return tc, nil
	}
	if cfg.ProxyCAFile == "" {
		return tc, nil
	}
	pem, err := os.ReadFile(cfg.ProxyCAFile)
	if err != nil {
		return nil, fmt.Errorf("read proxy CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("proxy CA file %s holds no PEM certificate", cfg.ProxyCAFile)
	}
	tc.RootCAs = pool
	return tc, nil
}

// FailClosedTLS is used when ProxyTLSConfig fails: an empty root pool, so
// every handshake fails and the error says why, instead of the relay quietly
// trusting any certificate.
func FailClosedTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}
}

// NewProxyClient is the HTTP client for the vehicle-command proxy. It only
// speaks HTTPS, since TLS settings protect nothing on an http:// URL, and it
// follows no redirect, which could carry the token to another endpoint.
func NewProxyClient(tc *tls.Config) *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: httpsOnly{&http.Transport{TLSClientConfig: tc}},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("the command proxy answered with a redirect; not following it")
		},
	}
}

// httpsOnly refuses any request that is not https.
type httpsOnly struct{ rt http.RoundTripper }

func (h httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("refusing %s:// to the command proxy; it must be https", req.URL.Scheme)
	}
	return h.rt.RoundTrip(req)
}

// proxyHealth remembers whether the last exchange with the proxy got an
// HTTP answer, so /healthz can show a TLS or connection failure that would
// otherwise only appear when someone presses a button.
type proxyHealth struct {
	mu       sync.Mutex
	status   string
	insecure bool
}

func (h *proxyHealth) note(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.status = "ok"
		return
	}
	h.status = "unreachable: " + err.Error()
}

func (h *proxyHealth) get() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.status
	if s == "" {
		s = "not checked yet"
	}
	if h.insecure {
		s += " (TLS verification disabled)"
	}
	return s
}

// ProxyStatus reports "ok" once the proxy has answered over TLS, or what went
// wrong on the last attempt. /healthz shows it.
func (r *Relay) ProxyStatus() string { return r.proxyHealth.get() }

// ProbeProxy makes one request to the proxy that does nothing at Tesla: a GET
// on the root path, without a token. Any HTTP answer proves the TLS hop works.
func (r *Relay) ProbeProxy() error {
	resp, err := r.client.Get(r.proxy + "/")
	if err == nil {
		_ = resp.Body.Close()
	}
	r.proxyHealth.note(err)
	return err
}
