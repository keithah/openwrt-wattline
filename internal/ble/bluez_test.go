//go:build linux

package ble

import (
	"errors"
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
