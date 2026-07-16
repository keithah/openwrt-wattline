package ble

import (
	"testing"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func newCtlSession() (*Session, *fakeTransport) {
	f := newFake()
	return NewSession(f, state.NewStore()), f
}

func TestUSBCLimitGetSetUnset(t *testing.T) {
	s, f := newCtlSession()
	// get global -> level 4 (100W): reply [02 80 00 04]
	f.push(CharCmd, "02800004")
	if lvl, err := s.USBCLimit(proto.LimitGlobal); err != nil || lvl != 4 {
		t.Fatalf("USBCLimit = %d, %v", lvl, err)
	}
	// runtime unset -> reply [02 80 ff]
	f.push(CharCmd, "0280ff")
	if lvl, err := s.USBCLimit(proto.LimitRuntime); err != nil || lvl != -1 {
		t.Fatalf("unset USBCLimit = %d, %v (want -1,nil)", lvl, err)
	}
	// set output to 5 -> reply [02 81 00]
	f.push(CharCmd, "028100")
	if err := s.SetUSBCLimit(proto.LimitOutput, 5); err != nil {
		t.Fatalf("SetUSBCLimit: %v", err)
	}
	if got := f.writes[len(f.writes)-1][1]; got != "0201"+"03"+"05" {
		t.Fatalf("set frame = %s", got)
	}
}

func TestBypassThresholdGetSet(t *testing.T) {
	s, f := newCtlSession()
	// get -> [15 80 00 d0 e7] = 20.00 V
	f.push(CharCmd, "158000d0e7")
	if v, err := s.BypassThreshold(); err != nil || v < 19.99 || v > 20.01 {
		t.Fatalf("BypassThreshold = %v, %v", v, err)
	}
	// set 19.6 -> reply [15 81 00]
	f.push(CharCmd, "158100")
	if err := s.SetBypassThreshold(19.6); err != nil {
		t.Fatalf("SetBypassThreshold: %v", err)
	}
}

func TestSchedulesListAndUpsert(t *testing.T) {
	s, f := newCtlSession()
	// list -> [06 80 00 <count=1> <id=0> <trailer 10>]
	f.push(CharCmd, "0680000100"+"10")
	// get reply: [06 80 00 <id=00> <9-byte struct> <trailer>]
	// id=00, then status=01 type=01(daily) hour=03 min=1e(30) repeat=00000000 action=01
	f.push(CharCmd, "068000"+"00"+"0101031e"+"00000000"+"01"+"01")
	list, err := s.Schedules()
	if err != nil || len(list) != 1 {
		t.Fatalf("Schedules = %+v, %v", list, err)
	}
	if list[0].ID != 0 || list[0].Type != proto.TimerDaily || list[0].Hour != 3 || list[0].Action != 1 {
		t.Fatalf("timer = %+v", list[0])
	}
	// add -> reply [06 81 00 <newid=2>]
	f.push(CharCmd, "06810002")
	id, err := s.UpsertSchedule(0xFF, proto.Timer{Status: 1, Type: proto.TimerDaily, Hour: 6, Action: 1})
	if err != nil || id != 2 {
		t.Fatalf("UpsertSchedule id = %d, %v", id, err)
	}
	// delete -> reply [06 81 00]
	f.push(CharCmd, "068100")
	if err := s.DeleteSchedule(2); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
}
