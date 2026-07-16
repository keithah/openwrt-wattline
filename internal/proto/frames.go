package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	CmdDCControl     = 0x01
	CmdBLEPin        = 0x04
	CmdDeviceID      = 0x10
	CmdRestart       = 0x11
	CmdTypeCControl  = 0x13
	CmdBypassControl = 0x14
	CmdFeatures      = 0xFE

	ActGet = 0x00
	ActSet = 0x01
)

// ErrResult wraps a non-zero device result code (API.md §3.3 table).
var ErrResult = errors.New("device returned error result")

func onByte(on bool) byte {
	if on {
		return 1
	}
	return 0
}

func DCControl(on bool) []byte     { return []byte{CmdDCControl, ActSet, onByte(on)} }
func TypeCOutput(on bool) []byte   { return []byte{CmdTypeCControl, ActSet, 0x02, onByte(on)} }
func BypassControl(on bool) []byte { return []byte{CmdBypassControl, ActSet, onByte(on)} }
func Restart() []byte              { return []byte{CmdRestart, ActSet} }
func FeaturesQuery() []byte        { return []byte{CmdFeatures, ActGet} }
func DeviceIDQuery() []byte        { return []byte{CmdDeviceID, ActGet} }
func OTAInfoQuery() []byte         { return []byte{0x84} }
func ShutdownMagic() []byte        { return []byte{'F', 'M'} }

// CurrentTime encodes the 10-byte Current Time write (API.md §5).
func CurrentTime(t time.Time) []byte {
	dow := byte(t.Weekday()) // Sunday=0
	if dow == 0 {
		dow = 7 // BLE: Mon=1..Sun=7
	}
	b := make([]byte, 10)
	binary.LittleEndian.PutUint16(b, uint16(t.Year()))
	b[2], b[3], b[4], b[5], b[6] = byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	b[7] = dow
	return b
}

// ValidateReply checks echo/action and result (API.md §3.1). Bypass replies
// carry non-zero results even on success (live-verified) so 0x14 is exempt
// from result failure; callers reconcile from telemetry instead.
func ValidateReply(req, reply []byte) (result byte, payload []byte, err error) {
	if len(req) == 0 || len(reply) < 2 {
		return 0, nil, fmt.Errorf("short frame: req=% x reply=% x", req, reply)
	}
	if reply[0] != req[0] {
		return 0, nil, fmt.Errorf("echo mismatch (stale reply?): sent %#02x got %#02x", req[0], reply[0])
	}
	if len(req) >= 2 && reply[1] != req[1]|0x80 {
		return 0, nil, fmt.Errorf("action echo mismatch: sent %#02x got %#02x", req[1], reply[1])
	}
	if len(reply) < 3 {
		return 0, nil, nil
	}
	result = reply[2]
	payload = reply[3:]
	if result != 0 && req[0] != CmdBypassControl {
		return result, payload, fmt.Errorf("cmd %#02x result %#02x: %w", req[0], result, ErrResult)
	}
	if req[0] == CmdBypassControl {
		return result, payload, nil
	}
	return 0, payload, nil
}

func ParseFeatures(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("features payload too short: % x", payload)
	}
	return binary.LittleEndian.Uint32(payload), nil
}

// ParseDeviceID converts the 6 reversed MAC bytes to canonical form.
func ParseDeviceID(payload []byte) (string, error) {
	if len(payload) < 6 {
		return "", fmt.Errorf("device id payload too short: % x", payload)
	}
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
		payload[5], payload[4], payload[3], payload[2], payload[1], payload[0]), nil
}

// ParseOTAMode parses the OTA INFO reply (API.md §9.1): mode byte + CID at 13.
func ParseOTAMode(reply []byte) (mode byte, cid uint16, err error) {
	if len(reply) < 1 {
		return 0, 0, errors.New("empty OTA INFO reply")
	}
	mode = reply[0]
	if len(reply) >= 15 {
		cid = binary.LittleEndian.Uint16(reply[13:])
	}
	return mode, cid, nil
}
