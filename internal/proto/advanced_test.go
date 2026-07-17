package proto

import (
	"bytes"
	"testing"
)

func TestAdvancedFrames(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"barrier get", BarrierFreeGet(), []byte{0x03, 0x00}},
		{"barrier on", BarrierFreeSet(true), []byte{0x03, 0x01, 0x01}},
		{"pin 020555", BLEPINSet(20555), []byte{0x04, 0x01, 0x4b, 0x50, 0x00, 0x00}},
		{"running factory", RunningModeSet(1), []byte{0xe0, 0x01, 0x01}},
		{"usb firmware", USBFirmwareGet(), []byte{0x17, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Fatalf("got % x, want % x", tt.got, tt.want)
			}
		})
	}

	if got := BLEPINSet(1_000_000); got != nil {
		t.Fatalf("oversized PIN emitted % x", got)
	}
	firmware := []byte{1, 0, 0, 5}
	got, err := ParseUSBFirmware(firmware)
	if err != nil || !bytes.Equal(got, firmware) {
		t.Fatalf("ParseUSBFirmware = % x, %v", got, err)
	}
	got[0] = 9
	if firmware[0] != 1 {
		t.Fatal("ParseUSBFirmware returned aliased storage")
	}
	if _, err := ParseUSBFirmware(nil); err == nil {
		t.Fatal("empty USB firmware payload accepted")
	}
}

func TestFeatureBits(t *testing.T) {
	checks := []func(FeatureSet) bool{
		func(f FeatureSet) bool { return f.Display },
		func(f FeatureSet) bool { return f.FactoryMode },
		func(f FeatureSet) bool { return f.Sleep },
		func(f FeatureSet) bool { return f.Shutdown },
		func(f FeatureSet) bool { return f.BatteryCapacity },
		func(f FeatureSet) bool { return f.DCOutPort },
		func(f FeatureSet) bool { return f.DCOutControl },
		func(f FeatureSet) bool { return f.DCOutScheduler },
		func(f FeatureSet) bool { return f.USBPort },
		func(f FeatureSet) bool { return f.USBPowerLimit },
		func(f FeatureSet) bool { return f.USBOutputControl },
		func(f FeatureSet) bool { return f.DCBypass },
		func(f FeatureSet) bool { return f.DCBypassControl },
		func(f FeatureSet) bool { return f.USBDCInput },
		func(f FeatureSet) bool { return f.USBDCInputPower },
	}
	for bit, check := range checks {
		feature := DecodeFeatures(1 << bit)
		if !check(feature) {
			t.Errorf("bit %d was not decoded", bit)
		}
	}
}
