// Package config loads gateway configuration. The same binary runs as a Home
// Assistant add-on and standalone, so config is layered:
//
//	defaults → config.yaml (if mounted) → /data/options.json (HA add-on) → env (TGW_*)
//
// LoadRaw returns this layered config WITHOUT runtime derivation — use it for
// editing/persisting (Save). Load additionally validates and resolves derived
// runtime fields (vehicle ids, display-name fallback). Persisting raw config keeps
// derived values (and the VIN, which is PII) out of the written file.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current config schema. Bumped when a migration is needed;
// migrate() upgrades older files on load.
const SchemaVersion = 1

type Config struct {
	SchemaVersion int       `yaml:"schema_version"`
	Ingest        Ingest    `yaml:"ingest"`
	HTTP          HTTP      `yaml:"http"`
	FleetAPI      FleetAPI  `yaml:"fleetapi"`
	HA            HA        `yaml:"ha"`
	Commands      Commands  `yaml:"commands"`
	Vehicles      []Vehicle `yaml:"vehicles"`
	Units         Units     `yaml:"units"`
	State         State     `yaml:"state"`
	Recording     Recording `yaml:"recording"`
	Onboard       Onboard   `yaml:"onboard"`
	Stream        Stream    `yaml:"stream"`
	Debug         Debug     `yaml:"debug"`
	LogLevel      string    `yaml:"log_level"`
}

// Debug controls the diagnostic endpoint. Off by default, because /debug/state is
// the most sensitive thing this process serves: it dumps every stored telemetry
// field, which includes precise GPS, the destination of the active route and the
// odometer. That is a live location feed for a named vehicle, available to
// anything that can reach the port.
type Debug struct {
	// StateEnabled exposes GET /debug/state. When false the route returns 404.
	StateEnabled bool `yaml:"state_enabled"`
	// Token, if set, must be presented as `Authorization: Bearer <token>` or
	// `X-Debug-Token`. Empty means the endpoint is open once enabled — fine
	// behind a loopback bind, not fine otherwise.
	Token string `yaml:"token"`
}

// Stream runs the upstream Tesla components (fleet-telemetry, and the
// vehicle-command proxy when commands are enabled) as child processes inside the
// same container — the "all-in-one" mode the HA add-on uses so installing one
// add-on yields the whole stack. Standalone setups leave Embedded=false and run
// those as separate docker-compose services instead.
type Stream struct {
	Embedded bool `yaml:"embedded"`
	// FleetTelemetryBin / FleetTelemetryConfig: the fleet-telemetry server binary and
	// the generated server config (TLS, listen port, ZMQ dispatcher, namespace).
	FleetTelemetryBin    string `yaml:"fleet_telemetry_bin"`
	FleetTelemetryConfig string `yaml:"fleet_telemetry_config"`
	// ProxyBin is the tesla vehicle-command HTTP proxy binary (started only when
	// commands are enabled).
	ProxyBin string `yaml:"proxy_bin"`
	// TelemetryPort is the port fleet-telemetry listens on for the car's mTLS stream
	// (the car dials in here). The public/forwarded port is advertised at enrollment.
	TelemetryPort int `yaml:"telemetry_port"`
	// ZMQAddr the embedded fleet-telemetry dispatcher binds; the gateway SUB-connects
	// to the same address on localhost.
	ZMQBind string `yaml:"zmq_bind"`
	// TLSCert/TLSKey: when both are set, the embedded fleet-telemetry terminates TLS
	// itself (port-forward + Let's Encrypt). Leave empty for Cloudflare-Tunnel mode,
	// where the tunnel provides public TLS and fleet-telemetry runs plaintext behind it.
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
}

// Onboard is the guided Tesla onboarding wizard (separate HTTP listener — keys/token
// holder, kept off the auth-less Fleet API port). Standalone: open the port (set a
// password). HA add-on: served via ingress (HA-authenticated).
type Onboard struct {
	Enabled  bool   `yaml:"enabled"`
	Listen   string `yaml:"listen"`   // e.g. :8099
	Password string `yaml:"password"` // optional basic-auth for standalone
	DataDir  string `yaml:"data_dir"` // where keys/state live; default /data/onboard
	// WellKnownListen serves the partner public key (unauthenticated) at
	// /.well-known/appspecific/com.tesla.3p.public-key.pem so the user can route their
	// domain here instead of hosting the file by hand. Tesla fetches it over HTTPS.
	WellKnownListen string `yaml:"well_known_listen"` // e.g. :8098
}

// Recording appends every telemetry field update to a JSONL file for debugging
// value changes over time (e.g. a whole drive). Opt-in.
type Recording struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`   // e.g. /data/telemetry.jsonl
	MaxMB   int    `yaml:"max_mb"` // rotate at this size (one backup kept)
}

// Commands enables the command relay: HA buttons/switches → signed Tesla commands
// via the vehicle-command proxy. Needs a cmd-scoped OAuth token the gateway refreshes.
type Commands struct {
	Enabled      bool   `yaml:"enabled"`
	ProxyURL     string `yaml:"proxy_url"` // vehicle-command proxy, e.g. https://vehicle-command-proxy:4443
	AuthHost     string `yaml:"auth_host"` // https://auth.tesla.com
	AuthPath     string `yaml:"auth_path"` // /oauth2/v3
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	RefreshToken string `yaml:"refresh_token"`
	// TokenCache persists the (rotating) refresh token across restarts. Tesla
	// rotates the refresh token on use; the cached value takes precedence over
	// RefreshToken on startup. Mount a writable volume for this path.
	TokenCache string `yaml:"token_cache"`
	// EnrollFile is the path to a fleet_telemetry_config JSON file pushed to the
	// vehicle-command proxy via POST /admin/enroll (self-enroll). Default /data/ftc.json.
	EnrollFile string `yaml:"enroll_file"`
	// FleetAPIURL is the regional Fleet API base (e.g.
	// https://fleet-api.prd.eu.vn.cloud.tesla.com). Only needed for commands that
	// are NOT end-to-end-signed and therefore not routed by the vehicle-command
	// proxy (navigation_request). Leave empty to hide those entities.
	FleetAPIURL string `yaml:"fleet_api_url"`
	// PINs for PIN-gated commands. An entity is only exposed when its PIN is set.
	ValetPIN      string `yaml:"valet_pin"`
	SpeedLimitPIN string `yaml:"speed_limit_pin"`
	PinToDrivePIN string `yaml:"pin_to_drive_pin"`
}

// Ingest is the brokerless ZMQ feed from fleet-telemetry (zmq dispatcher).
type Ingest struct {
	ZMQAddr   string `yaml:"zmq_addr"`  // e.g. tcp://fleet-telemetry:5284
	Namespace string `yaml:"namespace"` // fleet-telemetry namespace (topic prefix)
}

type HTTP struct {
	Listen string `yaml:"listen"` // e.g. :4460
}

// FleetAPI toggles the Tesla Fleet API emulator (for TeslaMate to poll).
type FleetAPI struct {
	Enabled bool `yaml:"enabled"`
}

// HA toggles Home Assistant MQTT auto-discovery (egress to HA's own broker).
type HA struct {
	Enabled         bool   `yaml:"enabled"`
	Broker          string `yaml:"broker"` // HA's MQTT broker, e.g. tcp://192.168.1.10:1883
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	ClientID        string `yaml:"client_id"`
	DiscoveryPrefix string `yaml:"discovery_prefix"` // default homeassistant
	StateTopicBase  string `yaml:"state_topic_base"` // default tgw
	// PublishIntervalSeconds is how often per-VIN state is republished to HA.
	// Lower = more live device_tracker/position; defaults to 2 if <=0.
	PublishIntervalSeconds int `yaml:"publish_interval_seconds"`
	// IdentifierMode controls the public identifier used in MQTT topics, unique_id,
	// object_id and the HA device identifiers: "name" (slug of the car's name — keeps
	// the VIN out of entity_ids) or "vin" (use the VIN). Default "name". The VIN is
	// always available as the device serial_number regardless of this setting.
	IdentifierMode string `yaml:"device_identifier"`
	// UniqueIDSalt is mixed into every entity unique_id. Bump it (any new value) to
	// force HA to mint fresh entities — useful to escape sticky entity_ids left over
	// from earlier collisions (HA never auto-renames an entity_id once assigned).
	// Leave empty normally; object_ids/topics are unaffected.
	UniqueIDSalt string `yaml:"unique_id_salt"`
}

type Vehicle struct {
	VIN         string `yaml:"vin"`
	DisplayName string `yaml:"display_name"`
	Template    string `yaml:"template"` // path to captured vehicle_data template JSON
	// CarType overrides the vehicle_config.car_type that is otherwise derived
	// from the VIN. Only needed for a model the VIN decoder does not know, or
	// for a VIN that fails its check digit.
	CarType   string `yaml:"car_type"`
	ID        int64  `yaml:"id"`
	VehicleID int64  `yaml:"vehicle_id"`
}

// IDString is the decimal string form of the vehicle's ID.
func (v Vehicle) IDString() string {
	return strconv.FormatInt(v.ID, 10)
}

// Slug is a filesystem/entity-id-safe form of the display name, used (in "name"
// identifier mode) as the HA object_id/topic prefix so entity_ids read e.g.
// "sensor.tesla_test_battery" instead of exposing the VIN. When no real name is
// available it falls back to "tesla_<hash>" — an opaque stable id derived from the
// VIN, never the VIN itself (which is PII).
func (v Vehicle) Slug() string {
	base := v.DisplayName
	if base == "" || base == v.VIN {
		return "tesla_" + vinHash(v.VIN)
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(base) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		case !prevUnderscore:
			b.WriteRune('_')
			prevUnderscore = true
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		return "tesla_" + vinHash(v.VIN)
	}
	return s
}

// vinHash is a short, stable, non-reversible-at-a-glance id derived from the VIN
// (FNV-1a, 24 bits → 6 hex). Used as an opaque fallback identifier, never exposing
// the raw VIN.
func vinHash(vin string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(vin); i++ {
		h ^= uint32(vin[i])
		h *= 16777619
	}
	return fmt.Sprintf("%06x", h&0xffffff)
}

// VehiclesByKey builds a lookup of vehicles keyed by VIN, ID and VehicleID
// (all as strings), shared by the Fleet API and WSS servers.
func VehiclesByKey(cfg *Config) map[string]Vehicle {
	byKey := make(map[string]Vehicle, len(cfg.Vehicles)*3)
	for _, v := range cfg.Vehicles {
		byKey[v.VIN] = v
		byKey[strconv.FormatInt(v.ID, 10)] = v
		byKey[strconv.FormatInt(v.VehicleID, 10)] = v
	}
	return byKey
}

// Units controls unit handling. The fleet-telemetry stream emits distance/speed in
// the *Input units (Tesla streams miles/mph by convention; verified), temperature in
// °C and pressure in bar. vehicle_data (for TeslaMate) is always normalized to miles
// using the *Input fields. The Home Assistant display unit is chosen via System
// (metric → km, km/h, °C, bar | imperial → mi, mph, °F, psi); users can still
// override per entity in HA's UI.
type Units struct {
	System        string `yaml:"system"`         // metric|imperial — HA display
	RangeInput    string `yaml:"range_input"`    // stream unit km|mi
	SpeedInput    string `yaml:"speed_input"`    // stream unit kmh|mph
	OdometerInput string `yaml:"odometer_input"` // stream unit km|mi
}

type State struct {
	StaleAfterSeconds    int    `yaml:"stale_after_seconds"`
	OnlineGraceSeconds   int    `yaml:"online_grace_seconds"`
	ReportAsleepWhenIdle bool   `yaml:"report_asleep_when_idle"`
	// SnapshotPath persists the last-known field values to disk so they survive a
	// gateway restart: a car that is asleep/away streams nothing, so without this
	// its battery/charge-limit/plugged_in sensors would read "unknown" after every
	// restart until it next wakes. Empty disables persistence. Default /data/state.json.
	SnapshotPath string `yaml:"snapshot_path"`
}

func Defaults() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Ingest:        Ingest{ZMQAddr: "tcp://fleet-telemetry:5284", Namespace: "tesla_telemetry"},
		HTTP:          HTTP{Listen: ":4460"},
		// Default profile is HA-only (see README). The Fleet API for TeslaMate is
		// opt-in: set fleetapi_enabled (add-on) / TGW_FLEETAPI_ENABLED / fleetapi.enabled.
		FleetAPI: FleetAPI{Enabled: false},
		HA: HA{
			Enabled:                true,
			ClientID:               "community-teslafleet",
			DiscoveryPrefix:        "homeassistant",
			StateTopicBase:         "tgw",
			PublishIntervalSeconds: 2,
			IdentifierMode:         "name",
		},
		Commands: Commands{
			ProxyURL:   "https://vehicle-command-proxy:4443",
			AuthHost:   "https://auth.tesla.com",
			AuthPath:   "/oauth2/v3",
			TokenCache: "/data/refresh_token",
			EnrollFile: "/data/ftc.json",
		},
		Units:     Units{System: "metric", RangeInput: "mi", SpeedInput: "mph", OdometerInput: "mi"},
		Recording: Recording{Path: "/data/telemetry.jsonl", MaxMB: 100},
		Onboard:   Onboard{Listen: ":8099", DataDir: "/data/onboard", WellKnownListen: ":8098"},
		Stream: Stream{
			FleetTelemetryBin:    "/usr/local/bin/fleet-telemetry",
			FleetTelemetryConfig: "/data/fleet-telemetry/config.json",
			ProxyBin:             "/usr/local/bin/tesla-http-proxy",
			TelemetryPort:        4443,
			ZMQBind:              "tcp://0.0.0.0:5284",
		},
		// OnlineGrace 300s keeps a parked-but-connected car "online" between sparse
		// battery telemetry updates (Soc 60s / RatedRange 120s); it flips to asleep
		// only after telemetry genuinely stops. Keeps TeslaMate's WSS stream open.
		State:    State{StaleAfterSeconds: 660, OnlineGraceSeconds: 300, SnapshotPath: "/data/state.json"},
		LogLevel: "info",
	}
}

// LoadRaw layers defaults → config.yaml → /data/options.json (HA add-on) → env, WITHOUT
// validation or runtime derivation. Use it for editing and persisting (Save), so derived
// ids/names (and the VIN) never leak into the written file. A missing file is not an error.
func LoadRaw(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config %s: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
	}
	migrate(&cfg)
	applyOptions(&cfg) // HA add-on options.json (no-op when absent, e.g. standalone)
	applyEnv(&cfg)
	return cfg, nil
}

// Load returns the runtime config: LoadRaw, then validate (errors only) and resolve
// (derive vehicle ids + display-name fallback). main() uses this.
func Load(path string) (Config, error) {
	cfg, err := LoadRaw(path)
	if err != nil {
		return cfg, err
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	cfg.resolve()
	return cfg, nil
}

// Save writes cfg to path atomically (tmp + rename, 0600). Callers should pass a RAW
// (un-resolved) config — e.g. from LoadRaw — so derived ids/names are not persisted.
func Save(path string, cfg Config) error {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

const secretMask = "••••"

// Redact returns a copy of cfg with secret fields masked, safe to expose to a UI/status
// endpoint. Nested structs are values, so the copy does not alias the original's secrets.
func (cfg Config) Redact() Config {
	mask := func(s string) string {
		if s == "" {
			return ""
		}
		return secretMask
	}
	cfg.HA.Password = mask(cfg.HA.Password)
	cfg.Commands.ClientSecret = mask(cfg.Commands.ClientSecret)
	cfg.Commands.RefreshToken = mask(cfg.Commands.RefreshToken)
	cfg.Commands.ValetPIN = mask(cfg.Commands.ValetPIN)
	cfg.Commands.SpeedLimitPIN = mask(cfg.Commands.SpeedLimitPIN)
	cfg.Commands.PinToDrivePIN = mask(cfg.Commands.PinToDrivePIN)
	return cfg
}

// MergeSecrets fills masked secret fields in `in` (a config coming back from a UI) with
// the values from `existing`, so a redacted "••••" round-tripped from the UI does not
// blank the stored secret. Non-masked values overwrite (genuine changes).
func MergeSecrets(in *Config, existing Config) {
	keep := func(field *string, old string) {
		if *field == secretMask {
			*field = old
		}
	}
	keep(&in.HA.Password, existing.HA.Password)
	keep(&in.Commands.ClientSecret, existing.Commands.ClientSecret)
	keep(&in.Commands.RefreshToken, existing.Commands.RefreshToken)
	keep(&in.Commands.ValetPIN, existing.Commands.ValetPIN)
	keep(&in.Commands.SpeedLimitPIN, existing.Commands.SpeedLimitPIN)
	keep(&in.Commands.PinToDrivePIN, existing.Commands.PinToDrivePIN)
}

// migrate upgrades an older config in place. No migrations yet; stamps the version.
func migrate(c *Config) {
	if c.SchemaVersion == 0 {
		// Pre-versioned file: treat as current (no structural changes have shipped).
		c.SchemaVersion = SchemaVersion
	}
	// Future: switch on c.SchemaVersion to transform old layouts, then bump.
}

// applyOptions overlays a Home Assistant add-on options.json (flat key/value) onto the
// config. Absent (standalone) → no-op. The mapping is the small set of keys exposed in
// the add-on's options schema; everything else is configured via env or config.yaml.
func applyOptions(c *Config) {
	path := os.Getenv("TGW_OPTIONS_FILE")
	if path == "" {
		path = "/data/options.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var o map[string]any
	if json.Unmarshal(b, &o) != nil {
		return
	}
	optStr(o, "zmq_addr", &c.Ingest.ZMQAddr)
	optStr(o, "namespace", &c.Ingest.Namespace)
	optStr(o, "units_system", &c.Units.System)
	optStr(o, "device_identifier", &c.HA.IdentifierMode)
	optStr(o, "log_level", &c.LogLevel)
	optBool(o, "fleetapi_enabled", &c.FleetAPI.Enabled)
	optStr(o, "mqtt_broker", &c.HA.Broker)
	optStr(o, "mqtt_username", &c.HA.Username)
	optStr(o, "mqtt_password", &c.HA.Password)
	optBool(o, "embedded_stream", &c.Stream.Embedded)
	optInt(o, "telemetry_port", &c.Stream.TelemetryPort)
	optBool(o, "commands_enabled", &c.Commands.Enabled)
	optStr(o, "tesla_client_id", &c.Commands.ClientID)
	optStr(o, "tesla_client_secret", &c.Commands.ClientSecret)
	optStr(o, "tesla_refresh_token", &c.Commands.RefreshToken)
	optStr(o, "fleet_api_url", &c.Commands.FleetAPIURL)
	if v, ok := o["vins"].(string); ok && strings.TrimSpace(v) != "" && len(c.Vehicles) == 0 {
		c.Vehicles = parseVINs(v, "")
	}
}

func optStr(o map[string]any, key string, dst *string) {
	if v, ok := o[key].(string); ok && v != "" {
		*dst = v
	}
}

func optBool(o map[string]any, key string, dst *bool) {
	if v, ok := o[key].(bool); ok {
		*dst = v
	}
}

func optInt(o map[string]any, key string, dst *int) {
	// JSON numbers decode to float64.
	if v, ok := o[key].(float64); ok {
		*dst = int(v)
	}
}

func applyEnv(c *Config) {
	setStr(&c.Ingest.ZMQAddr, "TGW_INGEST_ZMQ_ADDR")
	setStr(&c.Ingest.Namespace, "TGW_INGEST_NAMESPACE")
	setStr(&c.HTTP.Listen, "TGW_HTTP_LISTEN")
	setBool(&c.FleetAPI.Enabled, "TGW_FLEETAPI_ENABLED")
	setBool(&c.Debug.StateEnabled, "TGW_DEBUG_STATE_ENABLED")
	setStr(&c.Debug.Token, "TGW_DEBUG_TOKEN")
	setBool(&c.HA.Enabled, "TGW_HA_ENABLED")
	setStr(&c.HA.Broker, "TGW_HA_BROKER")
	setStr(&c.HA.Username, "TGW_HA_USERNAME")
	setStr(&c.HA.Password, "TGW_HA_PASSWORD")
	setStr(&c.HA.ClientID, "TGW_HA_CLIENT_ID")
	setStr(&c.HA.DiscoveryPrefix, "TGW_HA_DISCOVERY_PREFIX")
	setStr(&c.HA.StateTopicBase, "TGW_HA_STATE_TOPIC_BASE")
	setInt(&c.HA.PublishIntervalSeconds, "TGW_HA_PUBLISH_INTERVAL_SECONDS")
	setStr(&c.HA.IdentifierMode, "TGW_HA_DEVICE_IDENTIFIER")
	setStr(&c.HA.UniqueIDSalt, "TGW_HA_UNIQUE_ID_SALT")
	setStr(&c.Units.System, "TGW_UNITS_SYSTEM")
	setStr(&c.Units.RangeInput, "TGW_UNITS_RANGE_INPUT")
	setStr(&c.Units.SpeedInput, "TGW_UNITS_SPEED_INPUT")
	setStr(&c.Units.OdometerInput, "TGW_UNITS_ODOMETER_INPUT")
	setBool(&c.Commands.Enabled, "TGW_COMMANDS_ENABLED")
	setStr(&c.Commands.ProxyURL, "TGW_TESLA_PROXY_URL")
	setStr(&c.Commands.AuthHost, "TGW_TESLA_AUTH_HOST")
	setStr(&c.Commands.AuthPath, "TGW_TESLA_AUTH_PATH")
	setStr(&c.Commands.ClientID, "TGW_TESLA_CLIENT_ID")
	setStr(&c.Commands.ClientSecret, "TGW_TESLA_CLIENT_SECRET")
	setStr(&c.Commands.RefreshToken, "TGW_TESLA_REFRESH_TOKEN")
	setStr(&c.Commands.TokenCache, "TGW_TESLA_TOKEN_CACHE")
	setStr(&c.Commands.EnrollFile, "TGW_ENROLL_FILE")
	setStr(&c.Commands.FleetAPIURL, "TGW_TESLA_FLEET_API_URL")
	setStr(&c.Commands.ValetPIN, "TGW_TESLA_VALET_PIN")
	setStr(&c.Commands.SpeedLimitPIN, "TGW_TESLA_SPEED_LIMIT_PIN")
	setStr(&c.Commands.PinToDrivePIN, "TGW_TESLA_PIN_TO_DRIVE_PIN")
	setBool(&c.Recording.Enabled, "TGW_RECORDING_ENABLED")
	setStr(&c.Recording.Path, "TGW_RECORDING_PATH")
	setInt(&c.Recording.MaxMB, "TGW_RECORDING_MAX_MB")
	setInt(&c.State.OnlineGraceSeconds, "TGW_STATE_ONLINE_GRACE_SECONDS")
	setInt(&c.State.StaleAfterSeconds, "TGW_STATE_STALE_AFTER_SECONDS")
	setBool(&c.State.ReportAsleepWhenIdle, "TGW_STATE_REPORT_ASLEEP_WHEN_IDLE")
	setStr(&c.State.SnapshotPath, "TGW_STATE_SNAPSHOT_PATH")
	setBool(&c.Onboard.Enabled, "TGW_ONBOARD_ENABLED")
	setStr(&c.Onboard.Listen, "TGW_ONBOARD_LISTEN")
	setStr(&c.Onboard.Password, "TGW_ONBOARD_PASSWORD")
	setStr(&c.Onboard.DataDir, "TGW_ONBOARD_DATA_DIR")
	setStr(&c.Onboard.WellKnownListen, "TGW_ONBOARD_WELLKNOWN_LISTEN")
	setBool(&c.Stream.Embedded, "TGW_STREAM_EMBEDDED")
	setStr(&c.Stream.FleetTelemetryBin, "TGW_STREAM_FLEET_TELEMETRY_BIN")
	setStr(&c.Stream.FleetTelemetryConfig, "TGW_STREAM_FLEET_TELEMETRY_CONFIG")
	setStr(&c.Stream.ProxyBin, "TGW_STREAM_PROXY_BIN")
	setInt(&c.Stream.TelemetryPort, "TGW_STREAM_TELEMETRY_PORT")
	setStr(&c.Stream.ZMQBind, "TGW_STREAM_ZMQ_BIND")
	setStr(&c.Stream.TLSCert, "TGW_STREAM_TLS_CERT")
	setStr(&c.Stream.TLSKey, "TGW_STREAM_TLS_KEY")
	setStr(&c.LogLevel, "TGW_LOG_LEVEL")

	if len(c.Vehicles) == 0 {
		if v := os.Getenv("TGW_VINS"); v != "" {
			c.Vehicles = parseVINs(v, os.Getenv("TGW_TEMPLATE_DIR"))
		}
	}
}

// parseVINs turns a comma-separated VIN list into vehicles, optionally pointing each at
// a template under dir. Shared by env (TGW_VINS) and add-on options (vins).
func parseVINs(list, dir string) []Vehicle {
	var out []Vehicle
	for _, vin := range strings.Split(list, ",") {
		vin = strings.TrimSpace(vin)
		if vin == "" {
			continue
		}
		veh := Vehicle{VIN: vin}
		if dir != "" {
			veh.Template = strings.TrimRight(dir, "/") + "/" + vin + ".json"
		}
		out = append(out, veh)
	}
	return out
}

// validate checks for fatal misconfiguration. It does NOT mutate cfg — derivation of
// runtime fields happens in resolve(), so a config loaded for editing/persisting stays
// exactly as the user wrote it.
func (c *Config) validate() error {
	if c.Ingest.ZMQAddr == "" {
		return fmt.Errorf("ingest.zmq_addr is required")
	}
	if c.Ingest.Namespace == "" {
		return fmt.Errorf("ingest.namespace is required")
	}
	// Zero vehicles is valid: with no configured VINs the gateway auto-discovers
	// every car it sees on the telemetry stream. A non-empty vehicles[] acts as an
	// explicit allow-list instead.
	for i := range c.Vehicles {
		if c.Vehicles[i].VIN == "" {
			return fmt.Errorf("vehicles[%d].vin is required", i)
		}
	}
	if c.HA.Enabled && c.HA.Broker == "" {
		return fmt.Errorf("ha.broker is required when ha.enabled")
	}
	if c.Commands.Enabled {
		if !c.HA.Enabled {
			return fmt.Errorf("commands.enabled requires ha.enabled (commands arrive via HA MQTT)")
		}
		if c.Commands.ClientID == "" || c.Commands.RefreshToken == "" {
			return fmt.Errorf("commands.enabled requires client_id and refresh_token")
		}
		if c.Commands.ProxyURL == "" {
			return fmt.Errorf("commands.proxy_url is required when commands.enabled")
		}
	}
	return nil
}

// resolve derives runtime-only fields: stable per-vehicle ids and a display-name
// fallback. It mutates cfg and is applied only to the runtime config (Load), never to
// what Save persists — so the VIN is not baked into config.yaml as a display name.
// (Slug() turns an empty/VIN display name into an opaque hash; main() auto-names from
// the Fleet API when DisplayName is still the VIN.)
func (c *Config) resolve() {
	for i := range c.Vehicles {
		if c.Vehicles[i].ID == 0 {
			c.Vehicles[i].ID = deriveID(c.Vehicles[i].VIN)
		}
		if c.Vehicles[i].VehicleID == 0 {
			c.Vehicles[i].VehicleID = c.Vehicles[i].ID
		}
		if c.Vehicles[i].DisplayName == "" {
			c.Vehicles[i].DisplayName = c.Vehicles[i].VIN
		}
	}
}

// AutoVehicle builds a Vehicle for a VIN discovered on the stream (not in config),
// with stable derived ids and no display name — Slug() then yields a VIN-free
// "tesla_<hash>" id, and the HA publisher names the device "Tesla <model>".
func AutoVehicle(vin string) Vehicle {
	id := deriveID(vin)
	return Vehicle{VIN: vin, ID: id, VehicleID: id}
}

// deriveID produces a stable positive int64 id from a VIN (FNV-1a) so TeslaMate
// has a consistent vehicle id across restarts.
func deriveID(vin string) int64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(vin); i++ {
		h ^= uint64(vin[i])
		h *= 1099511628211
	}
	return int64(h%1_000_000_000_000) + 1
}

// setStr reads a setting from env, or from the file named by env+"_FILE".
//
// The _FILE form exists so a credential never has to appear in a compose file,
// an image layer, or `docker inspect` output — the same convention Tesla's own
// vehicle-command proxy and most infrastructure images already use. It applies
// to every string setting rather than a curated list of secrets: there is no
// cost to that, and a list is one more thing to forget to update.
//
// _FILE wins when both are set. That direction matters: migrating a secret out
// of an environment variable must not silently keep reading the stale inline
// copy you are trying to delete.
func setStr(dst *string, env string) {
	if path, ok := os.LookupEnv(env + "_FILE"); ok && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			// Deliberately fall through to the plain variable instead of leaving
			// the value empty: a typo'd path must not silently look like "this
			// setting was never configured", which for a credential surfaces as
			// a confusing auth failure somewhere far away.
			slog.Error("cannot read setting file, falling back to the plain env var",
				"env", env+"_FILE", "path", path, "err", err)
		} else {
			// Trailing newline only — `echo secret > file` adds one, and a
			// password may legitimately end in a space.
			*dst = strings.TrimRight(string(b), "\r\n")
			slog.Info("loaded setting from file", "env", env, "path", path)
			return
		}
	}
	if v, ok := os.LookupEnv(env); ok {
		*dst = v
	}
}

func setBool(dst *bool, env string) {
	if v, ok := os.LookupEnv(env); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		} else {
			slog.Warn("ignoring unparseable bool env var", "env", env, "value", v, "err", err)
		}
	}
}

func setInt(dst *int, env string) {
	if v, ok := os.LookupEnv(env); ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		} else {
			slog.Warn("ignoring unparseable int env var", "env", env, "value", v, "err", err)
		}
	}
}
