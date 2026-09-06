// Package vehicledata assembles a Tesla Fleet API vehicle_data "response" object
// from live telemetry overlaid on a captured template.
package vehicledata

import (
	"math"
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
	"github.com/LasseLegarth/community-teslafleet/internal/vin"
)

// Build produces the vehicle_data "response" object for a vehicle. It clones the
// template, overlays live telemetry values (unit-converted), and sets top-level
// identity + state. All sub-objects are always present so nothing is ever null.
func Build(snap store.Snapshot, d store.Derived, veh config.Vehicle, tmpl *Template, units config.Units, now time.Time) map[string]any {
	resp := tmpl.Clone()

	// Top-level identity + state.
	resp["id"] = veh.ID
	resp["id_s"] = veh.IDString()
	resp["user_id"] = veh.ID
	resp["vehicle_id"] = veh.VehicleID
	resp["vin"] = veh.VIN
	resp["display_name"] = veh.DisplayName
	resp["state"] = d.State

	ds := subObj(resp, "drive_state")
	cs := subObj(resp, "charge_state")
	cls := subObj(resp, "climate_state")
	vs := subObj(resp, "vehicle_state")
	vc := subObj(resp, "vehicle_config")

	// Section timestamps are the OBSERVATION time, not the time this document
	// was rendered. TeslaMate stores a streamed position's date verbatim
	// (vehicle.ex create_position/2) and compares drive_state.timestamp across
	// both paths, so stamping "now" on frozen values told it the car had a new
	// fix at this instant at the old coordinates. Falling back to now only when
	// nothing has ever been streamed keeps a first boot sane.
	//
	// Repeats are safe on both of TeslaMate's guards: a fetch is discarded only
	// when strictly older (`now < last`) and stream data only when the stored
	// timestamp is strictly greater, so an unchanged observation is accepted.
	obs := now
	if !snap.LastV.IsZero() {
		obs = snap.LastV
	}
	tsSec := obs.Unix()
	tsMs := obs.UnixMilli()
	ds["timestamp"], cs["timestamp"], cls["timestamp"], vs["timestamp"] = tsMs, tsMs, tsMs, tsMs

	// ---- drive_state ----
	if loc, ok := snap.Field(store.FieldLocation); ok {
		if lat, lng, ok := snap.Location(); ok {
			ds["latitude"], ds["longitude"] = lat, lng
			ds["native_latitude"], ds["native_longitude"] = lat, lng
			ds["gps_as_of"] = loc.UpdatedAt.Unix()
		}
	} else {
		ds["gps_as_of"] = tsSec
	}
	if h, ok := snap.Num(store.FieldGpsHeading); ok {
		ds["heading"] = int(h)
	}
	// shift_state + speed: only when actually driving (Tesla reports null parked).
	if g, ok := snap.Field(store.FieldGear); ok {
		if gs := store.GearString(g.Value); gs != "" {
			ds["shift_state"] = gs
		} else {
			ds["shift_state"] = nil
		}
	}
	if d.Driving {
		if sp, ok := snap.Num(store.FieldVehicleSpeed); ok {
			ds["speed"] = int(round1(SpeedToMph(sp, units.SpeedInput)))
		}
	} else {
		ds["speed"] = nil
		ds["shift_state"] = nil
	}
	ds["power"] = drivePower(snap, d)

	// ---- charge_state ----
	// battery_level and usable_battery_level are DIFFERENT numbers on a real car:
	// the usable figure sits below the displayed one when the pack is cold, and
	// TeslaMate uses the gap to show the blue snowflake (teslamate#321). Publishing
	// one telemetry field as both made that gap structurally always zero.
	//
	// Which telemetry field is which, and the rounding, are both settled against
	// real Fleet API documents fetched for two cars while they were awake:
	//
	//   telemetry Soc=59.221 BatteryLevel=59.553 -> API battery_level=60 usable=59
	//   telemetry Soc=69.541 BatteryLevel=69.851 -> API battery_level=70 usable=70
	//
	// So BatteryLevel is the displayed SOC, Soc is the usable one, and the API
	// ROUNDS rather than truncates. Truncating (the obvious int() conversion) makes
	// battery_level read one percent low most of the time and hides the usable gap
	// entirely -- in the first case above it would publish 59/59 where the car
	// reports 60/59.
	//
	// min() is kept as a cheap invariant: usable must never exceed displayed, which
	// is the one relation any consumer actually depends on.
	soc, hasSoc := snap.Num(store.FieldSoc)
	lvl, hasLvl := snap.Num(store.FieldBatteryLevel)
	switch {
	case hasSoc && hasLvl:
		cs["battery_level"] = pct(lvl)
		cs["usable_battery_level"] = pct(min(soc, lvl))
	case hasLvl:
		cs["battery_level"], cs["usable_battery_level"] = pct(lvl), pct(lvl)
	case hasSoc:
		cs["battery_level"], cs["usable_battery_level"] = pct(soc), pct(soc)
	}
	if r, ok := snap.Num(store.FieldRatedRange); ok {
		mi := round1(RangeToMiles(r, units.RangeInput))
		cs["battery_range"] = mi
		cs["ideal_battery_range"] = mi
	}
	if r, ok := snap.Num(store.FieldEstBatteryRange); ok {
		cs["est_battery_range"] = round1(RangeToMiles(r, units.RangeInput))
	}
	if p, ok := snap.ChargerPower(); ok {
		cs["charger_power"] = int(p)
	}
	if lim, ok := snap.Num(store.FieldChargeLimitSoc); ok {
		cs["charge_limit_soc"] = int(lim)
	}
	if e, ok := snap.ChargeEnergyAdded(); ok {
		cs["charge_energy_added"] = round1(e)
	}
	if t, ok := snap.Num(store.FieldTimeToFullCharge); ok {
		cs["time_to_full_charge"] = round1(t)
	}
	if v, ok := snap.Num(store.FieldChargerVoltage); ok {
		cs["charger_voltage"] = int(v)
	}
	if a, ok := snap.Num(store.FieldChargeAmps); ok {
		cs["charger_actual_current"] = int(a)
		cs["charge_current_request"] = int(a)
	}
	if b, ok := snap.Bool(store.FieldChargePortDoorOpen); ok {
		cs["charge_port_door_open"] = b
	}
	if b, ok := snap.FastChargerPresent(); ok {
		cs["fast_charger_present"] = b
	}
	if v, ok := snap.Field(store.FieldChargingCableType); ok {
		if c := store.CableTypeString(v.Value); c != "" {
			cs["conn_charge_cable"] = c
		}
	}
	cs["charging_state"] = chargingState(snap, d)

	// ---- climate_state ----
	if t, ok := snap.Num(store.FieldInsideTemp); ok {
		cls["inside_temp"] = round1(t)
	}
	if t, ok := snap.Num(store.FieldOutsideTemp); ok {
		cls["outside_temp"] = round1(t)
	}
	// is_climate_on was absent from the emulated document entirely, because the
	// captured template ships an empty climate_state and nothing overlaid it — so
	// every consumer read "no climate data" while HvacPower was streaming the whole
	// time. Omitted rather than defaulted when the enum cannot be classified.
	if v, ok := snap.Field(store.FieldIsClimateOn); ok {
		if on, ok := store.HvacOn(v.Value); ok {
			cls["is_climate_on"] = on
		}
	}

	// ---- vehicle_state ----
	if o, ok := snap.Num(store.FieldOdometer); ok {
		vs["odometer"] = round1(RangeToMiles(o, units.OdometerInput))
	}
	if b, ok := snap.Bool(store.FieldLocked); ok {
		vs["locked"] = b
	}
	if v, ok := snap.Field(store.FieldSentryMode); ok {
		vs["sentry_mode"] = store.SentryEnabled(v.Value)
	}
	if v, ok := snap.Field(store.FieldVersion); ok {
		if s := asStr(v.Value); s != "" {
			vs["car_version"] = s
		}
	}
	for field, key := range map[string]string{
		store.FieldTpmsFL: "tpms_pressure_fl",
		store.FieldTpmsFR: "tpms_pressure_fr",
		store.FieldTpmsRL: "tpms_pressure_rl",
		store.FieldTpmsRR: "tpms_pressure_rr",
	} {
		if p, ok := snap.Num(field); ok {
			vs[key] = round1(p)
		}
	}
	vs["vehicle_name"] = veh.DisplayName

	// ---- vehicle_config ----
	// car_type is not telemetry: it never changes, and the VIN already states
	// it. Deriving it removes a per-vehicle file every operator would otherwise
	// have to hand-maintain, and removes the wrong default (upstream shipped a
	// hardcoded model3, which silently mislabels every other model downstream in
	// TeslaMate and anything else consuming vehicle_data).
	//
	// Precedence: explicit config override, then the VIN, then whatever the
	// captured template supplied. The VIN is only used when it passes its check
	// digit AND names a model we know -- a malformed VIN is not evidence.
	if veh.CarType != "" {
		vc["car_type"] = veh.CarType
	} else if ct := vin.CarType(veh.VIN); ct != "" {
		vc["car_type"] = ct
	}

	return resp
}

func chargingState(snap store.Snapshot, d store.Derived) string {
	if v, ok := snap.Field(store.FieldChargeState); ok {
		if s := store.ChargeStateString(v.Value); s != "" {
			return s
		}
	}
	if d.Charging {
		return "Charging"
	}
	return "Disconnected"
}

// drivePower returns kW: negative while charging, pack power while driving.
//
// Sign convention follows the Fleet API: charging is negative, discharging
// positive. PackCurrent decays to a small residual when parked and never
// reaches a clean 0, so pack power is only reported while the car is actually
// moving -- otherwise a parked car reports a permanent trickle.
//
// PackVoltage/PackCurrent must be enrolled in the telemetry field set for this
// to produce anything; without them power stays 0, as it did before.
func drivePower(snap store.Snapshot, d store.Derived) int {
	if p, ok := snap.ChargerPower(); ok && p > 0 {
		return -int(p)
	}
	if d.Driving {
		if kw, ok := PackPowerKW(snap); ok {
			return int(math.Round(kw))
		}
	}
	return 0
}

// PackPowerKW is instantaneous DC pack power in kW, derived from voltage and
// current. Reported only when both fields are present.
func PackPowerKW(snap store.Snapshot) (float64, bool) {
	v, vok := snap.Num(store.FieldPackVoltage)
	a, aok := snap.Num(store.FieldPackCurrent)
	if !vok || !aok {
		return 0, false
	}
	return v * a / 1000, true
}

// DisplayedSOC is the battery percentage the Fleet API reports, rounded the way
// Tesla rounds it. BatteryLevel is the DISPLAYED charge; Soc is the usable
// figure and reads about half a point lower, so the two must not be mixed
// between the HTTP and WebSocket paths for the same instant.
func DisplayedSOC(snap store.Snapshot) (int, bool) {
	if v, ok := snap.Num(store.FieldBatteryLevel); ok {
		return pct(v), true
	}
	if v, ok := snap.Num(store.FieldSoc); ok {
		return pct(v), true
	}
	return 0, false
}

// subObj returns m[key] as map[string]any, creating it if missing or wrong type.
func subObj(m map[string]any, key string) map[string]any {
	if existing, ok := m[key].(map[string]any); ok {
		return existing
	}
	sub := map[string]any{}
	m[key] = sub
	return sub
}

// pct rounds a percentage the way the Fleet API does. Verified against real
// vehicle_data for two cars: 59.553 -> 60, 69.851 -> 70.
func pct(v float64) int {
	return int(math.Round(v))
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
