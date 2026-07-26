// Package commands relays Home Assistant button/switch presses to signed Tesla
// commands via the vehicle-command proxy. It holds a cmd-scoped OAuth token and
// refreshes it itself (refresh_token grant against auth.tesla.com).
package commands

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Wake-then-command timing. Variables (not consts) so tests can shrink them.
// Defaults follow Tesla's guidance: a woken car takes 10-60s to connect, so
// poll every few seconds until online with a ~40s ceiling, then let it settle.
var (
	wakePollInterval = 3 * time.Second
	wakeTimeout      = 40 * time.Second
	wakeReadyBuffer  = 2 * time.Second
)

// Entity is a command entity exposed in Home Assistant.
type Entity struct {
	Key       string // object_id + command-topic key
	Component string // button | switch | lock | number | select | cover | climate
	Name      string
	// number
	Min, Max, Step float64
	Unit           string
	// select
	Options []string
	// cover
	DeviceClass string // door | window (cover)
	// StateKey is the value_json key reflecting current state (switch/lock/number/
	// cover). Empty → optimistic/write-only entity (HA assumes the commanded state).
	StateKey string
}

// Entities is the catalog of command entities published to HA and handled here.
// Excluded by design: PIN/password-gated (valet, speed-limit, pin-to-drive),
// destructive (erase_user_data), and model-specific commands that 404 on a Model 3
// (bioweapon, tonneau, sunroof, seat coolers).
func Entities(cfg config.Commands) []Entity {
	es := []Entity{
		// Buttons (momentary).
		{Key: "flash_lights", Component: "button", Name: "Flash lights"},
		{Key: "honk", Component: "button", Name: "Honk"},
		{Key: "wake", Component: "button", Name: "Wake up"},
		{Key: "charge_max_range", Component: "button", Name: "Charge to max range"},
		{Key: "charge_standard", Component: "button", Name: "Charge to standard"},
		{Key: "remote_start", Component: "button", Name: "Remote start"},
		{Key: "homelink", Component: "button", Name: "Trigger HomeLink"},
		{Key: "media_toggle", Component: "button", Name: "Media play/pause"},
		{Key: "media_next", Component: "button", Name: "Media next"},
		{Key: "media_prev", Component: "button", Name: "Media previous"},
		{Key: "media_vol_up", Component: "button", Name: "Volume up"},
		{Key: "media_vol_down", Component: "button", Name: "Volume down"},
		// Switches.
		{Key: "charging", Component: "switch", Name: "Charging", StateKey: "charging"},
		{Key: "sentry", Component: "switch", Name: "Sentry mode", StateKey: "sentry"},
		{Key: "steering_wheel_heater", Component: "switch", Name: "Steering wheel heater"},
		{Key: "cabin_overheat", Component: "switch", Name: "Cabin overheat protection"},
		{Key: "preconditioning_max", Component: "switch", Name: "Preconditioning (max)"},
		{Key: "guest_mode", Component: "switch", Name: "Guest mode"},
		// Lock.
		{Key: "lock", Component: "lock", Name: "Lock", StateKey: "locked"},
		// Climate (proper thermostat entity: on/off + target temp + current temp).
		{Key: "climate", Component: "climate", Name: "Climate"},
		// Covers (frunk/trunk are optimistic — Tesla streams no open-state for them;
		// charge port + windows have real state).
		{Key: "charge_port", Component: "cover", Name: "Charge port", DeviceClass: "door", StateKey: "charge_port"},
		{Key: "windows", Component: "cover", Name: "Windows", DeviceClass: "window", StateKey: "windows"},
		{Key: "frunk", Component: "cover", Name: "Frunk", DeviceClass: "door"},
		{Key: "trunk", Component: "cover", Name: "Trunk", DeviceClass: "door"},
		// Numbers.
		{Key: "charge_limit", Component: "number", Name: "Charge limit", Min: 50, Max: 100, Step: 1, Unit: "%", StateKey: "charge_limit"},
		{Key: "charging_amps", Component: "number", Name: "Charging amps", Min: 0, Max: 32, Step: 1, Unit: "A"},
		// Selects (write-only/optimistic).
		{Key: "climate_keeper", Component: "select", Name: "Climate keeper", Options: []string{"off", "keep", "dog", "camp"}},
		{Key: "seat_heater_front_left", Component: "select", Name: "Seat heater front left", Options: seatLevels},
		{Key: "seat_heater_front_right", Component: "select", Name: "Seat heater front right", Options: seatLevels},
		{Key: "seat_heater_rear_left", Component: "select", Name: "Seat heater rear left", Options: seatLevels},
		{Key: "seat_heater_rear_center", Component: "select", Name: "Seat heater rear center", Options: seatLevels},
		{Key: "seat_heater_rear_right", Component: "select", Name: "Seat heater rear right", Options: seatLevels},
		// Software updates.
		{Key: "software_update", Component: "update", Name: "Software update"},
		{Key: "cancel_update", Component: "button", Name: "Cancel software update"},
	}
	// Navigation needs the regional Fleet API (navigation_request is not proxy-routed).
	if cfg.FleetAPIURL != "" {
		es = append(es, Entity{Key: "navigate", Component: "text", Name: "Navigate to"})
	}
	// PIN-gated commands — only exposed when the PIN is configured.
	if cfg.ValetPIN != "" {
		es = append(es, Entity{Key: "valet", Component: "switch", Name: "Valet mode"})
	}
	if cfg.SpeedLimitPIN != "" {
		es = append(es,
			Entity{Key: "speed_limit", Component: "switch", Name: "Speed limit"},
			Entity{Key: "speed_limit_value", Component: "number", Name: "Speed limit value", Min: 50, Max: 90, Step: 1, Unit: "mph"},
		)
	}
	if cfg.PinToDrivePIN != "" {
		es = append(es, Entity{Key: "pin_to_drive", Component: "switch", Name: "PIN to drive"})
	}
	return es
}

var seatLevels = []string{"off", "low", "medium", "high"}

// seatPositions maps a seat-heater entity key to the Tesla seat_position index.
var seatPositions = map[string]int{
	"seat_heater_front_left":  0,
	"seat_heater_front_right": 1,
	"seat_heater_rear_left":   2,
	"seat_heater_rear_center": 4,
	"seat_heater_rear_right":  5,
}

// locator supplies a vehicle's last-known GPS, needed by commands like
// window_control (close) and trigger_homelink. Implemented by *store.Store.
type locator interface {
	Snapshot(vin string) (store.Snapshot, bool)
}

type Relay struct {
	tm        *tokenManager
	proxy     string
	client    *http.Client
	apiClient *http.Client // TLS-verifying client for the public Fleet API (navigation)
	fleetAPI  string
	valetPIN  string
	speedPIN  string
	drivePIN  string
	log       *slog.Logger
	knownVINs map[string]bool
	store     locator
	wakeGroup singleflight.Group // coalesces concurrent wakes per VIN
}

// NewRelay builds the command relay. knownVINs is the set of configured VINs;
// commands for any VIN not in this set are rejected so an unvalidated VIN can
// never be interpolated into a proxy URL. store supplies GPS for location-gated
// commands (may be nil).
func NewRelay(cfg config.Commands, knownVINs []string, st locator, log *slog.Logger) *Relay {
	// Self-signed proxy cert → skip verify on the internal hop.
	insecure := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	known := make(map[string]bool, len(knownVINs))
	for _, vin := range knownVINs {
		known[vin] = true
	}
	r := &Relay{
		tm:        newTokenManager(cfg, log),
		proxy:     strings.TrimRight(cfg.ProxyURL, "/"),
		client:    insecure,
		apiClient: &http.Client{Timeout: 30 * time.Second},
		fleetAPI:  strings.TrimRight(cfg.FleetAPIURL, "/"),
		valetPIN:  cfg.ValetPIN,
		speedPIN:  cfg.SpeedLimitPIN,
		drivePIN:  cfg.PinToDrivePIN,
		log:       log,
		knownVINs: known,
		store:     st,
	}
	// Eager refresh so we know at startup whether the token is valid (and persist
	// the rotated token immediately). Commands won't work without a valid token.
	if _, err := r.tm.token(); err != nil {
		log.Error("initial token refresh FAILED — commands disabled until a valid refresh_token is provided", "err", err)
	}
	return r
}

// Handle maps an HA command (key + payload) to a Tesla command and sends it.
func (r *Relay) Handle(vin, key, payload string) {
	// An empty knownVINs set means zero-config auto-discovery: accept any VIN. A
	// non-empty set acts as an explicit allow-list.
	if len(r.knownVINs) > 0 && !r.knownVINs[vin] {
		r.log.Warn("rejecting command for unknown VIN", "vin", vin, "key", key)
		return
	}
	var err error
	switch key {
	// --- buttons ---
	case "flash_lights":
		err = r.command(vin, "flash_lights", nil)
	case "honk":
		err = r.command(vin, "honk_horn", nil)
	case "wake":
		err = r.wake(vin)
	// --- covers (frunk/trunk are toggles → OPEN and CLOSE both actuate) ---
	case "frunk":
		err = r.command(vin, "actuate_trunk", map[string]any{"which_trunk": "front"})
	case "trunk":
		err = r.command(vin, "actuate_trunk", map[string]any{"which_trunk": "rear"})
	case "charge_port":
		if coverOpen(payload) {
			err = r.command(vin, "charge_port_door_open", nil)
		} else {
			err = r.command(vin, "charge_port_door_close", nil)
		}
	case "windows":
		if coverOpen(payload) {
			err = r.command(vin, "window_control", map[string]any{"command": "vent", "lat": 0, "lon": 0})
		} else {
			lat, lon, ok := r.latlon(vin)
			if !ok {
				r.log.Warn("close windows needs GPS but none known", "vin", vin)
				return
			}
			err = r.command(vin, "window_control", map[string]any{"command": "close", "lat": lat, "lon": lon})
		}
	case "charge_max_range":
		err = r.command(vin, "charge_max_range", nil)
	case "charge_standard":
		err = r.command(vin, "charge_standard", nil)
	case "remote_start":
		err = r.command(vin, "remote_start_drive", nil)
	case "homelink":
		lat, lon, ok := r.latlon(vin)
		if !ok {
			r.log.Warn("homelink needs GPS but none known", "vin", vin)
			return
		}
		err = r.command(vin, "trigger_homelink", map[string]any{"lat": lat, "lon": lon})
	case "media_toggle":
		err = r.command(vin, "media_toggle_playback", nil)
	case "media_next":
		err = r.command(vin, "media_next_track", nil)
	case "media_prev":
		err = r.command(vin, "media_prev_track", nil)
	case "media_vol_up":
		err = r.command(vin, "media_volume_up", nil)
	case "media_vol_down":
		err = r.command(vin, "media_volume_down", nil)

	// --- switches ---
	case "charging":
		if isOn(payload) {
			err = r.command(vin, "charge_start", nil)
		} else {
			err = r.command(vin, "charge_stop", nil)
		}
	case "sentry":
		err = r.command(vin, "set_sentry_mode", map[string]any{"on": isOn(payload)})
	case "climate_mode":
		if strings.EqualFold(strings.TrimSpace(payload), "off") {
			err = r.command(vin, "auto_conditioning_stop", nil)
		} else {
			err = r.command(vin, "auto_conditioning_start", nil)
		}
	case "steering_wheel_heater":
		err = r.command(vin, "remote_steering_wheel_heater_request", map[string]any{"on": isOn(payload)})
	case "cabin_overheat":
		err = r.command(vin, "set_cabin_overheat_protection", map[string]any{"on": isOn(payload), "fan_only": false})
	case "preconditioning_max":
		err = r.command(vin, "set_preconditioning_max", map[string]any{"on": isOn(payload), "manual_override": true})
	case "guest_mode":
		err = r.command(vin, "guest_mode", map[string]any{"enable": isOn(payload)})

	// --- lock ---
	case "lock":
		if strings.EqualFold(payload, "LOCK") {
			err = r.command(vin, "door_lock", nil)
		} else {
			err = r.command(vin, "door_unlock", nil)
		}

	// --- numbers ---
	case "charge_limit":
		pct, e := parseNum(payload)
		if e != nil {
			r.log.Warn("bad charge_limit payload", "payload", payload)
			return
		}
		err = r.command(vin, "set_charge_limit", map[string]any{"percent": int(pct)})
	case "charging_amps":
		a, e := parseNum(payload)
		if e != nil {
			r.log.Warn("bad charging_amps payload", "payload", payload)
			return
		}
		err = r.command(vin, "set_charging_amps", map[string]any{"charging_amps": int(a)})
	case "climate_temp":
		t, e := parseNum(payload)
		if e != nil {
			r.log.Warn("bad climate_temp payload", "payload", payload)
			return
		}
		err = r.command(vin, "set_temps", map[string]any{"driver_temp": t, "passenger_temp": t})

	// --- selects ---
	case "climate_keeper":
		mode := map[string]int{"off": 0, "keep": 1, "dog": 2, "camp": 3}[strings.ToLower(strings.TrimSpace(payload))]
		err = r.command(vin, "set_climate_keeper_mode", map[string]any{"climate_keeper_mode": mode})

	// --- software updates ---
	case "software_update":
		// HA 'update' install press → install now (offset 0).
		err = r.command(vin, "schedule_software_update", map[string]any{"offset_sec": 0})
	case "cancel_update":
		err = r.command(vin, "cancel_software_update", nil)

	// --- navigation (direct Fleet API; not proxy-routed) ---
	case "navigate":
		err = r.navigate(vin, payload)

	// --- PIN-gated ---
	case "valet":
		if r.valetPIN == "" {
			r.log.Warn("valet command but no valet_pin configured")
			return
		}
		err = r.command(vin, "set_valet_mode", map[string]any{"on": isOn(payload), "password": r.valetPIN})
	case "speed_limit":
		if r.speedPIN == "" {
			r.log.Warn("speed_limit command but no speed_limit_pin configured")
			return
		}
		if isOn(payload) {
			err = r.command(vin, "speed_limit_activate", map[string]any{"pin": r.speedPIN})
		} else {
			err = r.command(vin, "speed_limit_deactivate", map[string]any{"pin": r.speedPIN})
		}
	case "speed_limit_value":
		v, e := parseNum(payload)
		if e != nil {
			r.log.Warn("bad speed_limit_value payload", "payload", payload)
			return
		}
		err = r.command(vin, "speed_limit_set_limit", map[string]any{"limit_mph": v})
	case "pin_to_drive":
		if r.drivePIN == "" {
			r.log.Warn("pin_to_drive command but no pin_to_drive_pin configured")
			return
		}
		err = r.command(vin, "set_pin_to_drive", map[string]any{"on": isOn(payload), "password": r.drivePIN})

	default:
		if pos, ok := seatPositions[key]; ok {
			lvl := map[string]int{"off": 0, "low": 1, "medium": 2, "high": 3}[strings.ToLower(strings.TrimSpace(payload))]
			err = r.command(vin, "remote_seat_heater_request", map[string]any{"seat_position": pos, "level": lvl})
			break
		}
		r.log.Warn("unknown command key", "key", key)
		return
	}
	if err != nil {
		r.log.Error("command failed", "vin", vin, "key", key, "err", err)
	} else {
		r.log.Info("command sent", "vin", vin, "key", key)
	}
}

// vehicleSummary is the subset of GET /api/1/vehicles/{vin} we consume.
type vehicleSummary struct {
	DisplayName string `json:"display_name"`
	State       string `json:"state"` // online | asleep | offline
}

// getVehicle fetches the vehicle summary from the Fleet API. Does not wake the
// car. Requires commands.fleet_api_url.
// vehicleDataEndpoints is the endpoint set requested from the real Fleet API.
// location_data is deliberately NOT requested: GPS comes from telemetry for free,
// and asking for it would make the call depend on a scope this seed does not need.
const vehicleDataEndpoints = "charge_state;climate_state;vehicle_config;vehicle_state;gui_settings;drive_state"

// VehicleData fetches the REAL Fleet API vehicle_data document for one VIN.
//
// This is the only paid Fleet API call in the gateway (about $0.002 a call). It
// exists because telemetry is delta-only: a field the car has not changed since
// enrollment is never sent at all, so slow-moving values (charge_limit_soc and
// sentry_mode before their first change, trim_badging, a parked odometer) are
// missing from the emulated document rather than merely stale. One real document
// seeds them, after which the stream keeps them current for free.
//
// It deliberately does NOT wake the car. Tesla answers 408 for a sleeping vehicle
// and that is returned as an error here, because waking a car to read it spends
// range on something a seed can simply wait for -- callers fetch opportunistically
// while the car is already online.
func (r *Relay) VehicleData(vin string) (map[string]any, error) {
	if r.fleetAPI == "" {
		return nil, fmt.Errorf("fleet_api_url not set")
	}
	tok, err := r.tm.token()
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	url := fmt.Sprintf("%s/api/1/vehicles/%s/vehicle_data?endpoints=%s", r.fleetAPI, vin, vehicleDataEndpoints)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := r.apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// A full vehicle_data document is far larger than the command responses this
	// client otherwise reads, so the cap is raised rather than reused -- truncating
	// it would produce a decode error that looks like an API fault.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.Response == nil {
		return nil, fmt.Errorf("vehicle_data had no response object")
	}
	return out.Response, nil
}

func (r *Relay) getVehicle(vin string) (vehicleSummary, error) {
	var vs vehicleSummary
	if r.fleetAPI == "" {
		return vs, fmt.Errorf("fleet_api_url not set")
	}
	tok, err := r.tm.token()
	if err != nil {
		return vs, fmt.Errorf("token: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/1/vehicles/%s", r.fleetAPI, vin), nil)
	if err != nil {
		return vs, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := r.apiClient.Do(req)
	if err != nil {
		return vs, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vs, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		Response vehicleSummary `json:"response"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return vs, err
	}
	out.Response.DisplayName = strings.TrimSpace(out.Response.DisplayName)
	out.Response.State = strings.TrimSpace(out.Response.State)
	return out.Response, nil
}

// VehicleDisplayName fetches the car's owner-given name from the Fleet API.
// Does not wake the car. Returns "" if unavailable.
func (r *Relay) VehicleDisplayName(vin string) (string, error) {
	vs, err := r.getVehicle(vin)
	if err != nil {
		return "", err
	}
	return vs.DisplayName, nil
}

// vehicleState returns the vehicle's top-level connectivity state
// ("online"/"asleep"/"offline") from the Fleet API. Does not wake the car.
func (r *Relay) vehicleState(vin string) (string, error) {
	vs, err := r.getVehicle(vin)
	if err != nil {
		return "", err
	}
	return vs.State, nil
}

// latlon returns the vehicle's last-known GPS from the store.
func (r *Relay) latlon(vin string) (float64, float64, bool) {
	if r.store == nil {
		return 0, 0, false
	}
	snap, ok := r.store.Snapshot(vin)
	if !ok {
		return 0, 0, false
	}
	return snap.Location()
}

func parseNum(p string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(p), 64)
}

// navigate sends a destination (address or "lat,lon") to the car. navigation_request
// is NOT end-to-end-signed, so it cannot go through the vehicle-command proxy — it is
// POSTed directly to the regional Fleet API with the OAuth token.
func (r *Relay) navigate(vin, destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return fmt.Errorf("empty destination")
	}
	if r.fleetAPI == "" {
		return fmt.Errorf("commands.fleet_api_url not set — navigation unavailable")
	}
	tok, err := r.tm.token()
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	body := map[string]any{
		"type":         "share_ext_content_raw",
		"locale":       "en-US",
		"timestamp_ms": strconv.FormatInt(time.Now().UnixMilli(), 10),
		"value":        map[string]any{"android.intent.extra.TEXT": destination},
	}
	b, _ := json.Marshal(body)
	urlStr := fmt.Sprintf("%s/api/1/vehicles/%s/command/navigation_request", r.fleetAPI, vin)
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

// command sends a signed command through the proxy. If the car is asleep (the
// proxy returns "vehicle unavailable: vehicle is offline or asleep"), it wakes
// the car and retries exactly once, so a command sent to a sleeping car still
// lands. Retrying after an explicit asleep error is safe even for momentary
// commands (honk/flash): the command provably did not execute, so it cannot
// double-fire. Errors unrelated to sleep are returned as-is without retry.
func (r *Relay) command(vin, name string, body map[string]any) error {
	urlStr := fmt.Sprintf("%s/api/1/vehicles/%s/command/%s", r.proxy, vin, name)
	err := r.post(urlStr, body)
	if !isAsleepErr(err) {
		return err
	}
	r.log.Info("command hit a sleeping car — waking, then retrying", "vin", vin, "cmd", name)
	if wErr := r.ensureAwake(vin); wErr != nil {
		return fmt.Errorf("%s: car asleep and wake failed: %w (original: %v)", name, wErr, err)
	}
	return r.post(urlStr, body)
}

func (r *Relay) wake(vin string) error {
	return r.post(fmt.Sprintf("%s/api/1/vehicles/%s/wake_up", r.proxy, vin), nil)
}

// isAsleepErr reports whether a command error is Tesla's "car is asleep/offline"
// condition. The vehicle-command proxy returns HTTP 500 wrapping
// "vehicle unavailable: vehicle is offline or asleep"; the status code alone is
// not distinctive, so the message text is matched.
func isAsleepErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "offline or asleep") || strings.Contains(s, "vehicle unavailable")
}

// ensureAwake wakes the vehicle and blocks until the Fleet API reports it online
// (or a timeout). Concurrent callers for the same VIN share one wake via
// singleflight, so a burst of commands against a sleeping car triggers a single
// wake_up. Returns nil once the car is online.
func (r *Relay) ensureAwake(vin string) error {
	_, err, _ := r.wakeGroup.Do(vin, func() (any, error) {
		return nil, r.wakeAndWait(vin)
	})
	return err
}

// wakeAndWait sends wake_up and polls the vehicle's state until it is online.
func (r *Relay) wakeAndWait(vin string) error {
	if err := r.wake(vin); err != nil {
		return fmt.Errorf("wake_up: %w", err)
	}
	// Without a Fleet API URL we cannot poll state — fall back to a fixed wait
	// and let the caller's retry find out whether the car actually woke.
	if r.fleetAPI == "" {
		time.Sleep(wakeTimeout / 2)
		return nil
	}
	deadline := time.Now().Add(wakeTimeout)
	var lastState string
	for {
		time.Sleep(wakePollInterval)
		state, err := r.vehicleState(vin)
		if err != nil {
			r.log.Warn("wake poll: state check failed", "vin", vin, "err", err)
		} else {
			lastState = state
			if state == "online" {
				time.Sleep(wakeReadyBuffer) // let the car settle before commanding
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("car did not come online within %s (last state %q)", wakeTimeout, lastState)
		}
	}
}

// Enroll pushes a fleet_telemetry_config to the vehicle-command proxy using the
// relay's own OAuth token (no contention with the command relay's token). payload
// is the raw fleet_telemetry_config JSON. A non-2xx response (including its body)
// is returned as an error.
func (r *Relay) Enroll(payload []byte) error {
	tok, err := r.tm.token()
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	urlStr := fmt.Sprintf("%s/api/1/vehicles/fleet_telemetry_config", r.proxy)
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

func (r *Relay) post(urlStr string, body map[string]any) error {
	tok, err := r.tm.token()
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, urlStr, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	return nil
}

func isOn(p string) bool {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "ON", "TRUE", "1", "LOCK", "PRESS":
		return true
	}
	return false
}

// coverOpen reports whether an HA cover payload requests opening (vs closing).
func coverOpen(p string) bool {
	return strings.EqualFold(strings.TrimSpace(p), "OPEN")
}

// ---- token manager ----

type tokenManager struct {
	cfg       config.Commands
	cachePath string
	log       *slog.Logger
	client    *http.Client

	mu      sync.Mutex
	access  string
	refresh string
	expiry  time.Time
}

func newTokenManager(cfg config.Commands, log *slog.Logger) *tokenManager {
	t := &tokenManager{
		cfg:       cfg,
		cachePath: cfg.TokenCache,
		log:       log,
		client:    &http.Client{Timeout: 20 * time.Second},
		refresh:   cfg.RefreshToken,
	}
	// Cached (rotated) refresh token takes precedence over the configured one.
	if t.cachePath != "" {
		if b, err := os.ReadFile(t.cachePath); err == nil {
			if cached := strings.TrimSpace(string(b)); cached != "" {
				t.refresh = cached
				log.Info("loaded refresh token from cache", "path", t.cachePath)
			}
		}
	}
	return t
}

// persist writes the current refresh token to the cache file (atomic rename).
func (t *tokenManager) persist() {
	if t.cachePath == "" {
		return
	}
	tmp := t.cachePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.refresh), 0o600); err != nil {
		t.log.Warn("persist refresh token failed", "err", err)
		return
	}
	if err := os.Rename(tmp, t.cachePath); err != nil {
		t.log.Warn("persist refresh token rename failed", "err", err)
		return
	}
	t.log.Info("persisted rotated refresh token", "path", t.cachePath)
}

// token returns a valid access token, refreshing if missing or near expiry.
func (t *tokenManager) token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.access != "" && time.Until(t.expiry) > 60*time.Second {
		return t.access, nil
	}
	if err := t.doRefresh(); err != nil {
		return "", err
	}
	return t.access, nil
}

func (t *tokenManager) doRefresh() error {
	endpoint := strings.TrimRight(t.cfg.AuthHost, "/") + t.cfg.AuthPath + "/token"
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", t.cfg.ClientID)
	form.Set("refresh_token", t.refresh)
	if t.cfg.ClientSecret != "" {
		form.Set("client_secret", t.cfg.ClientSecret)
	}
	resp, err := t.client.PostForm(endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return err
	}
	if out.AccessToken == "" {
		return fmt.Errorf("refresh returned no access_token")
	}
	t.access = out.AccessToken
	if out.RefreshToken != "" && out.RefreshToken != t.refresh {
		// Tesla rotates the refresh token on use; persist it so restarts survive.
		t.refresh = out.RefreshToken
		t.persist()
	}
	ttl := out.ExpiresIn
	if ttl <= 0 {
		ttl = 28800 // 8h default
	}
	t.expiry = time.Now().Add(time.Duration(ttl) * time.Second)
	t.log.Info("access token refreshed", "expires_in_s", ttl)
	return nil
}
