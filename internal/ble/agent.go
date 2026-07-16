//go:build linux

package ble

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// pairingAgent supplies a fixed PIN when BlueZ requests one (manual p.16:
// default fixed PIN 020555, overridable). Registered as the system agent.
type pairingAgent struct {
	pin string
}

func (a *pairingAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	return a.pin, nil
}
func (a *pairingAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	var p uint32
	fmt.Sscanf(a.pin, "%d", &p)
	return p, nil
}
func (a *pairingAgent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	return nil
}
func (a *pairingAgent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}
func (a *pairingAgent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return nil
}
func (a *pairingAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error { return nil }
func (a *pairingAgent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	return nil
}
func (a *pairingAgent) Cancel() *dbus.Error   { return nil }
func (a *pairingAgent) Release() *dbus.Error  { return nil }

const agentPath = dbus.ObjectPath("/com/wattline/agent")

// RegisterPairingAgent exports a BlueZ agent on the system bus and makes it
// the default. Returns a cancel func that unregisters it.
func RegisterPairingAgent(pin string) (func(), error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	agent := &pairingAgent{pin: pin}
	// Export all org.bluez.Agent1 methods.
	if err := conn.Export(agent, agentPath, "org.bluez.Agent1"); err != nil {
		return nil, fmt.Errorf("export agent: %w", err)
	}
	mgr := conn.Object("org.bluez", "/org/bluez")
	if call := mgr.Call("org.bluez.AgentManager1.RegisterAgent", 0, agentPath, "KeyboardOnly"); call.Err != nil {
		return nil, fmt.Errorf("register agent: %w", call.Err)
	}
	mgr.Call("org.bluez.AgentManager1.RequestDefaultAgent", 0, agentPath)
	return func() {
		mgr.Call("org.bluez.AgentManager1.UnregisterAgent", 0, agentPath)
	}, nil
}
