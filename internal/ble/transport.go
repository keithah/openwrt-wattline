// Package ble holds the Link-Power BLE session and transports.
package ble

import "errors"

const (
	CharOTA     = "00004301-0000-1000-8000-00805f9b34fb"
	CharCmd     = "00004302-0000-1000-8000-00805f9b34fb"
	CharBattery = "00004303-0000-1000-8000-00805f9b34fb"
	CharDC      = "00004304-0000-1000-8000-00805f9b34fb"
	CharTypeC   = "00004305-0000-1000-8000-00805f9b34fb"
	CharFactory = "00004310-0000-1000-8000-00805f9b34fb"
	CharModel   = "00002a24-0000-1000-8000-00805f9b34fb"
	CharFWRev   = "00002a26-0000-1000-8000-00805f9b34fb"
	CharHWRev   = "00002a27-0000-1000-8000-00805f9b34fb"
	CharSWRev   = "00002a28-0000-1000-8000-00805f9b34fb"
	CharTime    = "00002a2b-0000-1000-8000-00805f9b34fb"
)

// ErrBootloader means the device is in OTA/firmware-update mode; leave it alone.
var ErrBootloader = errors.New("device is in bootloader (OTA) mode")

// Transport is one live BLE connection. Implemented by tinygoTransport
// (production) and fakeTransport (tests).
type Transport interface {
	WriteChar(uuid string, data []byte) error
	ReadChar(uuid string) ([]byte, error)
	Subscribe(uuid string, fn func([]byte)) error
	// HasChar consults the characteristic inventory discovered at connect time.
	// It must not perform GATT I/O.
	HasChar(uuid string) bool
	Disconnected() <-chan struct{}
	// Close drops the connection, freeing the device (which accepts a single
	// central) for pairing or other clients. Disconnected() fires afterwards.
	Close() error
}
