// Package vehicledata assembles a Tesla Fleet API vehicle_data "response" object
// from live telemetry overlaid on a captured template.
package vehicledata

import (
	"time"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
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

	tsSec := now.Unix()
	tsMs := now.UnixMilli()
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
	ds["power"] = drivePower(snap)

	// ---- charge_state ----
	if soc, ok := snap.Num(store.FieldSoc); ok {
		cs["battery_level"] = int(soc)
		cs["usable_battery_level"] = int(soc)
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

// drivePower returns kW: negative while charging, telemetry-derived otherwise.
func drivePower(snap store.Snapshot) int {
	if p, ok := snap.ChargerPower(); ok && p > 0 {
		return -int(p)
	}
	return 0
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

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
