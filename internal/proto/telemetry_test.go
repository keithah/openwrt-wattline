package proto

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseBattery(t *testing.T) {
	b, err := ParseBattery(mustHex(t, "010001def3def364d0f0f3a7a8c10000"))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Enabled || b.Status != 0 || !b.Full || b.Level != 100 || b.RemainMin != 0 {
		t.Fatalf("flags: %+v", b)
	}
	for _, c := range []struct{ got, want float64 }{
		{b.MaxWh, 99.0}, {b.Wh, 99.0}, {b.Volts, 20.8}, {b.Amps, 0.002035}, {b.Watts, 0.0424},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("got %v want %v", c.got, c.want)
		}
	}
	if _, err := ParseBattery([]byte{1, 2, 3}); err == nil {
		t.Fatal("short frame must error")
	}
}

func TestParseDC(t *testing.T) {
	// 11-byte fw 1.4.9 frame: trailing undocumented bytes must be ignored.
	d, err := ParseDC(mustHex(t, "0100a7e713b11bc201007f"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Enabled || d.Status != 0 || !d.Bypass {
		t.Fatalf("flags: %+v", d)
	}
	if math.Abs(d.Volts-19.59) > 1e-9 || math.Abs(d.Amps-0.00275) > 1e-9 || math.Abs(d.Watts-0.0539) > 1e-9 {
		t.Fatalf("values: %+v", d)
	}
	// 8-byte legacy frame: no bypass field.
	d8, err := ParseDC(mustHex(t, "0100a7e713b11bc2"))
	if err != nil || d8.Bypass {
		t.Fatalf("8-byte: %+v err=%v", d8, err)
	}
}

func TestParseTypeC(t *testing.T) {
	c, err := ParseTypeC(mustHex(t, "0100000000000000faf0000300"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Mode != 3 || c.DCInput || math.Abs(c.TempC-25.0) > 1e-9 {
		t.Fatalf("%+v", c)
	}
	// status -1 = discharging (signed int8)
	c2, err := ParseTypeC(mustHex(t, "01ff000000000000faf0000300"))
	if err != nil || c2.Status != -1 {
		t.Fatalf("status: %+v err=%v", c2, err)
	}
}

// TestTelemetryJSONTags verifies the API emits snake_case field names that
// match what the LuCI status.js reads, not the Go exported field names.
func TestTelemetryJSONTags(t *testing.T) {
	b, err := json.Marshal(Battery{Level: 42, Watts: 12.5, RemainMin: 30})
	if err != nil {
		t.Fatal(err)
	}
	battery := string(b)
	for _, want := range []string{`"level"`, `"watts"`, `"remain_min"`, `"max_wh"`, `"wh"`} {
		if !strings.Contains(battery, want) {
			t.Fatalf("Battery JSON missing %s: %s", want, battery)
		}
	}
	for _, bad := range []string{`"Level"`, `"Watts"`, `"TempC"`, `"RemainMin"`} {
		if strings.Contains(battery, bad) {
			t.Fatalf("Battery JSON has un-tagged Go field name %s: %s", bad, battery)
		}
	}

	d, err := json.Marshal(DCPort{Watts: 5, Bypass: true})
	if err != nil {
		t.Fatal(err)
	}
	dc := string(d)
	for _, want := range []string{`"watts"`, `"bypass"`} {
		if !strings.Contains(dc, want) {
			t.Fatalf("DCPort JSON missing %s: %s", want, dc)
		}
	}
	if strings.Contains(dc, `"Bypass"`) {
		t.Fatalf("DCPort JSON has un-tagged Go field name: %s", dc)
	}

	c, err := json.Marshal(TypeCPort{Watts: 5, TempC: 33.2, DCInput: true})
	if err != nil {
		t.Fatal(err)
	}
	typec := string(c)
	for _, want := range []string{`"watts"`, `"temp_c"`, `"dc_input"`} {
		if !strings.Contains(typec, want) {
			t.Fatalf("TypeCPort JSON missing %s: %s", want, typec)
		}
	}
	for _, bad := range []string{`"TempC"`, `"DCInput"`} {
		if strings.Contains(typec, bad) {
			t.Fatalf("TypeCPort JSON has un-tagged Go field name %s: %s", bad, typec)
		}
	}
}
