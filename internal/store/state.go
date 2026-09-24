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
	d := Derived{}
	// A restored Gear D or DetailedChargeState Charging is what the car was
	// doing when the last process stopped. If the car fell asleep during the
	// downtime it never sends the update, so trusting those values would pin it
	// driving or charging, and online, until its next drive.
	if !s.Restored {
		d.Driving, d.Charging = isDriving(s), isCharging(s)
	}

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
	// An explicit DetailedChargeState is the vehicle's own verdict and outranks
	// any power reading. Without this, a disconnected car that is still reporting
	// residual DC power reads as "charging" and TeslaMate opens a phantom charge
	// session that never closes.
	if v, ok := s.Field(FieldChargeState); ok {
		switch ChargeStateString(v.Value) {
		case "Charging":
			return true
		case "Disconnected", "Complete", "NoPower", "Stopped":
			return false
		}
	}
	// DCChargingPower decays to a small residual rather than a clean 0, so it
	// needs the same floor ChargerPower() applies. AC power does reach 0.
	if p, ok := s.Num(FieldDCChargingPower); ok && p >= dcPowerFloor {
		return true
	}
	if p, ok := s.Num(FieldACChargingPower); ok && p > 0 {
		return true
	}
	return false
}

// CableTypeString normalizes a ChargingCableType enum ("CableTypeSAE") to the
// bare form the Fleet API reports for conn_charge_cable ("SAE"). Returns "" when
// the value is absent or not a string.
func CableTypeString(v any) string {
	s := asString(v)
	if s == "" {
		return ""
	}
	return strings.TrimPrefix(s, "CableType")
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

// HvacOn reports whether an HvacPower value means the climate system is running,
// plus whether the value could be classified at all. An unrecognized value returns
// ok=false so a caller can leave the field absent instead of publishing a confident
// "off" — HvacPowerStateUnknown is what a car sends when it does not know either.
//
// Preconditioning counts as on: the compressor is running and the pack is being
// heated or cooled, which is what every consumer of "is climate on" is asking about.
func HvacOn(v any) (bool, bool) {
	if b, ok := ToBool(v); ok {
		return b, true
	}
	switch strings.TrimPrefix(asString(v), "HvacPowerState") {
	case "On", "Precondition", "OverheatProtect":
		return true, true
	case "Off":
		return false, true
	}
	return false, false
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
