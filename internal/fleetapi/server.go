// Package fleetapi emulates the subset of Tesla's Fleet API that TeslaMate polls,
// served from live telemetry state. TeslaMate points TESLA_API_HOST here, so all
// polling is local and free and never wakes the car.
package fleetapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
	"github.com/LasseLegarth/community-teslafleet/internal/vehicledata"
)

type Server struct {
	store      *store.Store
	cfg        *config.Config
	tmpls      map[string]*vehicledata.Template // by VIN
	byKey      map[string]config.Vehicle        // id-string and VIN -> vehicle
	relay      *commands.Relay                  // optional: for self-enroll (may be nil)
	enrollFile string                           // path to fleet_telemetry_config JSON
	ingest     IngestHealth                     // optional: telemetry link state (may be nil)
	log        *slog.Logger
}

// IngestHealth is the slice of the telemetry consumer that readiness needs.
// An interface rather than a concrete type so fleetapi does not import ingest
// (and so tests can fake a dead link).
type IngestHealth interface {
	Connected() bool
}

// SetIngestHealth attaches the telemetry consumer so /healthz can report the
// ingest link. Optional: with no source attached, /healthz still reports store
// freshness but cannot distinguish "link down" from "fleet asleep", so it does
// not claim to.
func (s *Server) SetIngestHealth(h IngestHealth) { s.ingest = h }

func NewServer(st *store.Store, cfg *config.Config, tmpls map[string]*vehicledata.Template, relay *commands.Relay, enrollFile string, log *slog.Logger) *Server {
	return &Server{
		store:      st,
		cfg:        cfg,
		tmpls:      tmpls,
		byKey:      config.VehiclesByKey(cfg),
		relay:      relay,
		enrollFile: enrollFile,
		log:        log,
	}
}

func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(logRequests(s.log))
	r.Get("/api/1/products", s.handleProducts)
	r.Get("/api/1/vehicles", s.handleProducts)
	r.Get("/api/1/vehicles/{id}", s.handleVehicle)
	r.Get("/api/1/vehicles/{id}/vehicle_data", s.handleVehicleData)
	r.Post("/api/1/vehicles/{id}/wake_up", s.handleWake)
	r.Post("/admin/enroll", s.handleEnroll)
	r.Get("/debug/state", s.handleDebug)
	r.Get("/healthz", s.handleHealthz)
	return r
}

func (s *Server) handleProducts(w http.ResponseWriter, _ *http.Request) {
	vehicles := s.effectiveVehicles()
	out := make([]map[string]any, 0, len(vehicles))
	for _, v := range vehicles {
		out = append(out, s.summary(v))
	}
	s.writeResponse(w, out)
}

// effectiveVehicles returns the configured vehicles plus an auto-built Vehicle for
// every VIN seen on the stream that is not in config, so a zero-config gateway also
// serves TeslaMate the cars it has discovered.
func (s *Server) effectiveVehicles() []config.Vehicle {
	out := make([]config.Vehicle, 0, len(s.cfg.Vehicles))
	inCfg := make(map[string]bool, len(s.cfg.Vehicles))
	for _, v := range s.cfg.Vehicles {
		out = append(out, v)
		inCfg[v.VIN] = true
	}
	for _, vin := range s.store.VINs() {
		if !inCfg[vin] {
			out = append(out, config.AutoVehicle(vin))
		}
	}
	return out
}

func (s *Server) handleVehicle(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookup(r)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	s.writeResponse(w, s.summary(v))
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookup(r)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	sum := s.summary(v)
	sum["state"] = "online" // wake_up always reports the car coming online
	s.writeResponse(w, sum)
}

func (s *Server) handleVehicleData(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookup(r)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	snap, _ := s.store.Snapshot(v.VIN)
	now := s.store.Now()
	d := store.Derive(snap, s.cfg.State, now)
	tmpl := s.tmpls[v.VIN]
	resp := vehicledata.Build(snap, d, v, tmpl, s.cfg.Units, now)
	s.writeResponse(w, resp)
}

// handleEnroll reads the configured fleet_telemetry_config JSON file and pushes it
// to the vehicle-command proxy via the command relay's own OAuth token.
func (s *Server) handleEnroll(w http.ResponseWriter, _ *http.Request) {
	if s.relay == nil {
		s.log.Warn("enroll requested but command relay is disabled")
		s.writeError(w, http.StatusServiceUnavailable, "command_relay_disabled")
		return
	}
	payload, err := os.ReadFile(s.enrollFile)
	if err != nil {
		s.log.Error("enroll read file failed", "path", s.enrollFile, "err", err)
		s.writeError(w, http.StatusInternalServerError, "enroll_file_unreadable")
		return
	}
	if err := s.relay.Enroll(payload); err != nil {
		s.log.Error("enroll failed", "path", s.enrollFile, "err", err)
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.log.Info("enroll succeeded", "path", s.enrollFile)
	s.writeResponse(w, map[string]any{"enrolled": true})
}

// Health is the readiness verdict served by /healthz.
type Health struct {
	Status string `json:"status"` // ok | degraded
	// IngestConnected is nil when no ingest source is attached: unknown is a
	// distinct answer from "down" and must not be reported as either.
	IngestConnected *bool `json:"ingest_connected"`
	// LastIngestUnix is 0 and LastIngestAgeS is -1 when nothing has ever been
	// ingested, so a caller can tell "never" from "just now".
	LastIngestUnix int64  `json:"last_ingest_unix"`
	LastIngestAgeS int64  `json:"last_ingest_age_s"`
	VehiclesOnline int    `json:"vehicles_online"`
	VehiclesKnown  int    `json:"vehicles_known"`
	StaleAfterS    int    `json:"stale_after_s"`
	Reason         string `json:"reason,omitempty"`
}

// health computes readiness.
//
// The rule is: the ingest link must be up, AND if any vehicle is online it must
// have streamed something within stale_after_seconds. The second half has to be
// conditional, because a sleeping fleet streams nothing for hours and that is
// correct behaviour, not a fault — an unconditional freshness check would fail
// every night, and a check that ignores freshness misses the failure that
// actually happens (a faulted socket while the publisher keeps republishing
// stale values).
//
// A fleet with nothing online is therefore READY: there is no evidence of a
// problem, and the alternative -- staying unready until the first message --
// would leave the gateway permanently unready whenever it restarts while every
// car is asleep.
func (s *Server) health() Health {
	now := s.store.Now()
	h := Health{
		Status:         "ok",
		LastIngestAgeS: -1,
		StaleAfterS:    s.cfg.State.StaleAfterSeconds,
	}
	if s.ingest != nil {
		connected := s.ingest.Connected()
		h.IngestConnected = &connected
	}
	if last := s.store.LastIngest(); !last.IsZero() {
		h.LastIngestUnix = last.Unix()
		h.LastIngestAgeS = int64(now.Sub(last).Seconds())
	}
	for _, v := range s.effectiveVehicles() {
		h.VehiclesKnown++
		if snap, _ := s.store.Snapshot(v.VIN); snap.Connectivity == "online" {
			h.VehiclesOnline++
		}
	}

	stale := int64(s.cfg.State.StaleAfterSeconds)
	switch {
	case h.IngestConnected != nil && !*h.IngestConnected:
		h.Status = "degraded"
		h.Reason = "telemetry ingest link is not connected"
	case h.VehiclesOnline == 0:
		// Nothing is expected to be streaming.
	case stale <= 0:
		// Freshness threshold disabled by config; the link check is all we have.
	case h.LastIngestAgeS < 0:
		h.Status = "degraded"
		h.Reason = fmt.Sprintf("%d vehicle(s) online but no telemetry has ever been received", h.VehiclesOnline)
	case h.LastIngestAgeS > stale:
		h.Status = "degraded"
		h.Reason = fmt.Sprintf("%d vehicle(s) online but no telemetry for %ds (stale after %ds)",
			h.VehiclesOnline, h.LastIngestAgeS, stale)
	}
	return h
}

// handleHealthz serves real readiness: 200 when ingest is demonstrably working
// (or legitimately quiet), 503 with a reason when it is not. It used to return a
// hard-coded "ok", which stayed green through a wedged ingest socket and so was
// worse than no probe at all -- anything built on it, including a container
// healthcheck, was guaranteed to miss the one failure that happens.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	h := s.health()
	code := http.StatusOK
	if h.Status != "ok" {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(h); err != nil {
		s.log.Warn("healthz encode failed", "err", err)
	}
}

func (s *Server) handleDebug(w http.ResponseWriter, _ *http.Request) {
	now := s.store.Now()
	out := map[string]any{}
	for _, v := range s.effectiveVehicles() {
		snap, seen := s.store.Snapshot(v.VIN)
		d := store.Derive(snap, s.cfg.State, now)
		fields := map[string]any{}
		for name, fv := range snap.Fields {
			fields[name] = map[string]any{"value": fv.Value, "age_s": int(now.Sub(fv.UpdatedAt).Seconds()), "count": fv.Count}
		}
		// Explicit last-ingest timestamp per vehicle. Without it, the only way to
		// tell whether this vehicle's state is still moving is to diff successive
		// /debug/state responses -- and that does not work, because every field
		// carries an age_s that ticks on its own, so a naive whole-object
		// comparison always reports a change even for a car that has been silent
		// for hours.
		var lastUnix, lastAge int64 = 0, -1
		if !snap.LastV.IsZero() {
			lastUnix = snap.LastV.Unix()
			lastAge = int64(now.Sub(snap.LastV).Seconds())
		}
		out[v.VIN] = map[string]any{
			"seen":              seen,
			"state":             d.State,
			"driving":           d.Driving,
			"charging":          d.Charging,
			"connectivity":      snap.Connectivity,
			"last_ingest_unix":  lastUnix,
			"last_ingest_age_s": lastAge,
			"field_count":       len(snap.Fields),
			"fields":            fields,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.log.Warn("debug state encode failed", "err", err)
	}
}

func (s *Server) summary(v config.Vehicle) map[string]any {
	snap, _ := s.store.Snapshot(v.VIN)
	d := store.Derive(snap, s.cfg.State, s.store.Now())
	name := v.DisplayName
	if name == "" || name == v.VIN {
		name = "Tesla" // auto-discovered: TeslaMate shows a placeholder until renamed
	}
	return map[string]any{
		"id":               v.ID,
		"id_s":             v.IDString(),
		"user_id":          v.ID,
		"vehicle_id":       v.VehicleID,
		"vin":              v.VIN,
		"display_name":     name,
		"state":            d.State,
		"in_service":       false,
		"calendar_enabled": true,
		"api_version":      96,
		"option_codes":     "",
		"access_type":      "OWNER",
	}
}

func (s *Server) lookup(r *http.Request) (config.Vehicle, bool) {
	id := chi.URLParam(r, "id")
	if v, ok := s.byKey[id]; ok { // configured vehicle (VIN, id, or vehicle_id)
		return v, ok
	}
	// Fall back to auto-discovered vehicles keyed by VIN or derived id-string.
	for _, v := range s.effectiveVehicles() {
		if id == v.VIN || id == v.IDString() || id == strconv.FormatInt(v.VehicleID, 10) {
			return v, true
		}
	}
	return config.Vehicle{}, false
}

// writeResponse and writeError are methods rather than free functions purely so
// they can log. An encode failure cannot be reported to the client -- the status
// line is already on the wire -- so the only options are to log it or to lose
// it, and losing it means a client that received a truncated body while the
// gateway reported nothing at all. Two causes are real here: a client that
// disconnected mid-response (routine, and why this is Debug for the happy-path
// case) and a value in the store that will not marshal (a bug, and one that only
// shows up as TeslaMate quietly recording nothing).
func (s *Server) writeResponse(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"response": payload}); err != nil {
		s.log.Warn("fleetapi response encode failed", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]any{"response": nil, "error": msg, "error_description": ""}); err != nil {
		s.log.Warn("fleetapi error encode failed", "err", err, "for_error", msg)
	}
}

func logRequests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			log.Debug("fleetapi", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "dur_ms", time.Since(start).Milliseconds())
		})
	}
}
