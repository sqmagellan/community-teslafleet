package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
)

// Derived is the high-level interpretation of a snapshot.
type Derived struct {
	State    string // online | asleep | offline
	Driving  bool
	Charging bool
}

// Derive computes the Fleet-API top-level state plus driving/charging flags.
// With Sentry on, the car stays online forever — that is fine, polling the
// emulator is free. report_asleep_when_idle lets TeslaMate still log sleep.
func Derive(s Snapshot, cfg config.State, now time.Time) Derived {
	d := Derived{Driving: isDriving(s), Charging: isCharging(s)}

	grace := time.Duration(cfg.OnlineGraceSeconds) * time.Second
	stale := time.Duration(cfg.StaleAfterSeconds) * time.Second
	freshV := !s.LastV.IsZero() && now.Sub(s.LastV) < grace
	staleV := s.LastV.IsZero() || now.Sub(s.LastV) > stale

	switch {
	case d.Driving || d.Charging:
		d.State = "online"
	case cfg.ReportAsleepWhenIdle && staleV && s.Connectivity != "offline":
		// Idle for a long time: report asleep so TeslaMate logs a sleep, even
		// though Sentry keeps the modem connected.
		d.State = "asleep"
	case s.Connectivity == "online" || freshV:
		d.State = "online"
	case s.Connectivity == "offline" && staleV:
		d.State = "offline"
	default:
		d.State = "asleep"
	}
	return d
}

func isDriving(s Snapshot) bool {
	// Gear only. VehicleSpeed is deliberately NOT used: Tesla stops streaming it
	// at a small non-zero residual when parking (e.g. 0.6 mph) and never sends a
	// clean 0, so a speed>0 test stays true forever after a drive — which pinned
	// the car "driving/online" and the speed sensor non-zero while parked. Gear
	// reliably streams P on park.
	if g, ok := s.Field(FieldGear); ok {
		switch GearString(g.Value) {
		case "D", "R", "N":
			return true
		}
	}
	return false
}

func isCharging(s Snapshot) bool {
	for _, f := range []string{FieldACChargingPower, FieldDCChargingPower} {
		if v, ok := s.Field(f); ok {
			if pw, ok := ToFloat(v.Value); ok && pw > 0 {
				return true
			}
		}
	}
	if v, ok := s.Field(FieldChargeState); ok {
		if strings.EqualFold(ChargeStateString(v.Value), "Charging") {
			return true
		}
	}
	return false
}

// ChargeStateString normalizes a DetailedChargeState enum ("DetailedChargeStateDisconnected")
// to the plain value TeslaMate expects ("Disconnected"/"Charging"/"Stopped"/"Complete").
func ChargeStateString(v any) string {
	return strings.TrimPrefix(asString(v), "DetailedChargeState")
}

// SentryEnabled maps a SentryMode enum ("SentryModeStateIdle"/"...Armed"/"...Off")
// to a bool: armed/aware/panic = on; off/idle = off.
func SentryEnabled(v any) bool {
	switch strings.TrimPrefix(asString(v), "SentryModeState") {
	case "", "Off", "Idle":
		return false
	}
	return true
}

// GearString normalizes a decoded Gear value to "P"/"D"/"R"/"N" or "".
// Handles plain ("D"), prefixed ("ShiftStateD") and numeric encodings.
func GearString(v any) string {
	s := strings.ToUpper(strings.TrimSpace(asString(v)))
	if s == "" {
		return ""
	}
	switch s {
	case "P", "D", "R", "N":
		return s
	}
	// e.g. SHIFTSTATED / DRIVESTATER -> take trailing P/D/R/N
	last := s[len(s)-1:]
	switch last {
	case "P", "D", "R", "N":
		return last
	}
	// numeric enum fallback (Tesla ShiftState: 0/Invalid,1 P,2 R,3 N,4 D ... varies)
	switch s {
	case "1":
		return "P"
	case "2":
		return "R"
	case "3":
		return "N"
	case "4":
		return "D"
	}
	return ""
}

// ToFloat extracts a float64 from a decoded JSON value (float64, string, int, bool→0/1).
func ToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// ToBool extracts a bool from a decoded value.
func ToBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))
		if err != nil {
			return false, false
		}
		return b, true
	case float64:
		return x != 0, true
	}
	return false, false
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}
