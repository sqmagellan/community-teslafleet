package store

import "testing"

func TestHvacOn(t *testing.T) {
	cases := []struct {
		in     any
		want   bool
		wantOK bool
	}{
		{"HvacPowerStateOn", true, true},
		{"HvacPowerStateOff", false, true},
		{"HvacPowerStatePrecondition", true, true},
		{"HvacPowerStateOverheatProtect", true, true},
		{"On", true, true},
		{"Off", false, true},
		{true, true, true},
		{false, false, true},
		{"HvacPowerStateUnknown", false, false},
		{"", false, false},
		{nil, false, false},
	}
	for _, tc := range cases {
		got, ok := HvacOn(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("HvacOn(%v) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
