package enroll

import "testing"

// Fields the categories did not name fell into "diagnostic", which eco turns
// off, so eco never sent the odometer or sentry mode. They are now "state":
// in eco sent at most every 5 minutes when they change, never resent; balanced
// and live keep the cadence they had.

func TestUnnamedFieldsAreVehicleState(t *testing.T) {
	for _, f := range []string{"Odometer", "SentryMode", "ServiceMode", "LightsHazardsActive"} {
		if got := category(f); got != "state" {
			t.Errorf("category(%s) = %q, want state", f, got)
		}
	}
	if got := category("DiAxleSpeedF"); got != "diagnostic" {
		t.Errorf("category(DiAxleSpeedF) = %q, want diagnostic", got)
	}
}

func TestEcoSendsVehicleStateRarely(t *testing.T) {
	f := Generate("eco", "x", 443).Fields
	odo, ok := f["Odometer"]
	if !ok {
		t.Fatal("eco does not enroll Odometer")
	}
	if odo["interval_seconds"] != 300 || odo["resend_interval_seconds"] != 0 {
		t.Errorf("eco Odometer = %v, want interval 300 and no resend", odo)
	}
	if _, ok := f["DiAxleSpeedF"]; ok {
		t.Error("eco enrolls a diagnostic field")
	}
}

func TestBalancedAndLiveKeepTheirStateCadence(t *testing.T) {
	b := Generate("balanced", "x", 443).Fields["Odometer"]
	if b["interval_seconds"] != 300 || b["resend_interval_seconds"] != 600 {
		t.Errorf("balanced Odometer = %v, want slow (300, resend 600)", b)
	}
	l := Generate("live", "x", 443).Fields["Odometer"]
	if l["interval_seconds"] != 60 {
		t.Errorf("live Odometer = %v, want normal (60)", l)
	}
}
