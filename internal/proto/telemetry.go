package proto

import (
	"encoding/binary"
	"fmt"
)

type Battery struct {
	Enabled   bool    `json:"enabled"`
	Status    int8    `json:"status"`
	Full      bool    `json:"full"`
	MaxWh     float64 `json:"max_wh"`
	Wh        float64 `json:"wh"`
	Level     uint8   `json:"level"`
	Volts     float64 `json:"volts"`
	Amps      float64 `json:"amps"`
	Watts     float64 `json:"watts"`
	RemainMin uint16  `json:"remain_min"`
}

type DCPort struct {
	Enabled bool    `json:"enabled"`
	Status  int8    `json:"status"`
	Volts   float64 `json:"volts"`
	Amps    float64 `json:"amps"`
	Watts   float64 `json:"watts"`
	Bypass  bool    `json:"bypass"`
}

type TypeCPort struct {
	Enabled bool    `json:"enabled"`
	Status  int8    `json:"status"`
	Volts   float64 `json:"volts"`
	Amps    float64 `json:"amps"`
	Watts   float64 `json:"watts"`
	TempC   float64 `json:"temp_c"`
	Mode    uint8   `json:"mode"`
	DCInput bool    `json:"dc_input"`
}

func sfloatAt(b []byte, off int) float64 {
	return DecodeSFloat(binary.LittleEndian.Uint16(b[off:]))
}

// ParseBattery parses ExtBatteryInfo 0x4303 (API.md §4.1, 16 bytes).
func ParseBattery(b []byte) (Battery, error) {
	if len(b) < 16 {
		return Battery{}, fmt.Errorf("battery frame too short: %d bytes", len(b))
	}
	return Battery{
		Enabled: b[0] == 1, Status: int8(b[1]), Full: b[2] == 1,
		MaxWh: sfloatAt(b, 3), Wh: sfloatAt(b, 5), Level: b[7],
		Volts: sfloatAt(b, 8), Amps: sfloatAt(b, 10), Watts: sfloatAt(b, 12),
		RemainMin: binary.LittleEndian.Uint16(b[14:]),
	}, nil
}

// ParseDC parses DcPortStatus 0x4304 (API.md §4.2, 8–11 bytes on fw 1.4.9).
func ParseDC(b []byte) (DCPort, error) {
	if len(b) < 8 {
		return DCPort{}, fmt.Errorf("dc frame too short: %d bytes", len(b))
	}
	d := DCPort{
		Enabled: b[0] == 1, Status: int8(b[1]),
		Volts: sfloatAt(b, 2), Amps: sfloatAt(b, 4), Watts: sfloatAt(b, 6),
	}
	if len(b) >= 9 {
		d.Bypass = b[8] == 1
	}
	return d, nil
}

// ParseTypeC parses TypeCPortStatus 0x4305 (API.md §4.3, 10–13 bytes).
func ParseTypeC(b []byte) (TypeCPort, error) {
	if len(b) < 10 {
		return TypeCPort{}, fmt.Errorf("typec frame too short: %d bytes", len(b))
	}
	c := TypeCPort{
		Enabled: b[0] == 1, Status: int8(b[1]),
		Volts: sfloatAt(b, 2), Amps: sfloatAt(b, 4), Watts: sfloatAt(b, 6),
		TempC: sfloatAt(b, 8),
	}
	if len(b) >= 12 {
		c.Mode = b[11]
	}
	if len(b) >= 13 {
		c.DCInput = b[12] == 1
	}
	return c, nil
}
