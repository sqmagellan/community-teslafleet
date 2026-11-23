package enroll

import (
	"slices"
	"testing"
)

// Doors, windows and the lock went out at most every 5 minutes in eco, so a
// door opened and closed showed as open for 5 minutes, and for hours when the
// car slept before the close was sent.

var closureFields = []string{"DoorState", "FdWindow", "FpWindow", "Locked", "RdWindow", "RpWindow"}

func TestClosureCategoryIsDoorsWindowsAndLock(t *testing.T) {
	var got []string
	for _, f := range Fields {
		if category(f) == "closure" {
			got = append(got, f)
		}
	}
	if !slices.Equal(got, closureFields) {
		t.Errorf("closure fields = %v, want %v", got, closureFields)
	}
}

func TestClosuresAreSentPromptlyInEveryProfile(t *testing.T) {
	for _, profile := range []string{"eco", "balanced", "live"} {
		f := Generate(profile, "x", 443).Fields
		for _, name := range closureFields {
			if f[name]["interval_seconds"] != 5 || f[name]["resend_interval_seconds"] != 600 {
				t.Errorf("%s %s = %v, want interval 5 and resend 600", profile, name, f[name])
			}
		}
	}
}
