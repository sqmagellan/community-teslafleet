// Package store keeps the latest known telemetry state per vehicle (keyed by VIN),
// updated from the fleet-telemetry MQTT stream and read by the Fleet API emulator
// and the Home Assistant publisher.
package store

import (
	"sync"
	"time"
)

// Telemetry field names as emitted by fleet-telemetry (topic .../v/<Field>).
const (
	FieldLocation        = "Location"
	FieldVehicleSpeed    = "VehicleSpeed"
	FieldGpsHeading      = "GpsHeading"
	FieldGear            = "Gear"
	FieldSoc             = "Soc"
	FieldOdometer        = "Odometer"
	FieldRatedRange      = "RatedRange"
	FieldEstBatteryRange = "EstBatteryRange"
	FieldACChargingPower = "ACChargingPower"
	FieldDCChargingPower = "DCChargingPower"
	// Optional/enriched fields (Phase 6) — handled if present.
	FieldBatteryLevel       = "BatteryLevel"
	FieldEnergyRemaining    = "EnergyRemaining"
	FieldLifetimeEnergyUsed = "LifetimeEnergyUsed"
	FieldIdealBatteryRange  = "IdealBatteryRange"
	FieldChargeRate         = "ChargeRateMilePerHour"
	FieldInsideTemp         = "InsideTemp"
	FieldOutsideTemp        = "OutsideTemp"
	FieldIsClimateOn        = "HvacPower"
	FieldLocked             = "Locked"
	FieldSentryMode         = "SentryMode"
	FieldDoorState          = "DoorState"
	FieldWindowFd           = "FdWindow"
	FieldWindowFp           = "FpWindow"
	FieldWindowRd           = "RdWindow"
	FieldWindowRp           = "RpWindow"
	FieldChargeState        = "DetailedChargeState"
	FieldChargeStateRaw     = "ChargeState"
	FieldChargeLimitSoc     = "ChargeLimitSoc"
	FieldChargePortDoorOpen = "ChargePortDoorOpen"
	FieldChargePortLatch    = "ChargePortLatch"
	FieldChargingCableType  = "ChargingCableType"
	FieldChargeEnergyIn     = "ACChargingEnergyIn"
	FieldTimeToFullCharge   = "TimeToFullCharge"
	FieldChargerVoltage     = "ChargerVoltage"
	FieldChargeAmps         = "ChargeAmps"
	FieldCenterDisplay      = "CenterDisplay"
	FieldVersion            = "Version"
	FieldDriverSeatOccupied = "DriverSeatOccupied"
	FieldLocatedAtHome      = "LocatedAtHome"
	FieldDestinationLocation = "DestinationLocation"
	FieldOriginLocation     = "OriginLocation"
	FieldTpmsFL             = "TpmsPressureFl"
	FieldTpmsFR             = "TpmsPressureFr"
	FieldTpmsRL             = "TpmsPressureRl"
	FieldTpmsRR             = "TpmsPressureRr"
	FieldTpmsHardWarnings   = "TpmsHardWarnings"
	FieldTpmsSoftWarnings   = "TpmsSoftWarnings"
	FieldPackVoltage        = "PackVoltage"
	FieldPackCurrent        = "PackCurrent"
	FieldHvacLeftTempReq    = "HvacLeftTemperatureRequest"
	// Active-route fields (stream only while navigating).
	FieldMilesToArrival    = "MilesToArrival"
	FieldMinutesToArrival  = "MinutesToArrival"
	FieldEnergyAtArrival   = "ExpectedEnergyPercentAtTripArrival"
	FieldDestinationName   = "DestinationName"
	FieldRouteTrafficDelay = "RouteTrafficMinutesDelay"
)

// FieldValue is a decoded telemetry value plus when it arrived and how many
// times the field has been received since startup (drive-verification counter).
type FieldValue struct {
	Value     any
	UpdatedAt time.Time
	Count     int
}

// Snapshot is an immutable copy of a vehicle's state at a point in time.
type Snapshot struct {
	VIN          string
	Fields       map[string]FieldValue
	Connectivity string
	ConnAt       time.Time
	LastV        time.Time
}

func (s Snapshot) Field(name string) (FieldValue, bool) {
	fv, ok := s.Fields[name]
	return fv, ok
}

// Num returns a numeric field value as float64.
func (s Snapshot) Num(name string) (float64, bool) {
	if fv, ok := s.Fields[name]; ok {
		return ToFloat(fv.Value)
	}
	return 0, false
}

// Bool returns a boolean field value.
func (s Snapshot) Bool(name string) (bool, bool) {
	if fv, ok := s.Fields[name]; ok {
		return ToBool(fv.Value)
	}
	return false, false
}

// Str returns a field value as its string form ("" if absent or non-string-ish).
func (s Snapshot) Str(name string) string {
	if fv, ok := s.Fields[name]; ok {
		return asString(fv.Value)
	}
	return ""
}

// Location extracts lat/lng from the Location field. Returns 0,0,false on failure.
func (s Snapshot) Location() (lat, lng float64, ok bool) {
	return s.LocationField(FieldLocation)
}

// LocationField extracts lat/lng from any {latitude,longitude} dict field
// (Location, DestinationLocation, OriginLocation). Returns 0,0,false on failure.
func (s Snapshot) LocationField(name string) (lat, lng float64, ok bool) {
	fv, has := s.Fields[name]
	if !has {
		return 0, 0, false
	}
	m, isMap := fv.Value.(map[string]any)
	if !isMap {
		return 0, 0, false
	}
	la, laOk := ToFloat(m["latitude"])
	lo, loOk := ToFloat(m["longitude"])
	if !laOk || !loOk {
		return 0, 0, false
	}
	return la, lo, true
}

// BoolMap returns a dict field's bool sub-values (DoorState, TpmsHardWarnings,
// TpmsSoftWarnings). Returns nil if the field is absent or not a dict.
func (s Snapshot) BoolMap(name string) map[string]bool {
	fv, has := s.Fields[name]
	if !has {
		return nil
	}
	m, isMap := fv.Value.(map[string]any)
	if !isMap {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		if b, ok := ToBool(v); ok {
			out[k] = b
		}
	}
	return out
}

// ChargerPower returns max(ACChargingPower, DCChargingPower) in kW, and whether
// either field was present.
func (s Snapshot) ChargerPower() (float64, bool) {
	var max float64
	var found bool
	for _, f := range []string{FieldACChargingPower, FieldDCChargingPower} {
		if v, ok := s.Num(f); ok {
			found = true
			if v > max {
				max = v
			}
		}
	}
	return max, found
}

type vehicle struct {
	mu           sync.RWMutex
	fields       map[string]FieldValue
	connectivity string
	connAt       time.Time
	lastV        time.Time
}

// Store is the concurrency-safe in-memory state of all known vehicles.
type Store struct {
	mu       sync.RWMutex
	vehicles map[string]*vehicle
	now      func() time.Time

	// allow restricts which VINs are accepted. When non-nil, only listed VINs are
	// stored (an explicit allow-list from config). When nil (no VINs configured),
	// every VIN seen on the stream is auto-registered — telemetry IS the source of
	// truth for which cars exist.
	allow map[string]bool
}

// New creates a Store pre-populated with the given VINs. Only pre-populated VINs
// are tracked: telemetry for unknown VINs is dropped (get() is then a pure RLock
// read on the hot path). Passing no VINs yields an empty store (used by tests).
func New(vins ...string) *Store {
	s := &Store{vehicles: map[string]*vehicle{}, now: time.Now}
	for _, vin := range vins {
		if vin != "" {
			if s.allow == nil {
				s.allow = map[string]bool{}
			}
			s.allow[vin] = true
			s.vehicles[vin] = &vehicle{fields: map[string]FieldValue{}}
		}
	}
	return s
}

// getOrCreate returns the vehicle for vin, creating it on first sight when no
// allow-list is configured (auto-discovery). Returns nil for an empty vin or a
// vin excluded by the allow-list.
func (s *Store) getOrCreate(vin string) *vehicle {
	if vin == "" {
		return nil
	}
	s.mu.RLock()
	v := s.vehicles[vin]
	s.mu.RUnlock()
	if v != nil {
		return v
	}
	if s.allow != nil && !s.allow[vin] {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v = s.vehicles[vin]; v != nil { // re-check after upgrading the lock
		return v
	}
	v = &vehicle{fields: map[string]FieldValue{}}
	s.vehicles[vin] = v
	return v
}

// SetField records the latest value of a telemetry field for a vehicle. The
// vehicle is auto-registered on first sight unless excluded by the allow-list.
func (s *Store) SetField(vin, field string, value any) {
	v := s.getOrCreate(vin)
	if v == nil {
		return
	}
	now := s.now()
	v.mu.Lock()
	v.fields[field] = FieldValue{Value: value, UpdatedAt: now, Count: v.fields[field].Count + 1}
	v.lastV = now
	v.mu.Unlock()
}

// SetConnectivity records the latest connectivity status (online/offline/reconnect).
// The vehicle is auto-registered on first sight unless excluded by the allow-list.
func (s *Store) SetConnectivity(vin, status string) {
	v := s.getOrCreate(vin)
	if v == nil {
		return
	}
	now := s.now()
	v.mu.Lock()
	v.connectivity = status
	v.connAt = now
	v.mu.Unlock()
}

// Snapshot returns a deep-ish copy of a vehicle's current state.
func (s *Store) Snapshot(vin string) (Snapshot, bool) {
	s.mu.RLock()
	v := s.vehicles[vin]
	s.mu.RUnlock()
	if v == nil {
		return Snapshot{VIN: vin, Fields: map[string]FieldValue{}}, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	fields := make(map[string]FieldValue, len(v.fields))
	for k, val := range v.fields {
		fields[k] = val
	}
	return Snapshot{
		VIN:          vin,
		Fields:       fields,
		Connectivity: v.connectivity,
		ConnAt:       v.connAt,
		LastV:        v.lastV,
	}, true
}

// VINs returns all VINs seen so far.
func (s *Store) VINs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.vehicles))
	for vin := range s.vehicles {
		out = append(out, vin)
	}
	return out
}

// Now returns the store's clock (overridable in tests).
func (s *Store) Now() time.Time { return s.now() }

// SetClock replaces the store's clock. It is a test seam, not runtime
// reconfiguration: staleness, sleep detection and readiness are all pure
// functions of elapsed time, and asserting them against the wall clock means
// either sleeping in tests or accepting flakes. Call it before the store is
// shared with other goroutines.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// LastIngest returns the most recent moment ANY vehicle received a telemetry
// field, or the zero time if nothing has ever been ingested.
//
// This is the number a health check needs. Per-vehicle silence is normal and
// expected — a sleeping car streams nothing for hours — but silence across the
// whole fleet while a car is awake means ingest has stopped, which is otherwise
// invisible: the MQTT publisher runs off a ticker and keeps republishing the
// in-memory store at full rate, so message flow proves nothing about ingest.
func (s *Store) LastIngest() time.Time {
	// Copy the vehicle pointers under the store lock, then read each vehicle
	// under its own lock, so the two locks are never held at the same time.
	s.mu.RLock()
	vehicles := make([]*vehicle, 0, len(s.vehicles))
	for _, v := range s.vehicles {
		vehicles = append(vehicles, v)
	}
	s.mu.RUnlock()

	var last time.Time
	for _, v := range vehicles {
		v.mu.RLock()
		if v.lastV.After(last) {
			last = v.lastV
		}
		v.mu.RUnlock()
	}
	return last
}
