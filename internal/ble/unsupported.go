//go:build !linux

package ble

import (
	"fmt"
	"runtime"
)

// This file provides non-Linux stubs so the daemon compiles and its non-BLE
// logic (rule engine, API, config, tick loop) stays testable on a dev host.
// The real BLE transport (tinygo.go) and pairing agent (agent.go) are
// //go:build linux — production runs on OpenWrt/BlueZ. See API.md §12.

// ScanAndConnect is unsupported off Linux; production uses the BlueZ backend.
func ScanAndConnect(namePrefix string) (Transport, error) {
	return ScanAndConnectPrefixes([]string{namePrefix})
}

// ScanAndConnectPrefixes is unsupported off Linux; production uses BlueZ.
func ScanAndConnectPrefixes(prefixes []string) (Transport, error) {
	return nil, fmt.Errorf("wattline: BLE transport is Linux/BlueZ only, not %s", runtime.GOOS)
}

// RegisterPairingAgent is a no-op off Linux (no BlueZ agent to register).
// Callers treat a nil error + no-op cancel as "no agent needed".
func RegisterPairingAgent(pin string, prompt ...*PasskeyPrompt) (func(), error) {
	SetAgentPIN(pin)
	return func() {}, nil
}

// NewBlueZPairOps is unsupported off Linux; the API surfaces the error.
func NewBlueZPairOps() (PairOps, error) {
	return nil, fmt.Errorf("wattline: pairing is Linux/BlueZ only, not %s", runtime.GOOS)
}
