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

// EncodeSFloat encodes a value as an IEEE-11073 16-bit SFLOAT, choosing the
// smallest exponent that keeps the mantissa within the signed 12-bit range
// (so it preserves as much precision as the format allows). Used for the DC
// bypass threshold (API.md §3.4). NaN encodes to the SFLOAT NaN (0x07FF).
func EncodeSFloat(v float64) uint16 {
	if math.IsNaN(v) {
		return 0x07FF
	}
	for exp := -2; exp <= 7; exp++ {
		m := math.Round(v / math.Pow(10, float64(exp)))
		if m >= -2048 && m <= 2047 {
			return uint16((exp&0x0F)<<12) | uint16(int(m)&0x0FFF)
		}
	}
	return 0x07FF
}
