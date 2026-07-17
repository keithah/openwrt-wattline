package ble

import (
	"reflect"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func writeFrames(f *fakeTransport) []string {
	frames := make([]string, len(f.writes))
	for i, write := range f.writes {
		frames[i] = write[1]
	}
	return frames
}

func TestAtomicLimitPutDeleteAndRuntimeUnset(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "028100")
	f.push(CharCmd, "02800005")
	f.push(CharCmd, "028200")
	f.push(CharCmd, "02800003")
	f.push(CharCmd, "0280ff")

	if got, err := s.PutUSBCLimit(proto.LimitOutput, 5); err != nil || got != 5 {
		t.Fatalf("PutUSBCLimit = %d, %v", got, err)
	}
	if got, err := s.DeleteUSBCLimit(proto.LimitOutput); err != nil || got != 3 {
		t.Fatalf("DeleteUSBCLimit = %d, %v", got, err)
	}
	if got, err := s.GetUSBCLimit(proto.LimitRuntime); err != nil || got != -1 {
		t.Fatalf("GetUSBCLimit(runtime) = %d, %v", got, err)
	}
	want := []string{"02010305", "020003", "020203", "020003", "020004"}
	if got := writeFrames(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("limit frames = %v, want %v", got, want)
	}
}

func pushOneTimerList(f *fakeTransport, id byte, hour byte) {
	f.push(CharCmd, "06800001"+hexByte(id))
	f.push(CharCmd, "068000"+hexByte(id)+"0101"+hexByte(hour)+"1e0000000001")
}

func hexByte(v byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&0xf]})
}

func TestAtomicTimerMutationsAdoptIDAndRelist(t *testing.T) {
	s, f := newCtlSession()
	timer := proto.Timer{Status: 1, Type: proto.TimerDaily, Hour: 6, Minute: 30, Action: 1}

	f.push(CharCmd, "06810007")
	pushOneTimerList(f, 7, 6)
	list, id, err := s.AddTimer(timer)
	if err != nil || id != 7 || len(list) != 1 || list[0].ID != 7 {
		t.Fatalf("AddTimer = %+v, %d, %v", list, id, err)
	}

	f.push(CharCmd, "068100")
	pushOneTimerList(f, 7, 8)
	timer.Hour = 8
	if list, err = s.PutTimer(7, timer); err != nil || len(list) != 1 || list[0].Hour != 8 {
		t.Fatalf("PutTimer = %+v, %v", list, err)
	}

	f.push(CharCmd, "068100")
	f.push(CharCmd, "06800000")
	if list, err = s.DeleteTimer(7); err != nil || len(list) != 0 {
		t.Fatalf("DeleteTimer = %+v, %v", list, err)
	}

	want := []string{
		"060102ff0101061e0000000001", "060000", "06000107",
		"060102070101081e0000000001", "060000", "06000107",
		"06010407", "060000",
	}
	if got := writeFrames(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("timer frames = %v, want %v", got, want)
	}
}

func TestAtomicTimerRejectsTruncatedListBeforeGet(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "0680000207") // declares two IDs, contains one
	if _, err := s.ListTimers(); err == nil {
		t.Fatal("ListTimers accepted truncated ID list")
	}
	if got := writeFrames(f); !reflect.DeepEqual(got, []string{"060000"}) {
		t.Fatalf("truncated list frames = %v, want list only", got)
	}
}

func TestAtomicTimerAddRequiresAssignedID(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "068100") // successful reply with no assigned ID
	f.push(CharCmd, "06800000")
	timer := proto.Timer{Status: 1, Type: proto.TimerDaily, Hour: 6, Action: 1}
	if _, _, err := s.AddTimer(timer); err == nil {
		t.Fatal("AddTimer accepted reply without assigned ID")
	}
	if got := writeFrames(f); !reflect.DeepEqual(got, []string{"060102ff010106000000000001"}) {
		t.Fatalf("missing-ID add frames = %v, want add only", got)
	}
}

func TestAtomicTimerRejectsShortSettingsBody(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "0680000107")
	f.push(CharCmd, "068000070101061e00000000") // ID plus eight setting bytes
	if _, err := s.ListTimers(); err == nil {
		t.Fatal("ListTimers accepted short timer settings body")
	}
}

func TestAtomicTimerRejectsMismatchedGetID(t *testing.T) {
	s, f := newCtlSession()
	f.push(CharCmd, "0680000107")
	f.push(CharCmd, "068000080101061e0000000001")
	if _, err := s.ListTimers(); err == nil {
		t.Fatal("ListTimers accepted mismatched GET reply ID")
	}
}

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
