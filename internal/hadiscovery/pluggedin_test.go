package hadiscovery

import (
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// TestPluggedIn covers the derivation of the plugged_in binary_sensor.
//
// ChargePortLatch is the only positive signal. Everything else is deliberately
// ignored as a positive signal because it is fragile:
//   - ChargingCableType is sticky (Tesla never sends CableTypeNone) — it used
//     to pin plugged_in on forever.
//   - A Gear/VehicleSpeed "driving" backstop read fields that stop at a
//     non-zero residual and never reach 0 — it stuck plugged_in *off* every
//     evening after a drive.
//
// The latch can however go stale Engaged, so it may be VETOED — never
// overridden — by two independent signals agreeing there is no cable. The veto
// truth table is TestPluggedInStaleLatchVeto.
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

// TestPluggedInStaleLatchVeto is the truth table for overruling a latch that is
// stuck Engaged. Both corroborating signals must be present and must both say
// "no cable"; anything else leaves the latch in charge.
//
// The row that motivated this: a car unplugged for hours still reported
// ChargePortLatchEngaged, with DetailedChargeState=Disconnected and the charge
// port closed. Note in particular the "Complete with closed port" row — an
// overnight-charged car must NOT be vetoed just because it stopped charging.
func TestPluggedInStaleLatchVeto(t *testing.T) {
	const engaged = "ChargePortLatchEngaged"

	// absent is a sentinel: the field is not set at all, which must veto nothing.
	const absent = "\x00absent"

	cases := []struct {
		name        string
		chargeState string
		doorOpen    string // "true" / "false" / absent
		want        bool
	}{
		{
			name:        "stale engaged latch vetoed by Disconnected + closed port",
			chargeState: "DetailedChargeStateDisconnected",
			doorOpen:    "false",
			want:        false,
		},
		{
			name:        "Disconnected but port OPEN -> latch wins (charge state may lag a plug-in)",
			chargeState: "DetailedChargeStateDisconnected",
			doorOpen:    "true",
			want:        true,
		},
		{
			name:        "charging with a closed port -> latch wins (a closed port cannot be charging)",
			chargeState: "DetailedChargeStateCharging",
			doorOpen:    "false",
			want:        true,
		},
		{
			// The common overnight state. Charging has finished but the cable is
			// still in. Only Disconnected may veto; Complete must not.
			name:        "charge Complete with closed port -> still plugged",
			chargeState: "DetailedChargeStateComplete",
			doorOpen:    "false",
			want:        true,
		},
		{
			name:        "NoPower with closed port -> still plugged",
			chargeState: "DetailedChargeStateNoPower",
			doorOpen:    "false",
			want:        true,
		},
		{
			name:        "Disconnected but port state never reported -> latch wins",
			chargeState: "DetailedChargeStateDisconnected",
			doorOpen:    absent,
			want:        true,
		},
		{
			name:        "closed port but charge state never reported -> latch wins",
			chargeState: absent,
			doorOpen:    "false",
			want:        true,
		},
		{
			name:        "neither corroborating signal reported -> latch wins",
			chargeState: absent,
			doorOpen:    absent,
			want:        true,
		},
		{
			// The enum arrives unprefixed from some sources; both forms must veto.
			name:        "unprefixed Disconnected also vetoes",
			chargeState: "Disconnected",
			doorOpen:    "false",
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New("VIN")
			st.SetField("VIN", store.FieldChargePortLatch, engaged)
			if tc.chargeState != absent {
				st.SetField("VIN", store.FieldChargeState, tc.chargeState)
			}
			switch tc.doorOpen {
			case "true":
				st.SetField("VIN", store.FieldChargePortDoorOpen, true)
			case "false":
				st.SetField("VIN", store.FieldChargePortDoorOpen, false)
			}
			snap, _ := st.Snapshot("VIN")
			if got := pluggedIn(snap); got != tc.want {
				t.Errorf("pluggedIn = %v, want %v (charge_state=%q door_open=%q)",
					got, tc.want, tc.chargeState, tc.doorOpen)
			}
		})
	}
}

// A disengaged latch must stay not-plugged no matter what the corroborating
// signals say: the veto path can only ever turn plugged_in OFF, never on.
func TestPluggedInVetoNeverTurnsOn(t *testing.T) {
	st := store.New("VIN")
	st.SetField("VIN", store.FieldChargePortLatch, "ChargePortLatchDisengaged")
	st.SetField("VIN", store.FieldChargeState, "DetailedChargeStateCharging")
	st.SetField("VIN", store.FieldChargePortDoorOpen, true)
	snap, _ := st.Snapshot("VIN")
	if pluggedIn(snap) {
		t.Error("pluggedIn = true with a disengaged latch; corroboration must not turn it on")
	}
}
