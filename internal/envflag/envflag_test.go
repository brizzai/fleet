package envflag

import "testing"

func TestIsTruthy(t *testing.T) {
	for v, want := range map[string]bool{
		"1": true, "true": true, "TRUE": true, " yes ": true, "y": true, "on": true,
		"": false, "0": false, "false": false, "no": false, "off": false, "2": false, "enabled": false,
	} {
		if got := IsTruthy(v); got != want {
			t.Errorf("IsTruthy(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestSet(t *testing.T) {
	t.Setenv("FLEET_ENVFLAG_TEST", "on")
	if !Set("FLEET_ENVFLAG_TEST") {
		t.Error("Set = false for on")
	}
	t.Setenv("FLEET_ENVFLAG_TEST", "")
	if Set("FLEET_ENVFLAG_TEST") {
		t.Error("Set = true for empty")
	}
}
