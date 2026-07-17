package proto

import (
	"bytes"
	"testing"
)

func TestLifecycleFrames(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"ota enter", OTAEnter(), []byte{'P', 'K'}},
		{"ota exit", OTAExit(), []byte{0x83}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Fatalf("got % x, want % x", tt.got, tt.want)
			}
		})
	}
}

func TestLifecycleOTAInfo(t *testing.T) {
	app := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x05, 0x03}
	got, err := ParseOTAInfo(app)
	if err != nil || got.Mode != 1 || got.CID != 0x0305 || got.Revision != 0 {
		t.Fatalf("app info = %+v, %v", got, err)
	}
	boot := []byte{0x02, 0x00, 0x10, 0x00, 0x00, 0x00, 0x10, 0x83, 0x00, 0x00, 0x00, 0x04, 0x00, 0x05, 0x03, 0x01, 0, 0, 0, 0}
	got, err = ParseOTAInfo(boot)
	if err != nil || got != (OTAInfo{Mode: 2, OTAStart: 0x1000, BlockSize: 0x1000, ChipType: 0x83, AppStart: 0x40000, CID: 0x0305, Revision: 1}) {
		t.Fatalf("boot info = %+v, %v", got, err)
	}
	for _, bad := range [][]byte{nil, {1}, {2, 0, 0}} {
		if _, err := ParseOTAInfo(bad); err == nil {
			t.Errorf("short info accepted: % x", bad)
		}
	}
}
