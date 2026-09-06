package onboard

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/LasseLegarth/community-teslafleet/internal/enroll"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Options configures the wizard server. The Tesla* fields come from the gateway config;
// the wizard fills in domain/client creds/token as the user progresses.
type Options struct {
	DataDir    string
	Password   string // optional basic-auth (standalone); HA ingress is pre-authenticated
	AuthHost   string
	AuthPath   string
	FleetAPI   string // regional Fleet API base (required for partner/vehicle/enroll calls)
	ClientID   string // optional default from config (wizard can override)
	ProxyURL   string // vehicle-command proxy (for enroll)
	EnrollFile string // ftc.json path to POST on enroll
	TokenCache string // where to persist the obtained refresh token (relay reads this)
}

// State is the persisted onboarding progress (no secrets in here; the private key and
// client secret live in separate 0600 files).
type State struct {
	Domain      string         `json:"domain"`
	ClientID    string         `json:"client_id"`
	HasKeys     bool           `json:"has_keys"`
	SecretSet   bool           `json:"secret_set"`
	PartnerDone bool           `json:"partner_done"`
	TokenSet    bool           `json:"token_set"`
	Vehicles    []teslaVehicle `json:"vehicles,omitempty"`
	LastProbe   *ProbeResult   `json:"last_probe,omitempty"`
	Profile     string         `json:"profile"`
	Port        int            `json:"port"`
	Notice      string         `json:"-"` // transient one-shot message
}

type Store struct {
	dir string
	mu  sync.Mutex
	st  State
}

func newStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if b, err := os.ReadFile(s.statePath()); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	return s, nil
}

func (s *Store) statePath() string   { return filepath.Join(s.dir, "state.json") }
func (s *Store) privatePath() string { return filepath.Join(s.dir, "private-key.pem") }
func (s *Store) publicPath() string  { return filepath.Join(s.dir, "public-key.pem") }
func (s *Store) secretPath() string  { return filepath.Join(s.dir, "client-secret") }

func (s *Store) saveState() error {
	b, _ := json.MarshalIndent(s.st, "", "  ")
	return atomicWrite(s.statePath(), b, 0o600)
}

func (s *Store) generateKeys() error {
	kp, err := GenerateKey()
	if err != nil {
		return err
	}
	if err := atomicWrite(s.privatePath(), kp.PrivatePEM, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(s.publicPath(), kp.PublicPEM, 0o644); err != nil {
		return err
	}
	s.st.HasKeys = true
	return s.saveState()
}

func (s *Store) publicPEM() ([]byte, error)   { return os.ReadFile(s.publicPath()) }
func (s *Store) clientSecret() string         { b, _ := os.ReadFile(s.secretPath()); return string(b) }
func (s *Store) saveSecret(v string) error    { return atomicWrite(s.secretPath(), []byte(v), 0o600) }

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Server is the onboarding wizard.
type Server struct {
	store   *Store
	opts    Options
	log     *slog.Logger
	tmpl    *template.Template
	tokenMu sync.Mutex // serializes refresh-and-persist against itself
}

func NewServer(opts Options, log *slog.Logger) (*Server, error) {
	store, err := newStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if store.st.ClientID == "" {
		store.st.ClientID = opts.ClientID
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{"hasPrefix": strings.HasPrefix}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: store, opts: opts, log: log, tmpl: tmpl}, nil
}

// client builds a Tesla API client from config + the wizard's stored creds.
func (s *Server) client() *teslaClient {
	return newTeslaClient(s.opts.AuthHost, s.opts.AuthPath, s.opts.FleetAPI,
		s.store.st.ClientID, s.store.clientSecret())
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("POST /domain", s.saveDomain)
	mux.HandleFunc("POST /generate", s.generate)
	mux.HandleFunc("GET /public-key.pem", s.downloadPublic)
	mux.HandleFunc("POST /probe", s.probe)
	mux.HandleFunc("POST /register-partner", s.registerPartner)
	mux.HandleFunc("POST /token", s.pasteToken)
	mux.HandleFunc("POST /vehicles", s.listVehicles)
	mux.HandleFunc("POST /enrollment", s.saveEnrollment)
	mux.HandleFunc("POST /enroll", s.enroll)
	return s.auth(mux)
}

// WellKnownHandler serves the partner public key, UNAUTHENTICATED, so the user can
// point their domain's /.well-known here instead of hosting the file by hand (Tesla
// fetches it over HTTPS with no credentials). 404 until the key is generated.
func (s *Server) WellKnownHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+WellKnownPath, func(w http.ResponseWriter, _ *http.Request) {
		pub, err := s.store.publicPEM()
		if err != nil {
			http.Error(w, "public key not generated yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		if _, err := w.Write(pub); err != nil {
			// Tesla fetches this to verify domain ownership; a failed write means
			// enrollment will fail for a reason visible nowhere else.
			s.log.Warn("well-known public key write failed", "err", err)
		}
	})
	return mux
}

// auth guards the wizard. Every handler behind it can replace the signing
// keypair, the partner domain, or the stored refresh token, so "no password
// configured" must not mean "open to the network".
//
// With a password: Basic Auth. Without one: loopback only. The HA add-on runs
// behind the Supervisor's ingress proxy, which reaches us over loopback, so
// that path keeps working; a standalone listener on :8099 no longer serves the
// LAN unless a password is set.
func (s *Server) auth(next http.Handler) http.Handler {
	if s.opts.Password == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
				s.log.Warn("rejected non-loopback onboarding request (no password configured)",
					"remote_addr", r.RemoteAddr)
				http.Error(w, "forbidden: set a password to use the wizard over the network",
					http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(s.opts.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="community-teslafleet"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type view struct {
	Base       string
	State      State
	PairingURL string
	Profiles   []string
	Estimate   enroll.Estimate
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, notice string) {
	s.store.mu.Lock()
	st := s.store.st
	st.Notice = notice
	s.store.mu.Unlock()
	v := view{Base: r.Header.Get("X-Ingress-Path"), State: st, Profiles: enroll.Profiles}
	if st.Domain != "" {
		v.PairingURL = pairingURL(st.Domain)
	}
	prof := st.Profile
	if prof == "" {
		prof = "balanced"
	}
	v.Estimate = enroll.EstimateCost(enroll.Generate(prof, st.Domain, st.Port))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", v); err != nil {
		s.log.Error("render", "err", err)
	}
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) { s.render(w, r, "") }

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, r.Header.Get("X-Ingress-Path")+"/", http.StatusSeeOther)
}

func (s *Server) saveDomain(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	s.store.st.Domain = strings.TrimSpace(r.FormValue("domain"))
	s.store.st.ClientID = strings.TrimSpace(r.FormValue("client_id"))
	if sec := strings.TrimSpace(r.FormValue("client_secret")); sec != "" {
		if err := s.store.saveSecret(sec); err == nil {
			s.store.st.SecretSet = true
		}
	}
	_ = s.store.saveState()
	s.store.mu.Unlock()
	s.home(w, r)
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	err := s.store.generateKeys()
	s.store.mu.Unlock()
	if err != nil {
		http.Error(w, "key generation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.home(w, r)
}

func (s *Server) downloadPublic(w http.ResponseWriter, r *http.Request) {
	pub, err := s.store.publicPEM()
	if err != nil {
		http.Error(w, "no key yet — generate first", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="com.tesla.3p.public-key.pem"`)
	if _, err := w.Write(pub); err != nil {
		s.log.Warn("public key download write failed", "err", err)
	}
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	domain := s.store.st.Domain
	pub, _ := s.store.publicPEM()
	s.store.mu.Unlock()
	if domain == "" || pub == nil {
		http.Error(w, "set a domain and generate keys first", http.StatusBadRequest)
		return
	}
	res := ProbeWellKnown(domain, pub)
	s.store.mu.Lock()
	s.store.st.LastProbe = &res
	_ = s.store.saveState()
	s.store.mu.Unlock()
	s.home(w, r)
}

func (s *Server) registerPartner(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	domain, clientID := s.store.st.Domain, s.store.st.ClientID
	s.store.mu.Unlock()
	if domain == "" || clientID == "" || s.store.clientSecret() == "" || s.opts.FleetAPI == "" {
		s.render(w, r, "✗ Need domain, client_id, client_secret and a Fleet API URL first.")
		return
	}
	if err := s.client().registerPartner(domain); err != nil {
		s.render(w, r, "✗ Partner registration failed: "+err.Error())
		return
	}
	s.store.mu.Lock()
	s.store.st.PartnerDone = true
	_ = s.store.saveState()
	s.store.mu.Unlock()
	s.render(w, r, "✓ Partner account registered for "+domain)
}

func (s *Server) pasteToken(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("refresh_token"))
	if tok == "" {
		s.render(w, r, "✗ Paste a refresh token.")
		return
	}
	if s.opts.TokenCache != "" {
		if err := atomicWrite(s.opts.TokenCache, []byte(tok), 0o600); err != nil {
			s.render(w, r, "✗ Could not write token cache: "+err.Error())
			return
		}
	}
	s.store.mu.Lock()
	s.store.st.TokenSet = true
	_ = s.store.saveState()
	s.store.mu.Unlock()
	s.render(w, r, "✓ Refresh token saved. Restart the gateway to use it for commands.")
}

func (s *Server) listVehicles(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("access_token"))
	if tok == "" {
		// Try to mint an access token from the saved refresh token.
		if at, err := s.accessToken(); err == nil {
			tok = at
		}
	}
	if tok == "" {
		s.render(w, r, "✗ Provide an access token, or save a refresh token first.")
		return
	}
	vs, err := s.client().vehicles(tok)
	if err != nil {
		s.render(w, r, "✗ List vehicles failed: "+err.Error())
		return
	}
	s.store.mu.Lock()
	s.store.st.Vehicles = vs
	_ = s.store.saveState()
	s.store.mu.Unlock()
	s.render(w, r, "✓ Found "+itoa(len(vs))+" vehicle(s). Pair each via the link below, then enroll.")
}

// saveEnrollment generates an ftc.json from the chosen profile + endpoint and writes it
// to the gateway's enroll file (used by step 7 / the relay).
func (s *Server) saveEnrollment(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(r.FormValue("profile"))
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	s.store.mu.Lock()
	domain := s.store.st.Domain
	s.store.st.Profile = profile
	s.store.st.Port = port
	_ = s.store.saveState()
	s.store.mu.Unlock()

	ftc := enroll.Generate(profile, domain, port)
	b, err := json.MarshalIndent(ftc, "", "  ")
	if err != nil {
		s.render(w, r, "✗ Could not build config: "+err.Error())
		return
	}
	if err := atomicWrite(s.opts.EnrollFile, b, 0o600); err != nil {
		s.render(w, r, "✗ Could not write "+s.opts.EnrollFile+": "+err.Error())
		return
	}
	est := enroll.EstimateCost(ftc)
	s.render(w, r, "✓ Saved enrollment ("+itoa(est.EnrolledFields)+" signals, profile "+profile+"). Now enroll below.")
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	payload, err := os.ReadFile(s.opts.EnrollFile)
	if err != nil {
		s.render(w, r, "✗ No enrollment file at "+s.opts.EnrollFile+" — set intervals on the Enrollment page first.")
		return
	}
	at, err := s.accessToken()
	if err != nil {
		s.render(w, r, "✗ Token refresh failed: "+err.Error())
		return
	}
	if err := postEnroll(s.opts.ProxyURL, at, payload); err != nil {
		s.render(w, r, "✗ Enroll failed: "+err.Error())
		return
	}
	s.render(w, r, "✓ Telemetry config sent. The car adopts it within a minute (poll synced).")
}

// accessToken mints an access token from the cached refresh token, persisting
// the replacement Tesla hands back.
//
// Tesla ROTATES the refresh token on every use: the old one is dead the moment
// this call succeeds. Discarding the replacement left the cache holding a spent
// credential, so the next enroll — or the next restart — failed to authenticate
// for no visible reason.
func (s *Server) accessToken() (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	rt := s.readTokenCache()
	if rt == "" {
		return "", fmt.Errorf("no refresh token saved")
	}
	t, err := s.client().refresh(rt)
	if err != nil {
		return "", err
	}
	if t.Refresh != "" && t.Refresh != rt && s.opts.TokenCache != "" {
		if err := atomicWrite(s.opts.TokenCache, []byte(t.Refresh), 0o600); err != nil {
			// The old token is already spent, so failing to record the new one
			// loses the credential entirely. Say so loudly.
			s.log.Error("could not persist rotated refresh token — re-paste one in the wizard",
				"path", s.opts.TokenCache, "err", err)
			return "", fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	return t.Access, nil
}

func (s *Server) readTokenCache() string {
	if s.opts.TokenCache == "" {
		return ""
	}
	b, _ := os.ReadFile(s.opts.TokenCache)
	return strings.TrimSpace(string(b))
}
