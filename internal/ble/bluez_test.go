//go:build linux

package ble

import (
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestDiscoveryInProgress(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "canonical D-Bus name",
			err:  dbus.NewError("org.bluez.Error.InProgress", []interface{}{"Operation already in progress"}),
			want: true,
		},
		{name: "legacy text", err: errors.New("org.bluez.Error.InProgress"), want: true},
		{name: "GL-X3000 text", err: errors.New("Operation already in progress"), want: true},
		{
			name: "unrelated named D-Bus error",
			err:  dbus.NewError("org.bluez.Error.Failed", []interface{}{"Operation already in progress"}),
			want: false,
		},
		{name: "unrelated text", err: errors.New("Discovery adapter unavailable"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discoveryInProgress(tt.err); got != tt.want {
				t.Fatalf("discoveryInProgress(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestPairPreparationPolicy(t *testing.T) {
	tests := []struct {
		name    string
		recover bool
		paired  bool
		want    bool
	}{
		{name: "ordinary unpaired", recover: false, paired: false, want: false},
		{name: "ordinary paired", recover: false, paired: true, want: true},
		{name: "recover orphaned", recover: true, paired: false, want: true},
		{name: "recover paired", recover: true, paired: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRemoveBeforePair(tt.recover, tt.paired); got != tt.want {
				t.Fatalf("shouldRemoveBeforePair(%v, %v) = %v, want %v", tt.recover, tt.paired, got, tt.want)
			}
		})
	}
}

func TestBondConfirmationRequiresBooleanTrue(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "paired", value: true, want: true},
		{name: "not paired", value: false, want: false},
		{name: "missing", value: nil, want: false},
		{name: "wrong type", value: "true", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := bondConfirmed(tt.value); got != tt.want {
				t.Fatalf("bondConfirmed(%#v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestBondConfirmationProgressMessagesAreCurated(t *testing.T) {
	for _, phase := range []PairingPhase{
		PhaseClearingStaleBond, PhaseLocatingDevice, PhaseExchangingPIN, PhaseConfirmingBond,
	} {
		message := bluezPairMessage(phase)
		if message == "" {
			t.Fatalf("phase %q has no message", phase)
		}
		if strings.Contains(message, "020555") {
			t.Fatalf("phase %q leaked PIN in %q", phase, message)
		}
	}
}
