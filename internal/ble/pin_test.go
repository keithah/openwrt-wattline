package ble

import "testing"

func TestAgentPINDefaultsAndUpdates(t *testing.T) {
	SetAgentPIN("020555")
	if got := currentAgentPIN(); got != "020555" {
		t.Fatalf("currentAgentPIN = %q", got)
	}
	SetAgentPIN("111222")
	if got := currentAgentPIN(); got != "111222" {
		t.Fatalf("after update = %q", got)
	}
	SetAgentPIN("") // empty never clobbers the active PIN
	if got := currentAgentPIN(); got != "111222" {
		t.Fatalf("after empty set = %q", got)
	}
}

func TestPinToPasskey(t *testing.T) {
	cases := map[string]uint32{
		"020555": 20555, // leading zeros: same numeric passkey
		"000000": 0,
		"999999": 999999,
		"junk":   0,
	}
	for pin, want := range cases {
		if got := pinToPasskey(pin); got != want {
			t.Errorf("pinToPasskey(%q) = %d, want %d", pin, got, want)
		}
	}
}
