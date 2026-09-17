package hadiscovery

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/LasseLegarth/community-teslafleet/internal/commands"
	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Publisher connects to Home Assistant's MQTT broker, publishes retained
// auto-discovery configs once, then publishes per-VIN state on a ticker.
type Publisher struct {
	client   paho.Client
	cfg      *config.Config
	store    *store.Store
	log      *slog.Logger
	entities []entity
	relay    *commands.Relay // optional: command relay (may be nil)
	interval time.Duration
	stop     chan struct{}

	// discovered tracks generic per-field discovery already published, keyed by
	// VIN -> telemetry field name, so each generic sensor is announced once.
	// Only touched from the single publishState goroutine, so no lock is needed.
	discovered map[string]map[string]bool

	// announced tracks which VINs have had their device-level discovery (curated +
	// command entities) published. Touched from both the publishState loop and the
	// MQTT on-connect callback, so it is guarded by annMu.
	announced map[string]bool
	annMu     sync.Mutex

	// covered is the set of telemetry field names represented by a curated entity;
	// generic per-field discovery skips them. Computed once.
	covered map[string]bool
}

func NewPublisher(cfg *config.Config, st *store.Store, relay *commands.Relay, log *slog.Logger) *Publisher {
	interval := time.Duration(cfg.HA.PublishIntervalSeconds) * time.Second
	if cfg.HA.PublishIntervalSeconds <= 0 {
		interval = 2 * time.Second
	}
	return &Publisher{
		cfg:      cfg,
		store:    st,
		log:      log,
		entities:   catalog(cfg.Units),
		relay:      relay,
		interval:   interval,
		stop:       make(chan struct{}),
		discovered: map[string]map[string]bool{},
		announced:  map[string]bool{},
		covered:    coveredFields(),
	}
}

func (p *Publisher) Start() error {
	opts := paho.NewClientOptions().
		AddBroker(p.cfg.HA.Broker).
		SetClientID(p.cfg.HA.ClientID + "-ha").
		SetAutoReconnect(true).
		SetCleanSession(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(c paho.Client) {
			// Re-announce on every (re)connect so retained discovery survives a broker
			// restart. Clearing announced makes ensureAnnounced re-publish for all
			// currently-known vehicles (configured + already auto-discovered).
			p.annMu.Lock()
			p.announced = map[string]bool{}
			p.annMu.Unlock()
			for _, v := range p.effectiveVehicles() {
				p.ensureAnnounced(v)
			}
			p.log.Info("ha discovery published", "broker", p.cfg.HA.Broker)
			if p.relay != nil {
				topic := p.cfg.HA.StateTopicBase + "/+/cmd/+/set"
				if tok := c.Subscribe(topic, 1, p.handleCommand); tok.Wait() && tok.Error() != nil {
					p.log.Error("command subscribe failed", "topic", topic, "err", tok.Error())
				} else {
					p.log.Info("command relay subscribed", "topic", topic)
				}
			}
		})
	if p.cfg.HA.Username != "" {
		opts.SetUsername(p.cfg.HA.Username)
		opts.SetPassword(p.cfg.HA.Password)
	}
	p.client = paho.NewClient(opts)
	if tok := p.client.Connect(); tok.Wait() && tok.Error() != nil {
		return tok.Error()
	}
	go p.loop()
	return nil
}

func (p *Publisher) Stop() {
	close(p.stop)
	p.client.Disconnect(250)
}

func (p *Publisher) loop() {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.publishState()
		}
	}
}

// effectiveVehicles returns the configured vehicles plus an auto-built Vehicle for
// every VIN seen on the stream that is not in config — so a zero-config gateway
// publishes whatever cars appear. Configured entries win (keep display_name/template).
func (p *Publisher) effectiveVehicles() []config.Vehicle {
	out := make([]config.Vehicle, 0, len(p.cfg.Vehicles))
	inCfg := make(map[string]bool, len(p.cfg.Vehicles))
	for _, v := range p.cfg.Vehicles {
		out = append(out, v)
		inCfg[v.VIN] = true
	}
	for _, vin := range p.store.VINs() {
		if !inCfg[vin] {
			out = append(out, config.AutoVehicle(vin))
		}
	}
	return out
}

// ensureAnnounced publishes a vehicle's device-level discovery (curated + command
// entities) exactly once per (re)connect. Safe to call from both the on-connect
// callback and the publishState loop.
func (p *Publisher) ensureAnnounced(v config.Vehicle) {
	p.annMu.Lock()
	if p.announced[v.VIN] {
		p.annMu.Unlock()
		return
	}
	p.announced[v.VIN] = true
	p.annMu.Unlock()
	if err := p.publishVehicleDiscovery(v); err != nil {
		p.log.Error("vehicle discovery publish failed", "vin", v.VIN, "err", err)
		// Roll back so the next tick retries this vehicle.
		p.annMu.Lock()
		delete(p.announced, v.VIN)
		p.annMu.Unlock()
	}
}

// pubID is the public identifier for a vehicle per ha.device_identifier: the name
// slug ("name", default — keeps the VIN out of entity_ids) or the VIN ("vin").
func (p *Publisher) pubID(v config.Vehicle) string {
	if strings.EqualFold(p.cfg.HA.IdentifierMode, "vin") {
		return v.VIN
	}
	return v.Slug()
}

// uid builds an entity unique_id, mixing in the configurable salt so it can be
// bumped to escape sticky entity_ids. object_id/topics are unaffected.
func (p *Publisher) uid(v config.Vehicle, key string) string {
	base := p.pubID(v)
	if s := p.cfg.HA.UniqueIDSalt; s != "" {
		return base + "_" + s + "_" + key
	}
	return base + "_" + key
}

// vinForPubID maps a public identifier (from a command topic) back to its VIN.
func (p *Publisher) vinForPubID(id string) (string, bool) {
	for _, v := range p.effectiveVehicles() {
		if p.pubID(v) == id {
			return v.VIN, true
		}
	}
	return "", false
}

func (p *Publisher) stateTopic(id string) string {
	return fmt.Sprintf("%s/%s/state", p.cfg.HA.StateTopicBase, id)
}

func (p *Publisher) trackerTopic(id, name string) string {
	return fmt.Sprintf("%s/%s/%s", p.cfg.HA.StateTopicBase, id, name)
}

// trackerSpecs maps each device_tracker's published topic to the lat/lon keys in
// the state map produced by buildState.
var trackerSpecs = []struct{ topic, latKey, lonKey string }{
	{"location", "latitude", "longitude"},
	{"destination", "destination_latitude", "destination_longitude"},
	{"origin", "origin_latitude", "origin_longitude"},
}

// publishTrackers publishes each known GPS position to its own RETAINED topic as
// {latitude, longitude, gps_accuracy}, so the device_tracker has coordinates that
// survive reconnects (independent of the 2s non-retained state blob).
func (p *Publisher) publishTrackers(vin string, st map[string]any) {
	for _, t := range trackerSpecs {
		lat, okLat := st[t.latKey]
		lon, okLon := st[t.lonKey]
		if !okLat || !okLon {
			continue
		}
		payload, err := json.Marshal(map[string]any{"latitude": lat, "longitude": lon, "gps_accuracy": 5})
		if err != nil {
			continue
		}
		p.client.Publish(p.trackerTopic(vin, t.topic), 1, true, payload) // retained QoS1
	}
}

// deviceInfo builds the HA device registry block for a vehicle, decoding the model
// from the VIN and (best-effort) the software version from current telemetry.
func (p *Publisher) deviceInfo(v config.Vehicle) map[string]any {
	// identifiers/topics use pubID (name slug by default), keeping the VIN out of
	// entity_ids. The VIN is still surfaced as serial_number — a value visible in the
	// device info, not in any entity_id.
	// Friendly device name. For auto-discovered vehicles (no display_name, or it's
	// just the VIN) fall back to "Tesla <model>" — never the raw VIN in the name.
	name := v.DisplayName
	if name == "" || name == v.VIN {
		name = "Tesla " + modelFromVIN(v.VIN)
	}
	dev := map[string]any{
		"identifiers":   []string{p.pubID(v)},
		"name":          name,
		"manufacturer":  "Tesla",
		"model":         modelFromVIN(v.VIN),
		"serial_number": v.VIN,
	}
	if p.store != nil {
		if snap, ok := p.store.Snapshot(v.VIN); ok {
			if fv, ok := snap.Field(store.FieldVersion); ok {
				if s, ok := fv.Value.(string); ok && s != "" {
					dev["sw_version"] = s
				}
			}
		}
	}
	return dev
}

func (p *Publisher) publishState() {
	now := p.store.Now()
	for _, v := range p.effectiveVehicles() {
		// Announce device-level discovery for any vehicle that appeared on the stream
		// after the last (re)connect, so a zero-config gateway lights up in HA.
		p.ensureAnnounced(v)
		snap, _ := p.store.Snapshot(v.VIN)
		d := store.Derive(snap, p.cfg.State, now)
		// Announce generic discovery for any newly-seen, uncovered field before
		// publishing state, so HA has the entity by the time the value lands.
		p.discoverNewFields(v, snap)
		st := buildState(snap, d, p.cfg.Units)
		payload, err := json.Marshal(st)
		if err != nil {
			p.log.Error("marshal state failed", "vin", v.VIN, "err", err)
			continue
		}
		// QoS0 fire-and-forget; state is republished every interval.
		p.client.Publish(p.stateTopic(p.pubID(v)), 0, false, payload)
		// Dedicated retained GPS topics for the device_trackers.
		p.publishTrackers(p.pubID(v), st)
	}
}

// discoverNewFields publishes a generic HA sensor discovery for each telemetry
// field present in snap that is not covered by a curated entity and not yet
// discovered for this VIN. Each field is published exactly once.
func (p *Publisher) discoverNewFields(v config.Vehicle, snap store.Snapshot) {
	seen := p.discovered[v.VIN]
	if seen == nil {
		seen = map[string]bool{}
		p.discovered[v.VIN] = seen
	}
	dev := p.deviceInfo(v)
	origin := map[string]any{"name": "community-teslafleet"}
	for name, fv := range snap.Fields {
		if seen[name] || p.covered[name] {
			continue
		}
		// Skip dict/composite values that have no scalar rendering (the curated
		// composites already expose their sub-values; any other dict is noise).
		if _, isMap := fv.Value.(map[string]any); isMap {
			seen[name] = true
			continue
		}
		component, cfg := p.genericDiscoveryConfig(v, name, fv.Value, dev, origin)
		topic := fmt.Sprintf("%s/%s/%s/raw_%s/config", p.cfg.HA.DiscoveryPrefix, component, p.pubID(v), name)
		payload, err := json.Marshal(cfg)
		if err != nil {
			p.log.Error("marshal generic discovery failed", "vin", v.VIN, "field", name, "err", err)
			continue
		}
		// Retained QoS1 so HA keeps the entity definition across restarts.
		if tok := p.client.Publish(topic, 1, true, payload); tok.Wait() && tok.Error() != nil {
			p.log.Error("generic discovery publish failed", "topic", topic, "err", tok.Error())
			continue
		}
		seen[name] = true
		p.log.Debug("generic discovery published", "vin", v.VIN, "field", name)
	}
}

// genericDiscoveryConfig builds a discovery config for an uncurated telemetry field
// keyed by its Tesla field name. Bools become binary_sensors; numerics get
// heuristic device_class/unit; everything is entity_category=diagnostic.
func (p *Publisher) genericDiscoveryConfig(v config.Vehicle, field string, val any, dev, origin map[string]any) (string, map[string]any) {
	id := p.pubID(v)
	c := map[string]any{
		"name":            humanize(field),
		"unique_id":       p.uid(v, "raw_"+field),
		"object_id":       id + "_" + field,
		"state_topic":     p.stateTopic(id),
		"device":          dev,
		"origin":          origin,
		"entity_category": "diagnostic",
		"qos":             1,
	}
	if _, ok := val.(bool); ok {
		c["payload_on"] = "true"
		c["payload_off"] = "false"
		c["value_template"] = fmt.Sprintf("{{ 'true' if value_json.%s else 'false' }}", field)
		return "binary_sensor", c
	}
	c["value_template"] = fmt.Sprintf("{{ value_json.%s | default('') }}", field)
	if dc, unit := genericClassUnit(field); dc != "" {
		c["device_class"] = dc
		if unit != "" {
			c["unit_of_measurement"] = unit
		}
		c["state_class"] = "measurement"
	}
	return "sensor", c
}

// publishVehicleDiscovery publishes one vehicle's retained QoS1 discovery configs
// (curated entities + optional command entities) and waits on the paho tokens so a
// failed publish surfaces as an error instead of silently yielding missing HA
// entities. Discovery configs that fail to marshal are skipped (an empty retained
// payload would DELETE the HA entity).
func (p *Publisher) publishVehicleDiscovery(v config.Vehicle) error {
	const pubTimeout = 10 * time.Second
	publish := func(topic string, cfg map[string]any) error {
		payload, err := json.Marshal(cfg)
		if err != nil {
			p.log.Error("marshal discovery config failed — skipping", "topic", topic, "err", err)
			return err
		}
		tok := p.client.Publish(topic, 1, true, payload)
		if !tok.WaitTimeout(pubTimeout) {
			return fmt.Errorf("publish %s timed out", topic)
		}
		if err := tok.Error(); err != nil {
			return fmt.Errorf("publish %s: %w", topic, err)
		}
		return nil
	}

	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	dev := p.deviceInfo(v)
	origin := map[string]any{"name": "community-teslafleet"}
	for _, e := range p.entities {
		cfg := p.discoveryConfig(v, e, dev, origin)
		topic := fmt.Sprintf("%s/%s/%s/%s/config", p.cfg.HA.DiscoveryPrefix, e.Component, p.pubID(v), e.Key)
		note(publish(topic, cfg))
	}
	if p.relay != nil {
		for _, ce := range commands.Entities(p.cfg.Commands) {
			cfg := p.commandDiscoveryConfig(v, ce, dev, origin)
			topic := fmt.Sprintf("%s/%s/%s/cmd_%s/config", p.cfg.HA.DiscoveryPrefix, ce.Component, p.pubID(v), ce.Key)
			note(publish(topic, cfg))
		}
	}
	return firstErr
}

// handleCommand routes an HA command message (topic tgw/<vin>/cmd/<key>/set) to the relay.
func (p *Publisher) handleCommand(_ paho.Client, m paho.Message) {
	if p.relay == nil {
		return
	}
	parts := strings.Split(m.Topic(), "/")
	// <base>/<pubID>/cmd/<key>/set
	if len(parts) != 5 || parts[2] != "cmd" || parts[4] != "set" {
		return
	}
	vin, ok := p.vinForPubID(parts[1])
	if !ok {
		p.log.Warn("command for unknown device id", "id", parts[1])
		return
	}
	key, payload := parts[3], string(m.Payload())
	// Run the relay call off the paho callback: a command can take up to 30s and
	// would otherwise block the MQTT client's incoming-message dispatch.
	go func() {
		p.publishResult(parts[1], p.relay.Handle(vin, key, payload))
	}()
}

// publishResult reports one command's terminal outcome back to the caller.
//
// The topic is not retained. A Result describes a single attempt, and a retained value
// would be redelivered on every reconnect as though it were current.
//
// QoS 0: a result is advisory, and blocking a command handler on a broker round-trip
// to deliver an advisory trades a real cost for a small one.
func (p *Publisher) publishResult(pubID string, res commands.Result) {
	if p.client == nil {
		return
	}
	body, err := json.Marshal(res)
	if err != nil {
		p.log.Error("command result marshal failed", "id", pubID, "err", err)
		return
	}
	topic := fmt.Sprintf("%s/%s/cmd_result", p.cfg.HA.StateTopicBase, pubID)
	if tok := p.client.Publish(topic, 0, false, body); tok.Wait() && tok.Error() != nil {
		p.log.Error("command result publish failed", "topic", topic, "err", tok.Error())
	}
}


func (p *Publisher) commandDiscoveryConfig(v config.Vehicle, ce commands.Entity, dev, origin map[string]any) map[string]any {
	id := p.pubID(v)
	cmdTopic := fmt.Sprintf("%s/%s/cmd/%s/set", p.cfg.HA.StateTopicBase, id, ce.Key)
	c := map[string]any{
		"name":          ce.Name,
		"unique_id":     p.uid(v, "cmd_"+ce.Key),
		"object_id":     id + "_" + ce.Key,
		"command_topic": cmdTopic,
		"device":        dev,
		"origin":        origin,
		"qos":           1,
	}
	state := p.stateTopic(id)
	switch ce.Component {
	case "button":
		c["payload_press"] = "PRESS"
	case "switch":
		c["payload_on"] = "ON"
		c["payload_off"] = "OFF"
		if ce.StateKey != "" {
			c["state_topic"] = state
			c["value_template"] = fmt.Sprintf("{{ 'ON' if value_json.%s else 'OFF' }}", ce.StateKey)
		} else {
			c["optimistic"] = true // write-only: HA assumes the commanded state
		}
	case "lock":
		c["payload_lock"] = "LOCK"
		c["payload_unlock"] = "UNLOCK"
		c["state_locked"] = "LOCKED"
		c["state_unlocked"] = "UNLOCKED"
		c["state_topic"] = state
		c["value_template"] = "{{ 'LOCKED' if value_json.locked else 'UNLOCKED' }}"
	case "number":
		c["min"] = ce.Min
		c["max"] = ce.Max
		c["step"] = ce.Step
		c["mode"] = "slider"
		if ce.Unit != "" {
			c["unit_of_measurement"] = ce.Unit
		}
		if ce.StateKey != "" {
			c["state_topic"] = state
			c["value_template"] = fmt.Sprintf("{{ value_json.%s | default('') }}", ce.StateKey)
		} else {
			c["optimistic"] = true
		}
	case "select":
		c["options"] = ce.Options
		c["optimistic"] = true // command-only: no telemetry state for these
	case "cover":
		c["payload_open"] = "OPEN"
		c["payload_close"] = "CLOSE"
		if ce.DeviceClass != "" {
			c["device_class"] = ce.DeviceClass
		}
		if ce.StateKey != "" {
			c["state_topic"] = state
			c["state_open"] = "open"
			c["state_closed"] = "closed"
			c["value_template"] = fmt.Sprintf("{{ 'open' if value_json.%s else 'closed' }}", ce.StateKey)
		} else {
			c["optimistic"] = true // frunk/trunk: no open-state telemetry
		}
	case "climate":
		// A climate entity uses dedicated mode/temperature topics, not command_topic.
		delete(c, "command_topic")
		base := fmt.Sprintf("%s/%s/cmd", p.cfg.HA.StateTopicBase, id)
		c["modes"] = []string{"off", "heat_cool"}
		c["mode_command_topic"] = base + "/climate_mode/set"
		c["mode_state_topic"] = state
		c["mode_state_template"] = "{{ 'heat_cool' if value_json.climate_on else 'off' }}"
		c["temperature_command_topic"] = base + "/climate_temp/set"
		c["temperature_state_topic"] = state
		c["temperature_state_template"] = "{{ value_json.climate_setpoint | default('') }}"
		c["current_temperature_topic"] = state
		c["current_temperature_template"] = "{{ value_json.climate_current | default('') }}"
		c["temperature_unit"] = "C"
		c["min_temp"] = 15
		c["max_temp"] = 28
		c["temp_step"] = 0.5
	case "update":
		c["device_class"] = "firmware"
		c["state_topic"] = state
		// installed = current Version; latest = available update or current (→ up to date).
		c["value_template"] = "{{ {'installed_version': value_json.version, 'latest_version': value_json.update_available | default(value_json.version, true)} | tojson }}"
		c["payload_install"] = "INSTALL"
	case "text":
		c["optimistic"] = true // the typed value is sent as the command payload
	}
	return c
}

func (p *Publisher) discoveryConfig(v config.Vehicle, e entity, dev, origin map[string]any) map[string]any {
	id := p.pubID(v)
	c := map[string]any{
		"name":        e.Name,
		"unique_id":   p.uid(v, e.Key),
		"object_id":   id + "_" + e.Key,
		"state_topic": p.stateTopic(id),
		"device":      dev,
		"origin":      origin,
		"qos":         1,
	}
	if e.DeviceClass != "" {
		c["device_class"] = e.DeviceClass
	}
	if e.Unit != "" {
		c["unit_of_measurement"] = e.Unit
	}
	if e.StateClass != "" {
		c["state_class"] = e.StateClass
	}
	if e.Category != "" {
		c["entity_category"] = e.Category
	}
	if e.Precision >= 0 {
		c["suggested_display_precision"] = e.Precision
	}
	if len(e.Options) > 0 {
		c["options"] = e.Options
	}

	switch e.Component {
	case "device_tracker":
		// GPS tracker: read a dedicated RETAINED topic carrying {latitude, longitude,
		// gps_accuracy}. No state_topic → HA derives the zone (home/not_home) from the
		// coordinates instead of from the telemetry blob. The retained topic means the
		// last position survives HA/gateway restarts.
		delete(c, "state_topic")
		c["json_attributes_topic"] = p.trackerTopic(id, e.TrackerTopic)
		c["json_attributes_template"] = "{{ value_json | tojson }}"
		c["source_type"] = "gps"
	case "binary_sensor":
		// State publishes Go bools as JSON true/false, but Jinja's default filter
		// renders True/False (capitalized), which never matches payload_on/off.
		// Normalize to the configured payloads: JSON true -> PayloadOn, false ->
		// PayloadOff. (For 'locked', PayloadOn is "false" so the lock device_class
		// inversion is preserved.)
		on, off := e.PayloadOn, e.PayloadOff
		if on == "" {
			on, off = "true", "false"
		}
		c["payload_on"] = on
		c["payload_off"] = off
		if e.ValueTmpl != "" {
			c["value_template"] = e.ValueTmpl
		} else {
			c["value_template"] = fmt.Sprintf("{{ '%s' if value_json.%s else '%s' }}", on, e.Key, off)
		}
	default: // sensor
		c["value_template"] = p.valueTemplate(e)
	}
	return c
}

func (p *Publisher) valueTemplate(e entity) string {
	if e.ValueTmpl != "" {
		return e.ValueTmpl
	}
	// Render nothing (entity unavailable) when the key is absent, so HA shows
	// "unknown" instead of a stale value.
	return fmt.Sprintf("{{ value_json.%s | default('') }}", e.Key)
}
