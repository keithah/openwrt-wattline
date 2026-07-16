//go:build linux

package ble

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

var adapter = bluetooth.DefaultAdapter

type tinygoTransport struct {
	dev   bluetooth.Device
	chars map[string]bluetooth.DeviceCharacteristic
	disc  chan struct{}
	once  sync.Once
}

func normUUID(u string) string { return strings.ToLower(u) }

// ScanAndConnect scans for a device whose advertised local name starts with
// namePrefix (API.md §12: match adv name, not cached name), connects, and
// discovers the Link-Power + DIS characteristics.
func ScanAndConnect(namePrefix string) (Transport, error) {
	if err := adapter.Enable(); err != nil {
		return nil, fmt.Errorf("enable adapter: %w", err)
	}
	var found bluetooth.ScanResult
	ok := false
	// Bound the scan to 20s, but cancel the timer the moment Scan returns so a
	// fast scan doesn't leave an orphaned goroutine that later StopScan()s a
	// subsequent reconnect scan mid-flight.
	scanDone := make(chan struct{})
	go func() {
		select {
		case <-time.After(20 * time.Second):
			adapter.StopScan()
		case <-scanDone:
		}
	}()
	err := adapter.Scan(func(a *bluetooth.Adapter, res bluetooth.ScanResult) {
		if strings.HasPrefix(res.LocalName(), namePrefix) {
			found, ok = res, true
			a.StopScan()
		}
	})
	close(scanDone)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("no device advertising %q found", namePrefix)
	}
	dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	t := &tinygoTransport{dev: dev, chars: map[string]bluetooth.DeviceCharacteristic{}, disc: make(chan struct{})}
	// Register the disconnect handler immediately after connecting, before
	// service/characteristic discovery, so a disconnect during discovery is
	// not lost (which would otherwise hang the connector forever waiting on
	// t.disc). This assumes a single peripheral is connected at a time; the
	// handler below only reacts to events for this transport's device.
	adapter.SetConnectHandler(func(d bluetooth.Device, connected bool) {
		if !connected && d.Address.String() == t.dev.Address.String() {
			t.once.Do(func() { close(t.disc) })
		}
	})
	svcs, err := dev.DiscoverServices(nil)
	if err != nil {
		return nil, fmt.Errorf("discover services: %w", err)
	}
	for _, svc := range svcs {
		chs, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			continue
		}
		for _, ch := range chs {
			t.chars[normUUID(ch.UUID().String())] = ch
		}
	}
	return t, nil
}

func (t *tinygoTransport) char(uuid string) (bluetooth.DeviceCharacteristic, error) {
	ch, ok := t.chars[normUUID(uuid)]
	if !ok {
		return bluetooth.DeviceCharacteristic{}, fmt.Errorf("characteristic %s not found", uuid)
	}
	return ch, nil
}

func (t *tinygoTransport) WriteChar(uuid string, data []byte) error {
	ch, err := t.char(uuid)
	if err != nil {
		return err
	}
	_, err = ch.WriteWithoutResponse(data) // Link-Power accepts write-with-response; tinygo maps both
	return err
}

func (t *tinygoTransport) ReadChar(uuid string) ([]byte, error) {
	ch, err := t.char(uuid)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 64)
	n, err := ch.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (t *tinygoTransport) Subscribe(uuid string, fn func([]byte)) error {
	ch, err := t.char(uuid)
	if err != nil {
		return err
	}
	return ch.EnableNotifications(fn)
}

func (t *tinygoTransport) Disconnected() <-chan struct{} { return t.disc }

func (t *tinygoTransport) Close() error {
	err := t.dev.Disconnect()
	if err != nil {
		// Surface it: the link may still be up, which blocks pairing scans
		// until the peripheral itself drops it.
		log.Printf("wattline: BLE disconnect failed: %v", err)
	}
	// The connect handler normally closes t.disc, but guard against a missed
	// event so a paused connector can never hang waiting on the channel.
	t.once.Do(func() { close(t.disc) })
	return err
}
