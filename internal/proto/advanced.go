package proto

import (
	"encoding/binary"
	"fmt"
)

const (
	cmdBarrierFree = 0x03
	cmdRunningMode = 0xe0
	cmdUSBFirmware = 0x17
)

// FeatureSet is the decoded command-channel FEATURES bitmask.
type FeatureSet struct {
	Display, FactoryMode, Sleep, Shutdown, BatteryCapacity bool
	DCOutPort, DCOutControl, DCOutScheduler                bool
	USBPort, USBPowerLimit, USBOutputControl               bool
	DCBypass, DCBypassControl, USBDCInput, USBDCInputPower bool
}

// DecodeFeatures maps the documented feature bits 0 through 14.
func DecodeFeatures(raw uint32) FeatureSet {
	set := func(bit uint) bool { return raw&(1<<bit) != 0 }
	return FeatureSet{
		Display: set(0), FactoryMode: set(1), Sleep: set(2), Shutdown: set(3), BatteryCapacity: set(4),
		DCOutPort: set(5), DCOutControl: set(6), DCOutScheduler: set(7),
		USBPort: set(8), USBPowerLimit: set(9), USBOutputControl: set(10),
		DCBypass: set(11), DCBypassControl: set(12), USBDCInput: set(13), USBDCInputPower: set(14),
	}
}

func BarrierFreeGet() []byte          { return []byte{cmdBarrierFree, ActGet} }
func BarrierFreeSet(on bool) []byte   { return []byte{cmdBarrierFree, ActSet, onByte(on)} }
func RunningModeSet(mode byte) []byte { return []byte{cmdRunningMode, ActSet, mode} }
func USBFirmwareGet() []byte          { return []byte{cmdUSBFirmware, ActGet} }

// ParseUSBFirmware returns the opaque USB-IC firmware bytes. Their component
// meanings are not documented, so the protocol layer deliberately preserves them.
func ParseUSBFirmware(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("USB firmware payload is empty")
	}
	return append([]byte(nil), payload...), nil
}

// BLEPINSet encodes a six-digit numeric PIN. Invalid values emit no frame.
func BLEPINSet(pin uint32) []byte {
	if pin > 999999 {
		return nil
	}
	b := []byte{CmdBLEPin, ActSet, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[2:], pin)
	return b
}
