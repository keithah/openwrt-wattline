package ble

import (
	"errors"
	"fmt"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

// This file adds the device settings/controls beyond the rule actions:
// USB-C power limit, DC bypass threshold, and on-device schedules. Each is a
// write-then-read command transaction on 0x4302 (API.md §3.4).

// USBCLimit returns the power-limit level (0..5) for a type
// (proto.LimitGlobal/Input/Output/Runtime), or -1 when unset (the device
// answers 0xFF, seen for runtime with no PD sink attached).
func (s *Session) USBCLimit(typ int) (int, error) {
	return s.GetUSBCLimit(typ)
}

func (s *Session) getUSBCLimitLocked(typ int) (int, error) {
	result, payload, err := s.commandLocked(proto.TypeCLimitGet(byte(typ)))
	if result == 0xFF {
		if typ == proto.LimitRuntime {
			return -1, nil
		}
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	return proto.ParseTypeCLimit(payload)
}

func (s *Session) GetUSBCLimit(typ int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getUSBCLimitLocked(typ)
}

func (s *Session) PutUSBCLimit(typ, level int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.commandLocked(proto.TypeCLimitSet(byte(typ), level)); err != nil {
		return 0, err
	}
	return s.getUSBCLimitLocked(typ)
}

func (s *Session) DeleteUSBCLimit(typ int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.commandLocked(proto.TypeCLimitClear(byte(typ))); err != nil {
		return 0, err
	}
	return s.getUSBCLimitLocked(typ)
}

// SetUSBCLimit sets a mutable limit type to a level (0..5). Invalid types,
// runtime writes, and levels outside 0..5 are rejected locally.
func (s *Session) SetUSBCLimit(typ, level int) error {
	_, _, err := s.command(proto.TypeCLimitSet(byte(typ), level))
	return err
}

// ClearUSBCLimit resets a limit type to the device default.
func (s *Session) ClearUSBCLimit(typ int) error {
	_, _, err := s.command(proto.TypeCLimitClear(byte(typ)))
	return err
}

// BypassThreshold returns the DC bypass engage voltage.
func (s *Session) bypassThresholdLocked() (float64, error) {
	_, payload, err := s.commandLocked(proto.BypassThresholdGet())
	if err != nil {
		return 0, err
	}
	return proto.ParseBypassThreshold(payload)
}

func (s *Session) BypassThreshold() (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bypassThresholdLocked()
}

// SetBypassThreshold sets the DC bypass engage voltage.
func (s *Session) SetBypassThreshold(volts float64) error {
	_, _, err := s.command(proto.BypassThresholdSet(volts))
	return err
}

// PutBypassThreshold sets the threshold and returns the authoritative device
// re-read while retaining command-channel ownership across both operations.
func (s *Session) PutBypassThreshold(volts float64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.commandLocked(proto.BypassThresholdSet(volts)); err != nil {
		return 0, err
	}
	return s.bypassThresholdLocked()
}

// Schedules lists all on-device timers with their settings.
func (s *Session) Schedules() ([]proto.Timer, error) {
	return s.ListTimers()
}

func (s *Session) listTimersLocked() ([]proto.Timer, error) {
	ids, err := s.timerIDsLocked()
	if err != nil {
		return nil, err
	}
	out := make([]proto.Timer, 0, len(ids))
	for _, id := range ids {
		timer, err := s.getTimerLocked(id)
		if err != nil {
			return nil, err
		}
		out = append(out, timer)
	}
	return out, nil
}

func (s *Session) timerIDsLocked() ([]byte, error) {
	_, payload, err := s.commandLocked(proto.ScheduleList())
	if err != nil {
		return nil, err
	}
	ids, err := strictScheduleIDs(payload)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Session) getTimerLocked(id byte) (proto.Timer, error) {
	_, payload, err := s.commandLocked(proto.ScheduleGet(id))
	if err != nil {
		if errors.Is(err, proto.ErrResult) {
			return proto.Timer{}, fmt.Errorf("%w: %v", proto.ErrTimerNotFound, err)
		}
		return proto.Timer{}, err
	}
	if len(payload) < 10 {
		return proto.Timer{}, fmt.Errorf("schedule %d: GET payload too short: % x", id, payload)
	}
	if payload[0] != id {
		return proto.Timer{}, fmt.Errorf("schedule %d: GET reply ID mismatch: got %d", id, payload[0])
	}
	timer, err := proto.ParseTimer(payload[1:10])
	if err != nil {
		return proto.Timer{}, err
	}
	timer.ID = id
	return timer, nil
}

func (s *Session) GetTimer(id byte) (proto.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getTimerLocked(id)
}

func hasTimerID(ids []byte, id byte) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func strictScheduleIDs(payload []byte) ([]byte, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("schedule list payload is empty")
	}
	count := int(payload[0])
	if len(payload) < 1+count {
		return nil, fmt.Errorf("schedule list declares %d IDs but contains %d", count, len(payload)-1)
	}
	return append([]byte(nil), payload[1:1+count]...), nil
}

func strictUpsertID(payload []byte, requested byte) (byte, error) {
	if len(payload) >= 1 {
		return payload[0], nil
	}
	if requested == 0xff {
		return 0, fmt.Errorf("schedule add reply is missing assigned ID")
	}
	return requested, nil
}

func (s *Session) ListTimers() ([]proto.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTimersLocked()
}

func (s *Session) AddTimer(t proto.Timer) ([]proto.Timer, byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, payload, err := s.commandLocked(proto.ScheduleUpsert(0xff, t))
	if err != nil {
		return nil, 0, err
	}
	id, err := strictUpsertID(payload, 0xff)
	if err != nil {
		return nil, 0, err
	}
	timers, err := s.listTimersLocked()
	return timers, id, err
}

func (s *Session) PutTimer(id byte, t proto.Timer) ([]proto.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.timerIDsLocked()
	if err != nil {
		return nil, err
	}
	if !hasTimerID(ids, id) {
		return nil, proto.ErrTimerNotFound
	}
	if _, _, err := s.commandLocked(proto.ScheduleUpsert(id, t)); err != nil {
		return nil, err
	}
	return s.listTimersLocked()
}

func (s *Session) DeleteTimer(id byte) ([]proto.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.timerIDsLocked()
	if err != nil {
		return nil, err
	}
	if !hasTimerID(ids, id) {
		return nil, proto.ErrTimerNotFound
	}
	if _, _, err := s.commandLocked(proto.ScheduleDelete(id)); err != nil {
		return nil, err
	}
	return s.listTimersLocked()
}

// UpsertSchedule adds (id 0xFF) or edits a timer, returning its id.
func (s *Session) UpsertSchedule(id byte, t proto.Timer) (byte, error) {
	_, payload, err := s.command(proto.ScheduleUpsert(id, t))
	if err != nil {
		return 0, err
	}
	return strictUpsertID(payload, id)
}

// DeleteSchedule removes a timer by id.
func (s *Session) DeleteSchedule(id byte) error {
	_, _, err := s.command(proto.ScheduleDelete(id))
	return err
}
