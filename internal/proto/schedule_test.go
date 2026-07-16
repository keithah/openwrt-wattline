package proto

import "testing"

func TestScheduleListGetFrames(t *testing.T) {
	if got := ScheduleList(); string(got) != string([]byte{0x06, 0x00, 0x00}) {
		t.Errorf("list = % x", got)
	}
	if got := ScheduleGet(3); string(got) != string([]byte{0x06, 0x00, 0x01, 0x03}) {
		t.Errorf("get = % x", got)
	}
	if got := ScheduleDelete(2); string(got) != string([]byte{0x06, 0x01, 0x04, 0x02}) {
		t.Errorf("delete = % x", got)
	}
}

func TestParseScheduleIDs(t *testing.T) {
	// payload after ValidateReply strips [cmd,act,result]: [count, id0, id1, ...trailer]
	ids := ParseScheduleIDs([]byte{0x02, 0x00, 0x05, 0x10})
	if len(ids) != 2 || ids[0] != 0 || ids[1] != 5 {
		t.Fatalf("ids = %v", ids)
	}
	if got := ParseScheduleIDs([]byte{0x00}); len(got) != 0 {
		t.Errorf("empty list = %v", got)
	}
	if got := ParseScheduleIDs(nil); got != nil {
		t.Errorf("nil payload = %v", got)
	}
}

func TestTimerRoundTripDaily(t *testing.T) {
	tm := Timer{Status: 1, Type: TimerDaily, Hour: 3, Minute: 30, Action: 1}
	b := tm.Encode()
	if len(b) != 9 {
		t.Fatalf("encoded len %d, want 9", len(b))
	}
	// status,type,hour,minute, 4x repeat=0, action
	if b[0] != 1 || b[1] != 1 || b[2] != 3 || b[3] != 30 || b[8] != 1 {
		t.Errorf("daily encode = % x", b)
	}
	got, err := ParseTimer(b)
	if err != nil || got != tm {
		t.Errorf("round-trip = %+v (%v), want %+v", got, err, tm)
	}
}

func TestTimerRoundTripWeekly(t *testing.T) {
	// weekdays bitmask stored at offset 4 (bit1=Mon..bit7=Sun)
	tm := Timer{Status: 1, Type: TimerWeekly, Hour: 8, Minute: 0, Repeat: 0b00101010, Action: 0}
	got, err := ParseTimer(tm.Encode())
	if err != nil || got.Repeat != tm.Repeat || got.Type != TimerWeekly {
		t.Errorf("weekly round-trip = %+v (%v)", got, err)
	}
}

func TestTimerRoundTripOneShot(t *testing.T) {
	// one-shot stores year(u16 LE)@4, month@6, day@7
	tm := Timer{Status: 1, Type: TimerOneShot, Hour: 12, Minute: 15,
		Repeat: uint32(2026) | (7 << 16) | (14 << 24), Action: 1}
	got, err := ParseTimer(tm.Encode())
	if err != nil || got.Repeat != tm.Repeat {
		t.Errorf("oneshot round-trip repeat = %#x (%v), want %#x", got.Repeat, err, tm.Repeat)
	}
}

func TestParseTimerShort(t *testing.T) {
	if _, err := ParseTimer([]byte{0x01, 0x01}); err == nil {
		t.Error("short timer should error")
	}
}

func TestScheduleUpsertFrame(t *testing.T) {
	tm := Timer{Status: 1, Type: TimerDaily, Hour: 3, Minute: 0, Action: 1}
	// add: id 0xFF
	add := ScheduleUpsert(0xFF, tm)
	if add[0] != 0x06 || add[1] != 0x01 || add[2] != 0x02 || add[3] != 0xFF {
		t.Fatalf("add header = % x", add[:4])
	}
	if len(add) != 4+9 {
		t.Errorf("add len = %d, want 13", len(add))
	}
}
