package hadiscovery

import (
	"math"
	"strings"
	"unicode"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
	"github.com/LasseLegarth/community-teslafleet/internal/vehicledata"
)

// entity describes one Home Assistant entity exposed via MQTT discovery. All
// entities read from a single shared per-VIN JSON state topic using a
// value_template, which keeps publishes to one message per update.
//
// The catalog (curated entities) is paired with buildState: catalog() declares the
// entity (platform/class/unit/category), buildState() fills the matching state key.
// They share unit resolution via the Units.System so the displayed unit and the
// converted value can never drift.
type entity struct {
	Component   string // sensor | binary_sensor | device_tracker
	Key         string // object_id suffix and value_json key
	Name        string
	DeviceClass string
	Unit        string
	StateClass  string
	Category    string   // "" | "diagnostic"
	Precision   int      // suggested_display_precision; -1 = unset
	Options     []string // enum options (device_class enum)
	PayloadOn    string // binary_sensor
	PayloadOff   string
	ValueTmpl    string // overrides default {{ value_json.<Key> }}
	TrackerTopic string // device_tracker: dedicated retained GPS topic suffix
}

const unset = -1

// quantity kinds drive unit + conversion from the stream's units to the chosen
// display system.
type quantity int

const (
	qNone quantity = iota
	qDistance
	qSpeed
	qTemp
	qPressure
)

func qUnit(q quantity, system string) string {
	switch q {
	case qDistance:
		return vehicledata.DistanceUnit(system)
	case qSpeed:
		return vehicledata.SpeedUnit(system)
	case qTemp:
		return vehicledata.TempUnit(system)
	case qPressure:
		return vehicledata.PressureUnit(system)
	}
	return ""
}

// numItem is a curated numeric field: a store field rendered as one sensor with
// quantity-based unit/conversion (or an explicit Unit when q == qNone).
type numItem struct {
	field       string
	key         string
	name        string
	q           quantity
	deviceClass string
	unit        string // used when q == qNone
	stateClass  string
	category    string
	precision   int
}

// numItems are the curated numeric sensors. Distance/speed convert from the stream
// input units; temp(°C)/pressure(bar) convert only for the imperial system.
var numItems = []numItem{
	{store.FieldBatteryLevel, "battery_level", "Battery", qNone, "battery", "%", "measurement", "", 0},
	{store.FieldSoc, "soc_raw", "Raw SoC", qNone, "battery", "%", "measurement", "diagnostic", 0},
	{store.FieldEnergyRemaining, "energy_remaining", "Energy remaining", qNone, "energy_storage", "kWh", "measurement", "", 1},
	{store.FieldLifetimeEnergyUsed, "lifetime_energy", "Lifetime energy used", qNone, "energy", "kWh", "total_increasing", "diagnostic", 0},
	{store.FieldChargeLimitSoc, "charge_limit", "Charge limit", qNone, "battery", "%", "", "", 0},
	{store.FieldRatedRange, "battery_range", "Range", qDistance, "distance", "", "measurement", "", 0},
	{store.FieldIdealBatteryRange, "ideal_range", "Ideal range", qDistance, "distance", "", "measurement", "diagnostic", 0},
	{store.FieldOdometer, "odometer", "Odometer", qDistance, "distance", "", "total_increasing", "diagnostic", 0},
	{store.FieldChargeRate, "charge_rate", "Charge rate", qSpeed, "speed", "", "measurement", "diagnostic", 0},
	{store.FieldInsideTemp, "inside_temp", "Inside temperature", qTemp, "temperature", "", "measurement", "", 1},
	{store.FieldOutsideTemp, "outside_temp", "Outside temperature", qTemp, "temperature", "", "measurement", "", 1},
	{store.FieldChargerVoltage, "charger_voltage", "Charger voltage", qNone, "voltage", "V", "measurement", "diagnostic", 1},
	{store.FieldChargeAmps, "charger_current", "Charger current", qNone, "current", "A", "measurement", "diagnostic", 0},
	// TimeToFullCharge: raw value (didn't stream in capture); modeled as a duration
	// in hours for now. TODO: verify the stream unit (minutes vs hours) and consider
	// a timestamp (now + remaining), which is how tesla_fleet/tesla_custom model it.
	{store.FieldTimeToFullCharge, "time_to_full", "Time to full charge", qNone, "duration", "h", "measurement", "", -1},
	{store.FieldTpmsFL, "tpms_fl", "TPMS front left", qPressure, "pressure", "", "measurement", "diagnostic", 1},
	{store.FieldTpmsFR, "tpms_fr", "TPMS front right", qPressure, "pressure", "", "measurement", "diagnostic", 1},
	{store.FieldTpmsRL, "tpms_rl", "TPMS rear left", qPressure, "pressure", "", "measurement", "diagnostic", 1},
	{store.FieldTpmsRR, "tpms_rr", "TPMS rear right", qPressure, "pressure", "", "measurement", "diagnostic", 1},
	{store.FieldGpsHeading, "heading", "Heading", qNone, "", "°", "measurement", "diagnostic", 0},
	// Active-route fields (present only while navigating).
	{store.FieldMilesToArrival, "distance_to_arrival", "Distance to arrival", qDistance, "distance", "", "measurement", "", 0},
	{store.FieldMinutesToArrival, "minutes_to_arrival", "Minutes to arrival", qNone, "duration", "min", "measurement", "", 0},
	{store.FieldEnergyAtArrival, "energy_at_arrival", "Energy at arrival", qNone, "battery", "%", "measurement", "", 0},
	{store.FieldRouteTrafficDelay, "traffic_delay", "Traffic delay", qNone, "duration", "min", "measurement", "diagnostic", 0},
}

// boolItem is a curated boolean field rendered as a binary_sensor.
type boolItem struct {
	field       string
	key         string
	name        string
	deviceClass string
	category    string
	invert      bool // device_class lock: on == unlocked
}

var boolItems = []boolItem{
	{store.FieldLocked, "locked", "Locked", "lock", "", true},
	{store.FieldDriverSeatOccupied, "driver_present", "Driver present", "occupancy", "", false},
	{store.FieldLocatedAtHome, "at_home", "At home", "", "", false},
}

// enumItem is a curated string-enum field. norm normalizes the raw stream value to
// a clean option; options are advertised so HA renders an enum sensor.
type enumItem struct {
	field    string
	key      string
	name     string
	category string
	options  []string
	norm     func(any) string
}

var enumItems = []enumItem{
	{store.FieldGear, "shift_state", "Shift state", "", []string{"P", "D", "R", "N"}, store.GearString},
	{store.FieldChargeState, "charging_state", "Charging state", "", nil, func(v any) string { return store.ChargeStateString(v) }},
	{store.FieldChargeStateRaw, "charge_state_raw", "Charge state (raw)", "diagnostic", nil, func(v any) string { return normalizeEnum(asStr(v)) }},
	{store.FieldCenterDisplay, "display_state", "Display state", "diagnostic", nil, func(v any) string { return normalizeEnum(asStr(v)) }},
	{store.FieldSentryMode, "sentry_mode", "Sentry mode", "", nil, func(v any) string { return normalizeEnum(asStr(v)) }},
}

// doorPositions / windowPositions / tpmsPositions drive composite splitting.
var doorPositions = []struct{ sub, key, name string }{
	{"DriverFront", "door_driver_front", "Door driver front"},
	{"DriverRear", "door_driver_rear", "Door driver rear"},
	{"PassengerFront", "door_passenger_front", "Door passenger front"},
	{"PassengerRear", "door_passenger_rear", "Door passenger rear"},
}

var windowPositions = []struct{ field, key, name string }{
	{store.FieldWindowFd, "window_fd", "Window driver front"},
	{store.FieldWindowFp, "window_fp", "Window passenger front"},
	{store.FieldWindowRd, "window_rd", "Window driver rear"},
	{store.FieldWindowRp, "window_rp", "Window passenger rear"},
}

// tpmsWarnPositions maps the warning-dict sub-keys to entity keys/names.
var tpmsWarnPositions = []struct{ sub, suffix, name string }{
	{"frontLeft", "fl", "front left"},
	{"frontRight", "fr", "front right"},
	{"rearLeft", "rl", "rear left"},
	{"rearRight", "rr", "rear right"},
}

// catalog returns the curated entity catalog with units resolved for the chosen
// display system. The long tail of streamed fields is published dynamically as
// diagnostic entities by the publisher (see discoverNewFields).
func catalog(units config.Units) []entity {
	sys := units.System
	var es []entity

	// Derived top-level state.
	es = append(es,
		entity{Component: "sensor", Key: "state", Name: "State", Precision: unset},
		entity{Component: "binary_sensor", Key: "online", Name: "Online", DeviceClass: "connectivity", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		entity{Component: "binary_sensor", Key: "charging", Name: "Charging", DeviceClass: "battery_charging", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		entity{Component: "binary_sensor", Key: "plugged_in", Name: "Plugged in", DeviceClass: "plug", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		entity{Component: "binary_sensor", Key: "doors", Name: "Doors", DeviceClass: "door", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		entity{Component: "binary_sensor", Key: "sentry", Name: "Sentry", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		entity{Component: "sensor", Key: "charger_power", Name: "Charger power", DeviceClass: "power", Unit: "kW", StateClass: "measurement", Precision: 1},
		entity{Component: "sensor", Key: "power", Name: "Power", DeviceClass: "power", Unit: "kW", StateClass: "measurement", Precision: 1},
		entity{Component: "sensor", Key: "version", Name: "Software version", Category: "diagnostic", Precision: unset},
		entity{Component: "sensor", Key: "destination_name", Name: "Destination name", Precision: unset},
	)
	// Speed: own entity (only set while driving, else 0).
	es = append(es, entity{Component: "sensor", Key: "speed", Name: "Speed", DeviceClass: "speed", Unit: vehicledata.SpeedUnit(sys), StateClass: "measurement", Precision: 0})

	// Curated numerics.
	for _, n := range numItems {
		unit := n.unit
		if n.q != qNone {
			unit = qUnit(n.q, sys)
		}
		es = append(es, entity{
			Component: "sensor", Key: n.key, Name: n.name, DeviceClass: n.deviceClass,
			Unit: unit, StateClass: n.stateClass, Category: n.category, Precision: n.precision,
		})
	}
	// Curated bools.
	for _, b := range boolItems {
		on, off := "true", "false"
		if b.invert {
			on, off = "false", "true"
		}
		es = append(es, entity{
			Component: "binary_sensor", Key: b.key, Name: b.name, DeviceClass: b.deviceClass,
			Category: b.category, PayloadOn: on, PayloadOff: off, Precision: unset,
		})
	}
	// Curated enums. device_class=enum only with an explicit options list (HA
	// rejects an enum sensor without options); otherwise a plain string sensor.
	for _, e := range enumItems {
		dc := ""
		if len(e.options) > 0 {
			dc = "enum"
		}
		es = append(es, entity{
			Component: "sensor", Key: e.key, Name: e.name, DeviceClass: dc,
			Category: e.category, Options: e.options, Precision: unset,
		})
	}
	// Composite splits: doors, windows, tpms warnings (all diagnostic).
	for _, d := range doorPositions {
		es = append(es, entity{Component: "binary_sensor", Key: d.key, Name: d.name, DeviceClass: "door", Category: "diagnostic", PayloadOn: "true", PayloadOff: "false", Precision: unset})
	}
	for _, w := range windowPositions {
		es = append(es, entity{Component: "binary_sensor", Key: w.key, Name: w.name, DeviceClass: "window", Category: "diagnostic", PayloadOn: "true", PayloadOff: "false", Precision: unset})
	}
	for _, p := range tpmsWarnPositions {
		es = append(es,
			entity{Component: "binary_sensor", Key: "tpms_hard_" + p.suffix, Name: "Tire hard warning " + p.name, DeviceClass: "problem", Category: "diagnostic", PayloadOn: "true", PayloadOff: "false", Precision: unset},
			entity{Component: "binary_sensor", Key: "tpms_soft_" + p.suffix, Name: "Tire soft warning " + p.name, DeviceClass: "problem", Category: "diagnostic", PayloadOn: "true", PayloadOff: "false", Precision: unset},
		)
	}
	// Device trackers. Each reads a dedicated retained GPS topic (published by
	// publishTrackers) via json_attributes + source_type gps, so HA computes the
	// zone (home/not_home) from coordinates rather than from the telemetry blob.
	es = append(es,
		entity{Component: "device_tracker", Key: "location", Name: "Location", TrackerTopic: "location", Precision: unset},
		entity{Component: "device_tracker", Key: "destination", Name: "Destination", TrackerTopic: "destination", Precision: unset},
		entity{Component: "device_tracker", Key: "origin", Name: "Origin", Category: "diagnostic", TrackerTopic: "origin", Precision: unset},
	)
	return es
}

// buildState produces the JSON payload published to the shared state topic. Keys
// must match catalog entity keys plus device_tracker attribute fields.
func buildState(snap store.Snapshot, d store.Derived, units config.Units) map[string]any {
	sys := units.System
	s := map[string]any{
		"state":    d.State,
		"online":   d.State != "offline",
		"charging": d.Charging,
	}

	// Curated numerics with quantity conversion.
	for _, n := range numItems {
		v, ok := snap.Num(n.field)
		if !ok {
			continue
		}
		switch n.q {
		case qDistance:
			input := units.RangeInput
			if n.field == store.FieldOdometer {
				input = units.OdometerInput
			}
			s[n.key] = roundP(vehicledata.DistanceForDisplay(v, input, sys), n.precision)
		case qSpeed:
			s[n.key] = roundP(vehicledata.SpeedForDisplay(v, units.SpeedInput, sys), n.precision)
		case qTemp:
			s[n.key] = roundP(vehicledata.TempForDisplay(v, sys), n.precision)
		case qPressure:
			s[n.key] = roundP(vehicledata.PressureForDisplay(v, sys), n.precision)
		default:
			if n.precision >= 0 {
				s[n.key] = roundP(v, n.precision)
			} else {
				s[n.key] = v
			}
		}
	}

	// ChargerVoltage decays to a small non-zero residual after a session ends
	// (Tesla never streams a clean 0), so show 0 unless actually charging.
	if _, ok := s["charger_voltage"]; ok && !d.Charging {
		s["charger_voltage"] = 0
	}

	// Speed: 0 when parked so HA shows a clean zero rather than a stale value.
	if d.Driving {
		if v, ok := snap.Num(store.FieldVehicleSpeed); ok {
			s["speed"] = roundP(vehicledata.SpeedForDisplay(v, units.SpeedInput, sys), 0)
		}
	} else {
		s["speed"] = 0
	}

	// Charger power: max(AC, DC).
	if p, ok := snap.ChargerPower(); ok {
		s["charger_power"] = roundP(p, 1)
	}
	// Drive/regen power, derived from battery pack: V × A → kW (no direct signal).
	// PackCurrent decays to a small residual when idle (never a clean 0), so only
	// report pack power while driving or charging; otherwise it is 0.
	if d.Driving || d.Charging {
		if v, ok := snap.Num(store.FieldPackVoltage); ok {
			if a, ok2 := snap.Num(store.FieldPackCurrent); ok2 {
				s["power"] = roundP(v*a/1000, 1)
			}
		}
	} else {
		s["power"] = 0
	}

	// Curated bools.
	for _, b := range boolItems {
		if v, ok := snap.Bool(b.field); ok {
			s[b.key] = v
		}
	}
	// Curated enums.
	for _, e := range enumItems {
		if fv, ok := snap.Field(e.field); ok {
			if str := e.norm(fv.Value); str != "" {
				s[e.key] = str
			}
		}
	}

	// Climate on/off + sentry bool (used by the climate entity + sentry switch).
	if fv, ok := snap.Field(store.FieldIsClimateOn); ok {
		s["climate_on"] = hvacOn(fv.Value)
	}
	if fv, ok := snap.Field(store.FieldSentryMode); ok {
		s["sentry"] = store.SentryEnabled(fv.Value)
	}
	// Climate entity current + target, in raw °C (the climate entity declares unit C).
	if v, ok := snap.Num(store.FieldInsideTemp); ok {
		s["climate_current"] = roundP(v, 1)
	}
	if v, ok := snap.Num(store.FieldHvacLeftTempReq); ok {
		s["climate_setpoint"] = roundP(v, 1)
	}
	// Charge-port open state (drives the charge_port cover).
	if b, ok := snap.Bool(store.FieldChargePortDoorOpen); ok {
		s["charge_port"] = b
	}
	// Plugged in: cable present or charge-port latch engaged.
	s["plugged_in"] = pluggedIn(snap)

	// Doors composite + aggregate.
	if dm := snap.BoolMap(store.FieldDoorState); dm != nil {
		anyOpen := false
		for _, dp := range doorPositions {
			if open, ok := dm[dp.sub]; ok {
				s[dp.key] = open
				anyOpen = anyOpen || open
			}
		}
		s["doors"] = anyOpen
	}
	// Windows composite + aggregate (enum "WindowStateClosed" → open=false).
	{
		anyOpen := false
		haveWindow := false
		for _, w := range windowPositions {
			if str := snap.Str(w.field); str != "" {
				haveWindow = true
				open := !strings.Contains(strings.ToLower(str), "closed")
				s[w.key] = open
				anyOpen = anyOpen || open
			}
		}
		if haveWindow {
			s["windows"] = anyOpen
		}
	}
	// TPMS warnings composite.
	for field, prefix := range map[string]string{
		store.FieldTpmsHardWarnings: "tpms_hard_",
		store.FieldTpmsSoftWarnings: "tpms_soft_",
	} {
		if wm := snap.BoolMap(field); wm != nil {
			for _, p := range tpmsWarnPositions {
				if warn, ok := wm[p.sub]; ok {
					s[prefix+p.suffix] = warn
				}
			}
		}
	}

	// Version + available software update (for the HA 'update' entity).
	if v, ok := snap.Field(store.FieldVersion); ok {
		if str, ok := v.Value.(string); ok {
			s["version"] = str
		}
	}
	if av := strings.TrimSpace(snap.Str("SoftwareUpdateVersion")); av != "" {
		s["update_available"] = av
	}
	// Destination name (only while navigating).
	if dn := strings.TrimSpace(snap.Str(store.FieldDestinationName)); dn != "" {
		s["destination_name"] = dn
	}

	// Device-tracker GPS attributes.
	if lat, lng, ok := snap.Location(); ok {
		s["latitude"] = lat
		s["longitude"] = lng
		s["gps_accuracy"] = 5
	}
	if lat, lng, ok := snap.LocationField(store.FieldDestinationLocation); ok {
		s["destination_latitude"] = lat
		s["destination_longitude"] = lng
	}
	if lat, lng, ok := snap.LocationField(store.FieldOriginLocation); ok {
		s["origin_latitude"] = lat
		s["origin_longitude"] = lng
	}

	// Generic pass: expose every remaining streamed field under its Tesla field
	// name so dynamic per-field discovery (diagnostic) can render it. Skip fields
	// already covered above and the dict fields whose sub-values are exposed.
	covered := coveredFields()
	for name, fv := range snap.Fields {
		if covered[name] {
			continue
		}
		val := genericValue(fv.Value)
		// HA rejects a sensor state longer than 255 chars ("exceeds maximum
		// length (255)") — the entity then spams the log and falls back to
		// unknown every publish. Drop any over-long string generically rather
		// than let an unforeseen field churn.
		if str, ok := val.(string); ok && len(str) > maxHAStateLen {
			continue
		}
		s[name] = val
	}
	return s
}

// hvacOn maps an HvacPower enum ("HvacPowerStateOn"/"...Off") or bool to on/off.
//
// Delegates to store.HvacOn so the HA entity and the emulated vehicle_data's
// climate_state.is_climate_on can never disagree about the same enum. An
// unclassifiable value reads as off here because a binary_sensor has no third
// state; the vehicle_data mapper omits the key instead.
func hvacOn(v any) bool {
	on, _ := store.HvacOn(v)
	return on
}

// pluggedIn derives whether a charge cable is connected.
//
// ChargePortLatch is Tesla's authoritative — and only reliable — plug signal:
// it is discrete and streams both ways (Engaged on plug-in, Disengaged on
// unplug), so we use nothing else. Every alternative is fragile and has bitten
// us: Tesla never resets ChargingCableType to CableTypeNone (so OR'ing it pins
// plugged_in on forever after the first charge), and a Gear/VehicleSpeed
// "driving" backstop reads fields that stop at a non-zero residual and never
// reach 0 — which stuck plugged_in *off* every evening after a drive. If the
// latch has never been reported we report not-plugged rather than guess.
func pluggedIn(snap store.Snapshot) bool {
	return strings.Contains(snap.Str(store.FieldChargePortLatch), "Engaged")
}

// genericValue normalizes a raw field value for the generic state pass: enum
// strings get their Tesla type prefix stripped; everything else passes through.
func genericValue(v any) any {
	if s, ok := v.(string); ok {
		return normalizeEnum(s)
	}
	return v
}

// maxHAStateLen is Home Assistant's hard limit on a sensor state string; a
// longer value is rejected with "exceeds maximum length (255)".
const maxHAStateLen = 255

// coveredFields lists Tesla telemetry field names represented by a curated,
// composite or derived entity (or deliberately excluded, e.g. RouteLine), so
// generic per-field discovery skips them.
func coveredFields() map[string]bool {
	m := map[string]bool{
		store.FieldVehicleSpeed:      true, // speed
		store.FieldACChargingPower:   true, // charger_power
		store.FieldDCChargingPower:   true, // charger_power
		store.FieldIsClimateOn:       true, // climate_on
		store.FieldSentryMode:        true, // sentry / sentry_mode
		store.FieldChargePortDoorOpen: true, // charge_port cover
		store.FieldChargePortLatch:    true, // plugged_in
		store.FieldChargingCableType:  true, // plugged_in
		store.FieldHvacLeftTempReq:    true, // climate setpoint
		store.FieldDoorState:         true, // doors composite
		store.FieldTpmsHardWarnings:  true, // tpms warnings composite
		store.FieldTpmsSoftWarnings:  true, // tpms warnings composite
		store.FieldVersion:           true, // version + device sw_version
		store.FieldDestinationName:   true, // destination_name
		store.FieldLocation:          true, // device_tracker
		store.FieldDestinationLocation: true, // device_tracker
		store.FieldOriginLocation:    true, // device_tracker
		"RouteLine":                  true, // encoded nav polyline — not a useful sensor, and >255 chars while navigating (exceeds HA's state length limit)
	}
	for _, n := range numItems {
		m[n.field] = true
	}
	for _, b := range boolItems {
		m[b.field] = true
	}
	for _, e := range enumItems {
		m[e.field] = true
	}
	for _, w := range windowPositions {
		m[w.field] = true
	}
	return m
}

func roundP(v float64, p int) float64 {
	if p < 0 {
		return v
	}
	m := math.Pow(10, float64(p))
	return math.Round(v*m) / m
}

// modelFromVIN decodes the model from the 4th VIN character (Tesla convention).
func modelFromVIN(vin string) string {
	if len(vin) < 4 {
		return "Tesla"
	}
	switch vin[3] {
	case 'S':
		return "Model S"
	case '3':
		return "Model 3"
	case 'X':
		return "Model X"
	case 'Y':
		return "Model Y"
	}
	return "Tesla"
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// normalizeEnum strips a Tesla type prefix from an enum string value. The values
// follow "<Type>State<Value>" (e.g. "DisplayStateOff" → "Off", "ShiftStateP" → "P",
// "BMSStateDrive" → "Drive"). Free text (containing spaces) is left untouched so
// media titles etc. survive.
func normalizeEnum(s string) string {
	if s == "" || strings.ContainsRune(s, ' ') {
		return s
	}
	if i := strings.LastIndex(s, "State"); i >= 0 && i+5 < len(s) {
		return s[i+5:]
	}
	return s
}

// humanize converts a Tesla CamelCase telemetry field name into spaced words,
// e.g. "DiStatorTempF" -> "Di Stator Temp F", "ACChargingPower" -> "AC Charging Power".
func humanize(name string) string {
	if name == "" {
		return ""
	}
	r := []rune(name)
	var b strings.Builder
	for i, c := range r {
		if i > 0 {
			prev := r[i-1]
			next := rune(0)
			if i+1 < len(r) {
				next = r[i+1]
			}
			boundary := false
			switch {
			case c == '_':
				if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
					b.WriteRune(' ')
				}
				continue
			case prev == '_':
			case unicode.IsUpper(c) && unicode.IsLower(prev):
				boundary = true
			case unicode.IsUpper(c) && unicode.IsUpper(prev) && unicode.IsLower(next):
				boundary = true
			case unicode.IsDigit(c) && !unicode.IsDigit(prev):
				boundary = true
			case unicode.IsLetter(c) && unicode.IsDigit(prev):
				boundary = true
			}
			if boundary {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

// genericClassUnit applies best-effort device_class + unit heuristics by field name
// for the diagnostic long tail. The display system only affects the few converted
// quantities, which are all curated, so generic distance/speed stay unit-less here.
func genericClassUnit(name string) (deviceClass, unit string) {
	switch {
	case strings.Contains(name, "Temp"):
		return "temperature", "°C"
	case strings.Contains(name, "Voltage"), strings.HasPrefix(name, "DiVBat"):
		return "voltage", "V"
	case strings.Contains(name, "Current"):
		return "current", "A"
	case strings.Contains(name, "Power"):
		return "power", "kW"
	case strings.Contains(name, "Pressure"):
		return "pressure", "bar"
	}
	return "", ""
}
