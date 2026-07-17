package ble

import (
	"bytes"
	"reflect"
	"testing"
)

func TestAdvancedOperationsUseExactFramesAndReadBack(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "03800001")
	if got, err := s.BarrierFree(); err != nil || !got {
		t.Fatalf("BarrierFree = %v, %v", got, err)
	}
	f.push(CharCmd, "038100")
	f.push(CharCmd, "03800000")
	if got, err := s.SetBarrierFree(false); err != nil || got {
		t.Fatalf("SetBarrierFree = %v, %v", got, err)
	}
	f.push(CharCmd, "e08100")
	if err := s.SetRunningMode(1); err != nil {
		t.Fatal(err)
	}
	f.push(CharCmd, "17800001000005")
	if got, err := s.USBFirmwareVersion(); err != nil || !bytes.Equal(got, []byte{1, 0, 0, 5}) {
		t.Fatalf("USBFirmwareVersion = % x, %v", got, err)
	}
	f.push(CharCmd, "048100")
	if err := s.SetBLEPIN(20555); err != nil {
		t.Fatal(err)
	}
	want := []string{"0300", "030100", "0300", "e00101", "1700", "04014b500000"}
	if got := writeFrames(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("advanced frames = %v, want %v", got, want)
	}
}

func TestAdvancedRejectsInvalidPINWithoutIO(t *testing.T) {
	s, f := newCtlSession()
	if err := s.SetBLEPIN(1000000); err == nil {
		t.Fatal("SetBLEPIN accepted seven-digit PIN")
	}
	if len(f.writes) != 0 || f.reads != 0 {
		t.Fatalf("invalid PIN performed I/O: writes=%d reads=%d", len(f.writes), f.reads)
	}
}
