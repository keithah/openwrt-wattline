package ble

import (
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
	result, payload, err := s.command(proto.TypeCLimitGet(byte(typ)))
	if result == 0xFF {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	return proto.ParseTypeCLimit(payload)
}

// SetUSBCLimit sets a limit type to a level (0..5), or -1 to send "unset".
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
func (s *Session) BypassThreshold() (float64, error) {
	_, payload, err := s.command(proto.BypassThresholdGet())
	if err != nil {
		return 0, err
	}
	return proto.ParseBypassThreshold(payload)
}

// SetBypassThreshold sets the DC bypass engage voltage.
func (s *Session) SetBypassThreshold(volts float64) error {
	_, _, err := s.command(proto.BypassThresholdSet(volts))
	return err
}

// Schedules lists all on-device timers with their settings.
func (s *Session) Schedules() ([]proto.Timer, error) {
	_, payload, err := s.command(proto.ScheduleList())
	if err != nil {
		return nil, err
	}
	ids := proto.ParseScheduleIDs(payload)
	out := make([]proto.Timer, 0, len(ids))
	for _, id := range ids {
		_, p, err := s.command(proto.ScheduleGet(id))
		if err != nil {
			return nil, err
		}
		// The get reply carries the timer id at payload[0]; the 9-byte
		// TIMER_SETTINGS struct starts at payload[1] (API.md §3.4:
		// "struct starting at byte 4" of the reply frame).
		if len(p) < 1 {
			return nil, fmt.Errorf("schedule %d: empty get reply", id)
		}
		tm, err := proto.ParseTimer(p[1:])
		if err != nil {
			return nil, err
		}
		tm.ID = id
		out = append(out, tm)
	}
	return out, nil
}

// UpsertSchedule adds (id 0xFF) or edits a timer, returning its id.
func (s *Session) UpsertSchedule(id byte, t proto.Timer) (byte, error) {
	_, payload, err := s.command(proto.ScheduleUpsert(id, t))
	if err != nil {
		return 0, err
	}
	return proto.ParsedUpsertID(payload, id), nil
}

// DeleteSchedule removes a timer by id.
func (s *Session) DeleteSchedule(id byte) error {
	_, _, err := s.command(proto.ScheduleDelete(id))
	return err
}
