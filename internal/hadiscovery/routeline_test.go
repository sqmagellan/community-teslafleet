package hadiscovery

import (
	"strings"
	"testing"

	"github.com/LasseLegarth/community-teslafleet/internal/config"
	"github.com/LasseLegarth/community-teslafleet/internal/store"
)

// TestBuildState_DropsRouteLineAndOverlongStrings guards the fix for HA
// spamming "state exceeds maximum length (255)": RouteLine (an encoded nav
// polyline, ~2800 chars while navigating) must never become a sensor state,
// and any unforeseen over-long string must be dropped rather than churn.
func TestBuildState_DropsRouteLineAndOverlongStrings(t *testing.T) {
	vin := "VINROUTE"
	st := store.New(vin)
	st.SetField(vin, "RouteLine", "EgUN2lhaQRIH")                  // short, excluded by name
	st.SetField(vin, "UnknownLongField", strings.Repeat("B", 300)) // long, excluded by length guard
	st.SetField(vin, "ShortRaw", "ok")                             // normal raw field, kept
	snap, _ := st.Snapshot(vin)

	s := buildState(snap, store.Derived{State: "online"}, config.Units{System: "metric"})

	if _, ok := s["RouteLine"]; ok {
		t.Error("RouteLine must never be a state field (excluded by name)")
	}
	if _, ok := s["UnknownLongField"]; ok {
		t.Error("a string >255 chars must be dropped (exceeds HA state limit)")
	}
	if s["ShortRaw"] != "ok" {
		t.Errorf("ShortRaw = %v, want ok (normal raw fields still pass through)", s["ShortRaw"])
	}
}
