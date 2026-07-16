package ble

import (
	"fmt"
	"sync/atomic"
)

// agentPIN holds the PIN the BlueZ pairing agent supplies. It is settable at
// runtime so a GUI pair request can carry a PIN different from the configured
// one (the Link-Power supports a fixed OEM PIN and a random-PIN mode).
var agentPIN atomic.Value // string

// SetAgentPIN updates the PIN the pairing agent supplies. Empty input is
// ignored so a blank GUI field never clobbers the active PIN.
func SetAgentPIN(pin string) {
	if pin != "" {
		agentPIN.Store(pin)
	}
}

func currentAgentPIN() string {
	v, _ := agentPIN.Load().(string)
	return v
}

// pinToPasskey converts a numeric PIN string to the SMP passkey value
// (leading zeros carry no numeric weight; non-numeric input yields 0).
func pinToPasskey(pin string) uint32 {
	var p uint32
	fmt.Sscanf(pin, "%d", &p)
	return p
}
