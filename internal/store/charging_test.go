package store

import "testing"

// Residual DC power is not charging. A disconnected car keeps reporting a small
// DCChargingPower (measured 0.099 kW), and the old any-positive-power test read
// that as charging — which pins state "online" and opens a TeslaMate charge
// session that never closes.
func TestIsCharging_ResidualDCWhileDisconnected(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]FieldValue
		want   bool
	}{
		{
			"disconnected with residual DC power",
			map[string]FieldValue{
				FieldChargeState:     {Value: "DetailedChargeStateDisconnected"},
				FieldDCChargingPower: {Value: 0.099},
				FieldACChargingPower: {Value: 0.0},
			},
			false,
		},
		{
			"explicit Charging outranks a zero power reading",
			map[string]FieldValue{
				FieldChargeState:     {Value: "DetailedChargeStateCharging"},
				FieldACChargingPower: {Value: 0.0},
			},
			true,
		},
		{
			"complete with retained positive power is not charging",
			map[string]FieldValue{
				FieldChargeState:     {Value: "DetailedChargeStateComplete"},
				FieldACChargingPower: {Value: 7.0},
			},
			false,
		},
		{
			"real DC charging with no detailed state",
			map[string]FieldValue{FieldDCChargingPower: {Value: 48.0}},
			true,
		},
		{
			"residual DC with no detailed state stays below the floor",
			map[string]FieldValue{FieldDCChargingPower: {Value: 0.099}},
			false,
		},
		{
			"AC charging with no detailed state",
			map[string]FieldValue{FieldACChargingPower: {Value: 11.0}},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCharging(Snapshot{Fields: tc.fields}); got != tc.want {
				t.Errorf("isCharging = %v, want %v", got, tc.want)
			}
		})
	}
}
