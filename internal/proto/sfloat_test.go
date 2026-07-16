package proto

import (
	"math"
	"testing"
)

func TestDecodeSFloat(t *testing.T) {
	cases := []struct {
		raw  uint16
		want float64
	}{
		{0xf0d0, 20.8},    // battery voltage
		{0xa7f3, 0.002035}, // battery current
		{0xc1a8, 0.0424},  // battery power
		{0xe7a7, 19.59},   // DC voltage
		{0xb113, 0.00275}, // DC current
		{0xc21b, 0.0539},  // DC power
		{0xf0fa, 25.0},    // TypeC temperature
		{0xe7d0, 20.0},    // bypass threshold
		{0xf3de, 99.0},    // battery capacity Wh
		{0x0000, 0.0},
	}
	for _, c := range cases {
		if got := DecodeSFloat(c.raw); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("DecodeSFloat(%#04x) = %v, want %v", c.raw, got, c.want)
		}
	}
	for _, special := range []uint16{0x07FF, 0x0800, 0x07FE, 0x0802} {
		if got := DecodeSFloat(special); !math.IsNaN(got) {
			t.Errorf("DecodeSFloat(%#04x) = %v, want NaN", special, got)
		}
	}
}
