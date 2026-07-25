// Package fleetapi emulates the subset of Tesla's Fleet API that TeslaMate polls,
// served from live telemetry state. TeslaMate points TESLA_API_HOST here, so all
// polling is local and free and never wakes the car.
package fleetapi

import (
	"encoding/json"
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
	log        *slog.Logger
}

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
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("ok")); err != nil {
			s.log.Debug("healthz write failed", "err", err)
		}
	})
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
		out[v.VIN] = map[string]any{
			"seen":         seen,
			"state":        d.State,
			"driving":      d.Driving,
			"charging":     d.Charging,
			"connectivity": snap.Connectivity,
			"fields":       fields,
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
