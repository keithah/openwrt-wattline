//go:build linux

package ble

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// bluezPairer implements PairOps over the BlueZ D-Bus API. It shares the
// system bus with the pairing agent (agent.go); the tinygo transport talks to
// the same bluetoothd, so pairing operates on the same device objects the
// connector uses.
type bluezPairer struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
}

// NewBlueZPairOps connects to the system bus and locates the first adapter.
func NewBlueZPairOps() (PairOps, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	p := &bluezPairer{conn: conn}
	if p.adapter, err = p.findAdapter(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *bluezPairer) managedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := p.conn.Object("org.bluez", "/").
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs)
	if err != nil {
		return nil, fmt.Errorf("GetManagedObjects: %w", err)
	}
	return objs, nil
}

func (p *bluezPairer) findAdapter() (dbus.ObjectPath, error) {
	objs, err := p.managedObjects()
	if err != nil {
		return "", err
	}
	for path, ifaces := range objs {
		if _, ok := ifaces["org.bluez.Adapter1"]; ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("no bluetooth adapter found")
}

func (p *bluezPairer) adapterObj() dbus.BusObject {
	return p.conn.Object("org.bluez", p.adapter)
}

func (p *bluezPairer) devicePath(mac string) dbus.ObjectPath {
	return dbus.ObjectPath(string(p.adapter) + "/dev_" +
		strings.ToUpper(strings.ReplaceAll(mac, ":", "_")))
}

func (p *bluezPairer) deviceObj(mac string) dbus.BusObject {
	return p.conn.Object("org.bluez", p.devicePath(mac))
}

func shouldRemoveBeforePair(recover, paired bool) bool { return recover || paired }

func bondConfirmed(value any) bool {
	paired, ok := value.(bool)
	return ok && paired
}

func bluezPairMessage(phase PairingPhase) string {
	switch phase {
	case PhaseClearingStaleBond:
		return "Clearing the router's stale pairing record"
	case PhaseLocatingDevice:
		return "Locating Link-Power"
	case PhaseExchangingPIN:
		return "Exchanging the pairing PIN"
	case PhaseConfirmingBond:
		return "Confirming the Bluetooth bond"
	default:
		return ""
	}
}

func reportBlueZPair(report PairProgress, phase PairingPhase) {
	if report != nil {
		report(phase, bluezPairMessage(phase))
	}
}

func discoveryInProgress(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr *dbus.Error
	if errors.As(err, &dbusErr) {
		return dbusErr.Name == "org.bluez.Error.InProgress"
	}
	message := err.Error()
	return strings.Contains(message, "InProgress") ||
		strings.Contains(message, "Operation already in progress")
}

// wattlineDevice reports whether an advertised name looks like a Link-Power
// (app mode) or PeakDo-OTA (bootloader). Matches the PWA's scan filter.
func wattlineDevice(name string) bool {
	return strings.HasPrefix(name, "Link-Power") || strings.HasPrefix(name, "PeakDo-OTA")
}

// startDiscovery begins LE discovery and returns a stop func. An InProgress
// error is benign (someone else is scanning; results still land in the
// object tree); filter errors on older BlueZ are ignored.
func (p *bluezPairer) startDiscovery() (func(), error) {
	ad := p.adapterObj()
	ad.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0,
		map[string]dbus.Variant{"Transport": dbus.MakeVariant("le")})
	if call := ad.Call("org.bluez.Adapter1.StartDiscovery", 0); call.Err != nil {
		if !discoveryInProgress(call.Err) {
			return nil, fmt.Errorf("StartDiscovery: %w", call.Err)
		}
		return func() {}, nil
	}
	return func() { ad.Call("org.bluez.Adapter1.StopDiscovery", 0) }, nil
}

func (p *bluezPairer) Scan(dur time.Duration) ([]Found, error) {
	stop, err := p.startDiscovery()
	if err != nil {
		return nil, err
	}
	defer stop()
	time.Sleep(dur)

	objs, err := p.managedObjects()
	if err != nil {
		return nil, err
	}
	var out []Found
	for _, ifaces := range objs {
		dev, ok := ifaces["org.bluez.Device1"]
		if !ok {
			continue
		}
		str := func(k string) string {
			if v, ok := dev[k]; ok {
				if s, ok := v.Value().(string); ok {
					return s
				}
			}
			return ""
		}
		name := str("Name")
		if !wattlineDevice(name) {
			continue
		}
		// No RSSI property = a cached BlueZ object from an earlier scan, not a
		// device that is advertising now; listing it would offer the user a
		// device that Pair can only time out on.
		v, ok := dev["RSSI"]
		if !ok {
			continue
		}
		f := Found{MAC: str("Address"), Name: name}
		if r, ok := v.Value().(int16); ok {
			f.RSSI = r
		}
		if v, ok := dev["Paired"]; ok {
			if b, ok := v.Value().(bool); ok {
				f.Paired = b
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSSI > out[j].RSSI })
	return out, nil
}

// discoverUntilPresent scans until the device object exists (RemoveDevice
// deletes it, and Pair needs it back).
func (p *bluezPairer) discoverUntilPresent(mac string, timeout time.Duration) error {
	dev := p.deviceObj(mac)
	deadline := time.Now().Add(timeout)
	stop, err := p.startDiscovery()
	if err != nil {
		return err
	}
	defer stop()
	for time.Now().Before(deadline) {
		if _, err := dev.GetProperty("org.bluez.Device1.Address"); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("device %s not found during discovery (is it advertising?)", mac)
}

// Pair bonds with mac. Recovery always removes the router's BlueZ object;
// ordinary pairing removes only a locally confirmed bond. The first Pair after
// a remove has been observed to hang, so CancelPairing + retry is retained.
func (p *bluezPairer) Pair(mac string, recover bool, report PairProgress) error {
	dev := p.deviceObj(mac)
	paired := false
	if property, err := dev.GetProperty("org.bluez.Device1.Paired"); err == nil {
		paired = bondConfirmed(property.Value())
	}
	if shouldRemoveBeforePair(recover, paired) {
		if recover {
			reportBlueZPair(report, PhaseClearingStaleBond)
		}
		if err := p.Unpair(mac); err != nil {
			return fmt.Errorf("remove stale bond: %w", err)
		}
	}
	reportBlueZPair(report, PhaseLocatingDevice)
	if err := p.discoverUntilPresent(mac, 20*time.Second); err != nil {
		return err
	}
	reportBlueZPair(report, PhaseExchangingPIN)
	if err := p.pairOnce(mac, 30*time.Second); err != nil {
		return fmt.Errorf("pair failed: %w", err)
	}
	reportBlueZPair(report, PhaseConfirmingBond)
	pairedProperty, err := p.deviceObj(mac).GetProperty("org.bluez.Device1.Paired")
	if err != nil {
		return fmt.Errorf("confirm Bluetooth bond: %w", err)
	}
	if !bondConfirmed(pairedProperty.Value()) {
		return errors.New("BlueZ did not confirm Paired: yes after the PIN exchange")
	}
	return nil
}

func (p *bluezPairer) pairOnce(mac string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	call := p.deviceObj(mac).CallWithContext(ctx, "org.bluez.Device1.Pair", 0)
	if call.Err != nil {
		// Confirmation reads Device1.Paired after this call; AlreadyExists alone
		// is never accepted as proof of a stored bond.
		if strings.Contains(call.Err.Error(), "AlreadyExists") {
			return nil
		}
		return call.Err
	}
	return nil
}

func (p *bluezPairer) Trust(mac string) error {
	err := p.deviceObj(mac).SetProperty("org.bluez.Device1.Trusted", dbus.MakeVariant(true))
	if err != nil {
		return fmt.Errorf("set Trusted: %w", err)
	}
	return nil
}

func (p *bluezPairer) Unpair(mac string) error {
	call := p.adapterObj().Call("org.bluez.Adapter1.RemoveDevice", 0, p.devicePath(mac))
	if call.Err != nil && !strings.Contains(call.Err.Error(), "DoesNotExist") {
		return fmt.Errorf("RemoveDevice: %w", call.Err)
	}
	return nil
}
