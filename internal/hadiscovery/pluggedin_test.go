package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// TestPluggedIn covers the derivation of the plugged_in binary_sensor, with
// emphasis on the production bug where ChargingCableType (which Tesla never
// resets to CableTypeNone) pinned plugged_in "on" forever after the first
// charge — even while driving.
func TestPluggedIn(t *testing.T) {
	const iec = "CableTypeIEC"
	const engaged = "ChargePortLatchEngaged"
	const disengaged = "ChargePortLatchDisengaged"

	type field struct {
		name  string
		value any
	}
	cases := []struct {
		name   string
		fields []field
		want   bool
	}{
		{
			name:   "latch engaged, parked",
			fields: []field{{store.FieldChargePortLatch, engaged}, {store.FieldGear, "ShiftStateP"}},
			want:   true,
		},
		{
			name:   "latch disengaged clears even with sticky cable set (production bug)",
			fields: []field{{store.FieldChargePortLatch, disengaged}, {store.FieldChargingCableType, iec}, {store.FieldGear, "ShiftStateP"}},
			want:   false,
		},
		{
			name:   "driving forces false despite stale engaged latch + sticky cable",
			fields: []field{{store.FieldChargePortLatch, engaged}, {store.FieldChargingCableType, iec}, {store.FieldGear, "ShiftStateD"}},
			want:   false,
		},
		{
			name:   "speed>0 forces false",
			fields: []field{{store.FieldChargePortLatch, engaged}, {store.FieldChargingCableType, iec}, {store.FieldVehicleSpeed, float64(37)}},
			want:   false,
		},
		{
			name:   "no latch yet, cable present, parked -> fallback true",
			fields: []field{{store.FieldChargingCableType, iec}, {store.FieldGear, "ShiftStateP"}},
			want:   true,
		},
		{
			name:   "no latch yet, charge-port door open -> fallback true",
			fields: []field{{store.FieldChargePortDoorOpen, true}},
			want:   true,
		},
		{
			name:   "nothing known",
			fields: nil,
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New("VIN")
			for _, f := range tc.fields {
				st.SetField("VIN", f.name, f.value)
			}
			snap, _ := st.Snapshot("VIN")
			if got := pluggedIn(snap); got != tc.want {
				t.Errorf("pluggedIn = %v, want %v", got, tc.want)
			}
		})
	}
}
