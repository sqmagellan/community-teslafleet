package commands

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// Range is the span a number command accepts, inclusive.
type Range struct{ Min, Max float64 }

// staticRanges are the bounds used when the car has not reported its own.
// charging_amps tops out at 48 A, the largest AC request current Teslas take
// on a Wall Connector; a car that reports a lower maximum gets that instead.
// climate_temp is in °C, the unit the climate entity declares.
var staticRanges = map[string]Range{
	"charge_limit":      {Min: 50, Max: 100},
	"charging_amps":     {Min: 0, Max: 48},
	"speed_limit_value": {Min: 50, Max: 90},
	"climate_temp":      {Min: 15, Max: 28},
}

// fieldChargeCurrentRequestMax is the telemetry field for the most current
// the car will request.
const fieldChargeCurrentRequestMax = "ChargeCurrentRequestMax"

// carLimits holds limits read from the last vehicle_data document the relay
// fetched for each car (charge_state.charge_current_request_max and
// charge_limit_soc_min/max). Telemetry wins over it where both exist.
type carLimits struct {
	mu  sync.Mutex
	vin map[string]map[string]float64
}

func (c *carLimits) set(vin string, vals map[string]float64) {
	if c == nil || len(vals) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vin == nil {
		c.vin = map[string]map[string]float64{}
	}
	c.vin[vin] = vals
}

func (c *carLimits) get(vin, key string) (float64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vin[vin][key]
	return v, ok
}

// recordLimits keeps the limits a vehicle_data document carries.
func (c *carLimits) recordLimits(vin string, doc map[string]any) {
	cs, _ := doc["charge_state"].(map[string]any)
	if cs == nil {
		return
	}
	vals := map[string]float64{}
	for _, k := range []string{"charge_current_request_max", "charge_limit_soc_min", "charge_limit_soc_max"} {
		if f, ok := num(cs[k]); ok && f > 0 {
			vals[k] = f
		}
	}
	c.set(vin, vals)
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// StaticRange returns the fallback bounds for a number command key.
func StaticRange(key string) (Range, bool) {
	rg, ok := staticRanges[key]
	return rg, ok
}

// NumberRange returns the bounds for a number command on one car: the car's
// own reported limits where it has reported them, the static ones otherwise.
// A reported value is used only if it leaves a non-empty range.
func (r *Relay) NumberRange(vin, key string) (Range, bool) {
	rg, ok := staticRanges[key]
	if !ok || r == nil {
		return rg, ok
	}
	switch key {
	case "charging_amps":
		if m, ok := r.reportedMaxAmps(vin); ok && m > rg.Min {
			rg.Max = m
		}
	case "charge_limit":
		lo, hi := rg.Min, rg.Max
		if v, ok := r.limits.get(vin, "charge_limit_soc_min"); ok {
			lo = v
		}
		if v, ok := r.limits.get(vin, "charge_limit_soc_max"); ok {
			hi = v
		}
		if lo < hi && lo >= 0 && hi <= 100 {
			rg.Min, rg.Max = lo, hi
		}
	}
	return rg, true
}

func (r *Relay) reportedMaxAmps(vin string) (float64, bool) {
	if r.store != nil {
		if snap, ok := r.store.Snapshot(vin); ok {
			if m, ok := snap.Num(fieldChargeCurrentRequestMax); ok && m > 0 {
				return m, true
			}
		}
	}
	return r.limits.get(vin, "charge_current_request_max")
}

// outOfRange returns a refusal reason when v is outside key's range.
func (r *Relay) outOfRange(vin, key string, v float64) (string, bool) {
	rg, ok := r.NumberRange(vin, key)
	if !ok || (v >= rg.Min && v <= rg.Max) {
		return "", false
	}
	return fmt.Sprintf("%s %s is outside %s..%s", key, fmtNum(v), fmtNum(rg.Min), fmtNum(rg.Max)), true
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// NewRelayForTest returns a relay that reaches no proxy and holds no token,
// reading car state from st. It lets other packages' tests ask it for number
// ranges.
func NewRelayForTest(st *store.Store) *Relay {
	return &Relay{store: st, proxyHealth: &proxyHealth{}}
}
