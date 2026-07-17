package ble

import (
	"fmt"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

func (s *Session) barrierFreeLocked() (bool, error) {
	_, payload, err := s.commandLocked(proto.BarrierFreeGet())
	if err != nil {
		return false, err
	}
	if len(payload) < 1 || payload[0] > 1 {
		return false, fmt.Errorf("invalid barrier-free payload: % x", payload)
	}
	return payload[0] == 1, nil
}

func (s *Session) BarrierFree() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.barrierFreeLocked()
}

func (s *Session) SetBarrierFree(on bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.commandLocked(proto.BarrierFreeSet(on)); err != nil {
		return false, err
	}
	return s.barrierFreeLocked()
}

func (s *Session) SetRunningMode(mode byte) error {
	_, _, err := s.command(proto.RunningModeSet(mode))
	return err
}

func (s *Session) USBFirmwareVersion() ([]byte, error) {
	_, payload, err := s.command(proto.USBFirmwareGet())
	if err != nil {
		return nil, err
	}
	return proto.ParseUSBFirmware(payload)
}

func (s *Session) SetBLEPIN(pin uint32) error {
	req := proto.BLEPINSet(pin)
	if len(req) == 0 {
		return fmt.Errorf("BLE PIN %d is outside 0..999999", pin)
	}
	_, _, err := s.command(req)
	return err
}
