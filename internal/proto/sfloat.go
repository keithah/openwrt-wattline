// Package proto implements the PeakDo Link-Power BLE protocol (see API.md).
package proto

import "math"

// DecodeSFloat decodes an IEEE-11073 16-bit SFLOAT (API.md §6).
// Specials (NaN, ±Inf, reserved) decode to NaN.
func DecodeSFloat(raw uint16) float64 {
	switch raw {
	case 0x07FF, 0x0800, 0x07FE, 0x0802:
		return math.NaN()
	}
	mant := int(raw & 0x0FFF)
	if mant&0x0800 != 0 {
		mant -= 0x1000
	}
	exp := int(raw >> 12)
	if exp&0x08 != 0 {
		exp -= 0x10
	}
	return float64(mant) * math.Pow(10, float64(exp))
}
