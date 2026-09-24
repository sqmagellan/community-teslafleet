// Package enroll turns a simple profile (eco/balanced/live) into a Tesla
// fleet_telemetry_config: which signals, how often, and a rough cost estimate. The
// canonical enrollable signal set is embedded from fields.txt.
package enroll

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed fields.txt
var fieldsRaw string

// Fields is the canonical list of enrollable telemetry signals.
var Fields = func() []string {
	var out []string
	for _, l := range strings.Split(fieldsRaw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}()

// Profiles a user can pick.
var Profiles = []string{"eco", "balanced", "live", "off"}

// tier is a stream cadence. interval=0 means "not enrolled".
type tier struct {
	interval int // seconds; how often (max) a changed value is sent
	delta    int // minimum_delta (numeric); 0 = unset
	resend   int // resend_interval_seconds; 0 = unset
}

var tiers = map[string]tier{
	"realtime":   {1, 0, 0},
	"responsive": {15, 0, 240},
	"normal":     {60, 0, 0},
	"slow":       {300, 0, 600},
	"off":        {0, 0, 0},
}

// category buckets a field by name. Coarse but stable.
func category(f string) string {
	switch {
	case f == "Location" || f == "VehicleSpeed" || f == "Gear" || f == "GpsHeading" || f == "GpsState":
		return "drive"
	case strings.HasPrefix(f, "MilesToArrival") || strings.HasPrefix(f, "MinutesToArrival") ||
		strings.HasPrefix(f, "Destination") || strings.HasPrefix(f, "Origin") || strings.HasPrefix(f, "Route") ||
		strings.Contains(f, "Arrival") || strings.HasPrefix(f, "LocatedAt"):
		return "nav"
	case strings.HasPrefix(f, "Di") || strings.HasPrefix(f, "Pack") || strings.HasPrefix(f, "Brick") ||
		strings.HasPrefix(f, "Module") || strings.HasPrefix(f, "Num") || strings.Contains(f, "Isolation") ||
		strings.Contains(f, "Acceleration") || strings.Contains(f, "Pedal") || f == "BMSState" || f == "Hvil" ||
		f == "DriveRail" || f == "DCDCEnable":
		return "diagnostic" // motor/pack/brick/module internals — checked before the "Temp" climate rule
	case strings.Contains(f, "Charg") || f == "Soc" || f == "BatteryLevel" || strings.Contains(f, "Range") ||
		f == "EnergyRemaining" || f == "DetailedChargeState" || f == "ChargeState":
		return "energy"
	case strings.HasPrefix(f, "Hvac") || strings.Contains(f, "Temp") || strings.HasPrefix(f, "Seat") ||
		strings.HasPrefix(f, "Cabin") || strings.HasPrefix(f, "Defrost") || strings.HasPrefix(f, "Climate") ||
		strings.Contains(f, "Preconditioning"):
		return "climate"
	case f == "DoorState" || strings.HasSuffix(f, "Window") || f == "Locked" || strings.HasPrefix(f, "ChargePort") ||
		strings.Contains(f, "Trunk"):
		return "closure"
	case strings.HasPrefix(f, "Tpms"):
		return "tpms"
	case strings.HasPrefix(f, "Media"):
		return "media"
	case strings.HasPrefix(f, "Setting") || f == "Version" || strings.HasPrefix(f, "SoftwareUpdate") ||
		f == "WheelType" || f == "EfficiencyPackage":
		return "settings"
	default:
		return "diagnostic"
	}
}

// profileTier maps (profile, category) → tier name.
func profileTier(profile, cat string) string {
	m := map[string]map[string]string{
		"eco": {
			"drive": "responsive", "nav": "slow", "energy": "normal", "climate": "slow",
			"closure": "slow", "tpms": "slow", "media": "off", "settings": "slow", "diagnostic": "off",
		},
		"balanced": {
			"drive": "realtime", "nav": "normal", "energy": "responsive", "climate": "normal",
			"closure": "normal", "tpms": "slow", "media": "normal", "settings": "slow", "diagnostic": "slow",
		},
		"live": {
			"drive": "realtime", "nav": "responsive", "energy": "responsive", "climate": "responsive",
			"closure": "normal", "tpms": "normal", "media": "responsive", "settings": "slow", "diagnostic": "normal",
		},
	}
	if p, ok := m[profile]; ok {
		if t, ok := p[cat]; ok {
			return t
		}
	}
	return "off"
}

// FTC is a fleet_telemetry_config. Wrap it with Wrap before posting it.
type FTC struct {
	Hostname    string                    `json:"hostname"`
	CA          string                    `json:"ca,omitempty"`
	Port        int                       `json:"port"`
	PreferTyped bool                      `json:"prefer_typed"`
	Fields      map[string]map[string]int `json:"fields"`
}

// Generate builds a fleet_telemetry_config for a profile + endpoint.
func Generate(profile, hostname string, port int) FTC {
	if port == 0 {
		port = 443
	}
	out := FTC{Hostname: hostname, Port: port, PreferTyped: true, Fields: map[string]map[string]int{}}
	for _, f := range Fields {
		t := tiers[profileTier(profile, category(f))]
		if t.interval == 0 {
			continue // off
		}
		fc := map[string]int{"interval_seconds": t.interval}
		if t.delta > 0 {
			fc["minimum_delta"] = t.delta
		}
		if t.resend > 0 {
			fc["resend_interval_seconds"] = t.resend
		}
		out.Fields[f] = fc
	}
	return out
}

// Estimate is a rough monthly signal volume + cost for an FTC. Streaming is event/delta
// gated, so this is indicative only: it assumes ~2h/day active (emitting at interval) and
// idle resends the rest of the time. Wakes and commands are billed separately.
type Estimate struct {
	EnrolledFields int
	SignalsLow     int     // /month
	SignalsHigh    int     // /month
	USDLow         float64 // after-credit cost is typically $0; these are raw signal cost
	USDHigh        float64
}

const usdPer = 1.0 / 150000.0

func EstimateCost(f FTC) Estimate {
	const activeSec = 2 * 3600
	const idleSec = 22 * 3600
	var low, high int
	for _, fc := range f.Fields {
		iv := fc["interval_seconds"]
		if iv <= 0 {
			continue
		}
		// High: emits every interval while active.
		high += activeSec / iv
		// Low: mostly idle; only resend (if any) ticks.
		if rs := fc["resend_interval_seconds"]; rs > 0 {
			low += idleSec / rs
			high += idleSec / rs
		}
		low += activeSec / iv / 4 // assume ~25% of active fields actually change
	}
	low *= 30
	high *= 30
	return Estimate{
		EnrolledFields: len(f.Fields),
		SignalsLow:     low, SignalsHigh: high,
		USDLow: float64(low) * usdPer, USDHigh: float64(high) * usdPer,
	}
}
