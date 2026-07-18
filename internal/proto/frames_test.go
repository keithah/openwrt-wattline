package proto

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestBuilders(t *testing.T) {
	cases := []struct{ got, want []byte }{
		{DCControl(true), []byte{0x01, 0x01, 0x01}},
		{DCControl(false), []byte{0x01, 0x01, 0x00}},
		{TypeCOutput(false), []byte{0x13, 0x01, 0x02, 0x00}},
		{BypassControl(true), []byte{0x14, 0x01, 0x01}},
		{Restart(), []byte{0x11, 0x01}},
		{FeaturesQuery(), []byte{0xFE, 0x00}},
		{DeviceIDQuery(), []byte{0x10, 0x00}},
		{OTAInfoQuery(), []byte{0x84}},
		{ShutdownMagic(), []byte{0x46, 0x4D}},
	}
	for i, c := range cases {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("case %d: got % x want % x", i, c.got, c.want)
		}
	}
}

func TestCurrentTime(t *testing.T) {
	// 2026-07-14 21:30:05 local, a Tuesday (day-of-week 2).
	ts := time.Date(2026, 7, 14, 21, 30, 5, 0, time.Local)
	got := CurrentTime(ts)
	want := []byte{0xEA, 0x07, 7, 14, 21, 30, 5, 2, 0, 0} // 2026 LE, m, d, h, m, s, dow, frac, reason
	if !bytes.Equal(got, want) {
		t.Errorf("CurrentTime = % x, want % x", got, want)
	}
	want[9] = 1
	if got := CurrentTimeAt(ts, 1); !bytes.Equal(got, want) {
		t.Errorf("CurrentTimeAt(reason=1) = % x, want % x", got, want)
	}
}

func TestValidateReply(t *testing.T) {
	// Live: DC on → 01 81 00
	res, payload, err := ValidateReply([]byte{0x01, 0x01, 0x01}, []byte{0x01, 0x81, 0x00})
	if err != nil || res != 0 || len(payload) != 0 {
		t.Fatalf("dc on: res=%d payload=%v err=%v", res, payload, err)
	}
	// Live: features → fe 80 00 ff 7f 00 00
	res, payload, err = ValidateReply([]byte{0xFE, 0x00}, []byte{0xFE, 0x80, 0x00, 0xFF, 0x7F, 0x00, 0x00})
	if err != nil || res != 0 || len(payload) != 4 {
		t.Fatalf("features: res=%d payload=%v err=%v", res, payload, err)
	}
	// Echo mismatch (stale reply) is an error.
	if _, _, err = ValidateReply([]byte{0x01, 0x01, 0x01}, []byte{0xFE, 0x80, 0x00}); err == nil {
		t.Fatal("expected echo-mismatch error")
	}
	// Non-zero result is ErrResult...
	_, _, err = ValidateReply([]byte{0x04, 0x00}, []byte{0x04, 0x80, 0xFC})
	if !errors.Is(err, ErrResult) {
		t.Fatalf("expected ErrResult, got %v", err)
	}
	// ...except for bypass (live: 14 81 ff yet toggle worked).
	res, _, err = ValidateReply([]byte{0x14, 0x01, 0x00}, []byte{0x14, 0x81, 0xFF})
	if err != nil || res != 0xFF {
		t.Fatalf("bypass exemption: res=%#x err=%v", res, err)
	}
	if _, _, err := ValidateReply(RunningModeSet(1), []byte{0xe0, 0x81, 0x00}); err != nil {
		t.Fatalf("running-mode SET reply rejected: %v", err)
	}
}

func TestValidateReplyRejectsEveryFrameShorterThanResultHeader(t *testing.T) {
	req := []byte{0x01, 0x01, 0x01}
	for _, reply := range [][]byte{nil, {0x01}, {0x01, 0x81}} {
		if _, _, err := ValidateReply(req, reply); err == nil {
			t.Fatalf("ValidateReply accepted short reply % x", reply)
		}
	}
}

func TestParsers(t *testing.T) {
	feats, err := ParseFeatures([]byte{0xFF, 0x7F, 0x00, 0x00})
	if err != nil || feats != 0x7FFF {
		t.Fatalf("features=%#x err=%v", feats, err)
	}
	// Live: 10 80 00 2b 72 eb 5a 04 dc → DC:04:5A:EB:72:2B
	mac, err := ParseDeviceID([]byte{0x2B, 0x72, 0xEB, 0x5A, 0x04, 0xDC})
	if err != nil || mac != "DC:04:5A:EB:72:2B" {
		t.Fatalf("mac=%q err=%v", mac, err)
	}
	// Live app-mode OTA INFO: 01 00...00 05 03
	info := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x05, 0x03}
	mode, cid, err := ParseOTAMode(info)
	if err != nil || mode != 1 || cid != 0x0305 {
		t.Fatalf("mode=%d cid=%#x err=%v", mode, cid, err)
	}
}
