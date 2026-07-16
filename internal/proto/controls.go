package proto

import (
	"encoding/binary"
	"fmt"
)

const (
	CmdTypeCLimit      = 0x02
	CmdScheduledOnOff  = 0x06
	CmdBypassThreshold = 0x15

	ActDel = 0x02

	// USB-C power-limit types (API.md §3.4). Runtime (4) is read-only.
	LimitGlobal  = 1
	LimitInput   = 2
	LimitOutput  = 3
	LimitRuntime = 4
)

// levelWatts maps the device's USB-C power-limit level index to watts
// (API.md §3.4). Index -1 means "unset".
var levelWatts = []int{30, 45, 60, 65, 100, 140}

// LevelToWatts converts a power-limit level (0..5) to watts; -1/unknown → 0.
func LevelToWatts(level int) int {
	if level >= 0 && level < len(levelWatts) {
		return levelWatts[level]
	}
	return 0
}

// WattsToLevel converts a watts value to the nearest exact level; -1 if none.
func WattsToLevel(watts int) int {
	for i, w := range levelWatts {
		if w == watts {
			return i
		}
	}
	return -1
}

// TypeCLimitGet builds a get for a limit type (1=global,2=input,3=output,4=runtime).
func TypeCLimitGet(typ byte) []byte { return []byte{CmdTypeCLimit, ActGet, typ} }

// TypeCLimitSet builds a set; level -1 is encoded as 0xFF.
func TypeCLimitSet(typ byte, level int) []byte {
	return []byte{CmdTypeCLimit, ActSet, typ, byte(level)}
}

// TypeCLimitClear resets a limit type to the device default.
func TypeCLimitClear(typ byte) []byte { return []byte{CmdTypeCLimit, ActDel, typ} }

// ParseTypeCLimit reads the level from a get reply payload (post-ValidateReply).
func ParseTypeCLimit(payload []byte) (level int, err error) {
	if len(payload) < 1 {
		return 0, fmt.Errorf("typec limit payload too short: % x", payload)
	}
	return int(int8(payload[0])), nil
}

// BypassThresholdGet builds a DC bypass-threshold read.
func BypassThresholdGet() []byte { return []byte{CmdBypassThreshold, ActGet} }

// BypassThresholdSet builds a set with the voltage as a little-endian SFLOAT.
func BypassThresholdSet(volts float64) []byte {
	b := []byte{CmdBypassThreshold, ActSet, 0, 0}
	binary.LittleEndian.PutUint16(b[2:], EncodeSFloat(volts))
	return b
}

// ParseBypassThreshold decodes the SFLOAT volts from a get reply payload.
func ParseBypassThreshold(payload []byte) (float64, error) {
	if len(payload) < 2 {
		return 0, fmt.Errorf("bypass threshold payload too short: % x", payload)
	}
	return DecodeSFloat(binary.LittleEndian.Uint16(payload)), nil
}
