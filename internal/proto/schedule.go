package proto

import (
	"encoding/binary"
	"fmt"
)

// Timer types (API.md §3.4 TIMER_SETTINGS).
const (
	TimerOneShot = 0
	TimerDaily   = 1
	TimerWeekly  = 2
	TimerMonthly = 3
)

// Schedule sub-opcodes carried in byte 2 of the SCHEDULED_ON_OFF frame.
const (
	schedListIDs = 0x00
	schedGetOne  = 0x01
	schedUpsert  = 0x02
	schedDelete  = 0x04
)

// Timer is the 9-byte TIMER_SETTINGS struct (API.md §3.4). Repeat is
// interpreted by Type: daily=0; weekly=weekday bitmask (bit1=Mon..bit7=Sun) in
// the low byte; one-shot=year(u16 LE) | month<<16 | day<<24; monthly=day
// bitmask (bit1=day1..bit31=day31).
type Timer struct {
	ID     byte   `json:"id"`
	Status int8   `json:"status"` // 0 empty,1 enabled,-1 disabled,-2/-3 disabled(validation/expired)
	Type   byte   `json:"type"`   // 0 one-shot,1 daily,2 weekly,3 monthly
	Hour   byte   `json:"hour"`
	Minute byte   `json:"minute"`
	Repeat uint32 `json:"repeat"`
	Action byte   `json:"action"` // 0 turn off, 1 turn on
}

// Encode serializes the 9-byte struct (ID is not part of the struct body).
func (t Timer) Encode() []byte {
	b := make([]byte, 9)
	b[0] = byte(t.Status)
	b[1] = t.Type
	b[2] = t.Hour
	b[3] = t.Minute
	binary.LittleEndian.PutUint32(b[4:], t.Repeat)
	b[8] = t.Action
	return b
}

// ParseTimer decodes a 9-byte struct (extra trailer bytes are ignored).
func ParseTimer(b []byte) (Timer, error) {
	if len(b) < 9 {
		return Timer{}, fmt.Errorf("timer struct too short: % x", b)
	}
	return Timer{
		Status: int8(b[0]),
		Type:   b[1],
		Hour:   b[2],
		Minute: b[3],
		Repeat: binary.LittleEndian.Uint32(b[4:8]),
		Action: b[8],
	}, nil
}

// ScheduleList builds a request for the list of timer IDs.
func ScheduleList() []byte { return []byte{CmdScheduledOnOff, ActGet, schedListIDs} }

// ScheduleGet builds a request for one timer's settings.
func ScheduleGet(id byte) []byte { return []byte{CmdScheduledOnOff, ActGet, schedGetOne, id} }

// ScheduleUpsert builds an add/edit; pass id 0xFF to add a new timer.
func ScheduleUpsert(id byte, t Timer) []byte {
	return append([]byte{CmdScheduledOnOff, ActSet, schedUpsert, id}, t.Encode()...)
}

// ScheduleDelete builds a delete for a timer id.
func ScheduleDelete(id byte) []byte { return []byte{CmdScheduledOnOff, ActSet, schedDelete, id} }

// ParseScheduleIDs reads the id list from a list reply payload (post-Validate):
// [count, id0, id1, ... , trailer]. Returns nil for a nil payload.
func ParseScheduleIDs(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	if len(payload) < 1 {
		return []byte{}
	}
	n := int(payload[0])
	ids := make([]byte, 0, n)
	for i := 0; i < n && 1+i < len(payload); i++ {
		ids = append(ids, payload[1+i])
	}
	return ids
}

// ParsedUpsertID extracts the assigned id from an add reply payload (byte 3 of
// the frame becomes payload[0] after ValidateReply strips the header).
func ParsedUpsertID(payload []byte, fallback byte) byte {
	if len(payload) >= 1 {
		return payload[0]
	}
	return fallback
}
