// Package wss implements Tesla's legacy streaming-WebSocket protocol so TeslaMate
// (TESLA_WSS_HOST → here) gets near-instant drive-start detection, replacing the
// MyTeslaMate bridge+shim. It pushes data:update CSV frames built from live state.
//
// Protocol: on connect send control:hello; client sends data:subscribe_oauth with
// a tag (vehicle_id); we stream data:update with the 13 legacy fields in order:
//
//	time,speed,odometer,soc,elevation,est_heading,est_lat,est_lng,power,shift_state,range,est_range,heading
package wss

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
	"github.com/LasseLegarth/community-teslafleet/internal/vehicledata"
)

type Server struct {
	store    *store.Store
	cfg      *config.Config
	byKey    map[string]config.Vehicle // vehicle_id / id / vin -> vehicle
	log      *slog.Logger
	upgrader websocket.Upgrader
	interval time.Duration
}

func NewServer(st *store.Store, cfg *config.Config, log *slog.Logger) *Server {
	return &Server{
		store:    st,
		cfg:      cfg,
		byKey:    config.VehiclesByKey(cfg),
		log:      log,
		upgrader: websocket.Upgrader{CheckOrigin: sameOriginOrNone},
		interval: time.Second,
	}
}

// sameOriginOrNone is the WebSocket origin policy. TeslaMate is a server-side
// client and sends no Origin header, so an absent Origin is allowed. A present
// Origin is a browser, and it must match the Host — otherwise any page the
// operator visits could open this socket and read live vehicle position.
func sameOriginOrNone(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type inMsg struct {
	MsgType string `json:"msg_type"`
	Token   string `json:"token"`
	Value   string `json:"value"`
	Tag     string `json:"tag"`
}

// Handler upgrades to WebSocket and speaks the legacy streaming protocol.
func (s *Server) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.log.Debug("wss upgrade failed", "err", err)
			return
		}
		defer conn.Close()

		var wmu sync.Mutex
		write := func(v any) error {
			wmu.Lock()
			defer wmu.Unlock()
			return conn.WriteJSON(v)
		}

		_ = write(map[string]any{"msg_type": "control:hello", "connection_timeout": 30000})

		// One pusher goroutine per subscribed tag, each with its own stop channel.
		var pmu sync.Mutex
		pushers := map[string]chan struct{}{}
		stopTag := func(tag string) {
			if ch, ok := pushers[tag]; ok {
				close(ch)
				delete(pushers, tag)
			}
		}
		stopAll := func() {
			pmu.Lock()
			defer pmu.Unlock()
			for tag, ch := range pushers {
				close(ch)
				delete(pushers, tag)
			}
		}
		defer stopAll()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m inMsg
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			switch m.MsgType {
			case "data:subscribe_oauth":
				veh, ok := s.lookupTag(m.Tag)
				if !ok {
					_ = write(map[string]any{"msg_type": "data:error", "tag": m.Tag, "error_type": "vehicle_error", "value": "unknown vehicle"})
					continue
				}
				s.log.Info("wss subscribe", "tag", m.Tag, "vin", veh.VIN)
				pmu.Lock()
				stopTag(m.Tag) // replace any existing pusher for this tag
				stop := make(chan struct{})
				pushers[m.Tag] = stop
				pmu.Unlock()
				go s.push(veh, m.Tag, write, stop)
			case "data:unsubscribe":
				pmu.Lock()
				if m.Tag != "" {
					stopTag(m.Tag)
				} else {
					for tag := range pushers {
						stopTag(tag)
					}
				}
				pmu.Unlock()
			}
		}
	}
}

// lookupTag resolves a subscription tag to a vehicle. Configured vehicles come
// from the startup map; anything else is matched against the VINs the store has
// actually seen, the same fallback /api/1/vehicles uses. Without it a
// zero-config deployment advertises a car over HTTP and then refuses to stream
// it, so TeslaMate never gets near-real-time drive detection.
func (s *Server) lookupTag(tag string) (config.Vehicle, bool) {
	if v, ok := s.byKey[tag]; ok {
		return v, true
	}
	for _, vin := range s.store.VINs() {
		v := config.AutoVehicle(vin)
		if tag == v.VIN || tag == v.IDString() || tag == strconv.FormatInt(v.VehicleID, 10) {
			return v, true
		}
	}
	return config.Vehicle{}, false
}

func (s *Server) push(veh config.Vehicle, tag string, write func(any) error, stop chan struct{}) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			snap, _ := s.store.Snapshot(veh.VIN)
			now := s.store.Now()
			d := store.Derive(snap, s.cfg.State, now)
			csv := buildCSV(snap, d, s.cfg.Units, now)
			if err := write(map[string]any{"msg_type": "data:update", "tag": tag, "value": csv}); err != nil {
				return
			}
		}
	}
}

// buildCSV renders the 13 legacy streaming fields. Empty string = nil. power is
// always numeric so TeslaMate treats it as a "real" online (not a subsystem probe).
//
// f[0] is the OBSERVATION time, not the send time: TeslaMate stores it verbatim
// as the position's date (vehicle.ex create_position/2), and while driving it
// inserts a row for every frame carrying gear D/N/R. Stamping the send time on
// a frozen snapshot therefore wrote one position per second at the last known
// coordinates, each claiming to be a fresh fix, which is how a stalled feed
// grew a fake parked tail on the end of a drive.
//
// Frames are NOT suppressed when the observation has not advanced, tempting as
// that looks. TeslaMate's stream client arms a 30-second inactivity timer that
// it resets on any received frame and, on expiry, reports :inactive and CLOSES
// the socket (tesla_api/stream.ex handle_info(:timeout, ...)). Going quiet
// would trade duplicate rows for a permanent disconnect/reconnect cycle.
func buildCSV(snap store.Snapshot, d store.Derived, units config.Units, now time.Time) string {
	f := make([]string, 13)
	for i := range f {
		f[i] = ""
	}
	obs := now
	if !snap.LastV.IsZero() {
		obs = snap.LastV
	}
	f[0] = strconv.FormatInt(obs.UnixMilli(), 10) // time

	if d.Driving {
		if v, ok := snap.Num(store.FieldVehicleSpeed); ok {
			f[1] = strconv.Itoa(int(vehicledata.SpeedToMph(v, units.SpeedInput) + 0.5))
		}
	}
	if v, ok := snap.Num(store.FieldOdometer); ok {
		f[2] = strconv.FormatFloat(vehicledata.RangeToMiles(v, units.OdometerInput), 'f', 2, 64)
	}
	if v, ok := vehicledata.DisplayedSOC(snap); ok {
		f[3] = strconv.Itoa(v)
	}
	// f[4] elevation: not enrolled → empty
	if v, ok := snap.Num(store.FieldGpsHeading); ok {
		f[5] = strconv.Itoa(int(v))
		f[12] = f[5] // heading
	}
	if lat, lng, ok := snap.Location(); ok {
		f[6] = strconv.FormatFloat(lat, 'f', 6, 64)
		f[7] = strconv.FormatFloat(lng, 'f', 6, 64)
	}
	f[8] = strconv.Itoa(drivePower(snap, d)) // power (numeric → real online)
	if g, ok := snap.Field(store.FieldGear); ok {
		f[9] = store.GearString(g.Value)
	}
	if v, ok := snap.Num(store.FieldRatedRange); ok {
		f[10] = strconv.Itoa(int(vehicledata.RangeToMiles(v, units.RangeInput) + 0.5))
	}
	if v, ok := snap.Num(store.FieldEstBatteryRange); ok {
		f[11] = strconv.Itoa(int(vehicledata.RangeToMiles(v, units.RangeInput) + 0.5))
	}
	return strings.Join(f, ",")
}

// drivePower mirrors the HTTP vehicle_data mapping: negative while charging,
// pack power while driving. The two paths must agree — TeslaMate reads both for
// the same instant and a disagreement shows up as a sawtooth in the history.
func drivePower(snap store.Snapshot, d store.Derived) int {
	if p, ok := snap.ChargerPower(); ok && p > 0 {
		return -int(p) // charging draws negative drive power
	}
	if d.Driving {
		if kw, ok := vehicledata.PackPowerKW(snap); ok {
			return int(math.Round(kw))
		}
	}
	return 0
}
