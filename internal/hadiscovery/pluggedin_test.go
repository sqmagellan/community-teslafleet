package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// TestPluggedIn covers the derivation of the plugged_in binary_sensor. It is
// now purely ChargePortLatch (Tesla's authoritative, both-ways signal); every
// other input is deliberately ignored because it is fragile:
//   - ChargingCableType is sticky (Tesla never sends CableTypeNone) — it used
//     to pin plugged_in on forever.
//   - A Gear/VehicleSpeed "driving" backstop read fields that stop at a
//     non-zero residual and never reach 0 — it stuck plugged_in *off* every
//     evening after a drive.
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
			name:   "latch engaged -> plugged",
			fields: []field{{store.FieldChargePortLatch, engaged}},
			want:   true,
		},
		{
			name:   "latch disengaged -> not plugged (ignores sticky cable + open door)",
			fields: []field{{store.FieldChargePortLatch, disengaged}, {store.FieldChargingCableType, iec}, {store.FieldChargePortDoorOpen, true}},
			want:   false,
		},
		{
			// Regression for the "not plugged in every evening" bug: after a
			// drive VehicleSpeed sticks at a small non-zero residual and Tesla
			// never streams 0. A parked, plugged car must stay plugged.
			name:   "parked with stale residual speed stays plugged",
			fields: []field{{store.FieldChargePortLatch, engaged}, {store.FieldGear, "ShiftStateP"}, {store.FieldVehicleSpeed, float64(0.621)}},
			want:   true,
		},
		{
			name:   "latch never reported -> not plugged (no guessing from cable/door)",
			fields: []field{{store.FieldChargingCableType, iec}, {store.FieldChargePortDoorOpen, true}},
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
