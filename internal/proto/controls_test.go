package proto

import (
	"math"
	"testing"
)

func TestEncodeSFloatRoundTrip(t *testing.T) {
	for _, v := range []float64{20.0, 19.6, 24.0, 5.5, 0.0, 12.34} {
		raw := EncodeSFloat(v)
		got := DecodeSFloat(raw)
		if math.Abs(got-v) > 0.05 {
			t.Errorf("SFloat round-trip %.2f -> %#04x -> %.3f", v, raw, got)
		}
	}
	// Known device value: 20.00 V is 0xE7D0 (API.md §3.4).
	if raw := EncodeSFloat(20.0); raw != 0xE7D0 {
		t.Errorf("EncodeSFloat(20.0) = %#04x, want 0xE7D0", raw)
	}
}

func TestTypeCLimitFrames(t *testing.T) {
	if got := TypeCLimitGet(2); string(got) != string([]byte{0x02, 0x00, 0x02}) {
		t.Errorf("get = % x", got)
	}
	if got := TypeCLimitSet(1, 4); string(got) != string([]byte{0x02, 0x01, 0x01, 0x04}) {
		t.Errorf("set = % x", got)
	}
	// level -1 encodes as 0xFF
	if got := TypeCLimitSet(3, -1); got[3] != 0xFF {
		t.Errorf("set(-1) level byte = %#02x", got[3])
	}
	if got := TypeCLimitClear(2); string(got) != string([]byte{0x02, 0x02, 0x02}) {
		t.Errorf("clear = % x", got)
	}
}

func TestLevelWattsMapping(t *testing.T) {
	cases := map[int]int{0: 30, 1: 45, 2: 60, 3: 65, 4: 100, 5: 140}
	for lvl, w := range cases {
		if got := LevelToWatts(lvl); got != w {
			t.Errorf("LevelToWatts(%d) = %d, want %d", lvl, got, w)
		}
		if got := WattsToLevel(w); got != lvl {
			t.Errorf("WattsToLevel(%d) = %d, want %d", w, got, lvl)
		}
	}
	if LevelToWatts(-1) != 0 {
		t.Error("unset level should map to 0 watts")
	}
	if WattsToLevel(999) != -1 {
		t.Error("unknown watts should map to -1")
	}
}

func TestParseTypeCLimit(t *testing.T) {
	// get reply payload after ValidateReply strips [cmd,act,result]: [level]
	if lvl, err := ParseTypeCLimit([]byte{0x04}); err != nil || lvl != 4 {
		t.Errorf("ParseTypeCLimit = %d, %v", lvl, err)
	}
	if _, err := ParseTypeCLimit(nil); err == nil {
		t.Error("empty payload should error")
	}
}

func TestBypassThresholdFrames(t *testing.T) {
	if got := BypassThresholdGet(); string(got) != string([]byte{0x15, 0x00}) {
		t.Errorf("get = % x", got)
	}
	set := BypassThresholdSet(20.0)
	if len(set) != 4 || set[0] != 0x15 || set[1] != 0x01 {
		t.Fatalf("set = % x", set)
	}
	// bytes 2-3 are SFLOAT LE of 20.0 = 0xE7D0 -> D0 E7
	if set[2] != 0xD0 || set[3] != 0xE7 {
		t.Errorf("set sfloat bytes = % x, want d0 e7", set[2:])
	}
	if v, err := ParseBypassThreshold([]byte{0xD0, 0xE7}); err != nil || math.Abs(v-20.0) > 0.01 {
		t.Errorf("ParseBypassThreshold = %v, %v", v, err)
	}
	if _, err := ParseBypassThreshold([]byte{0x01}); err == nil {
		t.Error("short payload should error")
	}
}
