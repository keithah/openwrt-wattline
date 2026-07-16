package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

type fakeControl struct {
	limits     map[int]int
	threshold  float64
	timers     []proto.Timer
	nextID     byte
	lastUpsert proto.Timer
	deleted    []byte
}

func (f *fakeControl) USBCLimit(typ int) (int, error) { return f.limits[typ], nil }
func (f *fakeControl) SetUSBCLimit(typ, level int) error {
	if f.limits == nil {
		f.limits = map[int]int{}
	}
	f.limits[typ] = level
	return nil
}
func (f *fakeControl) ClearUSBCLimit(typ int) error       { delete(f.limits, typ); return nil }
func (f *fakeControl) BypassThreshold() (float64, error)  { return f.threshold, nil }
func (f *fakeControl) SetBypassThreshold(v float64) error { f.threshold = v; return nil }
func (f *fakeControl) Schedules() ([]proto.Timer, error)  { return f.timers, nil }
func (f *fakeControl) UpsertSchedule(id byte, t proto.Timer) (byte, error) {
	f.lastUpsert = t
	if id == 0xFF {
		return f.nextID, nil
	}
	return id, nil
}
func (f *fakeControl) DeleteSchedule(id byte) error { f.deleted = append(f.deleted, id); return nil }

func ctlServer(t *testing.T, c Control) http.Handler {
	h, _, _ := testServerWith(t, func(d *Deps) {
		d.Control = func() Control { return c }
	})
	return h
}

func TestUSBCLimitAPI(t *testing.T) {
	fc := &fakeControl{limits: map[int]int{proto.LimitGlobal: 4, proto.LimitRuntime: -1}}
	h := ctlServer(t, fc)
	w := do(t, h, "GET", "/api/v1/device/usbc-limit", "tok", "")
	if w.Code != 200 {
		t.Fatalf("GET code %d", w.Code)
	}
	var got map[string]map[string]int
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["global"]["watts"] != 100 || got["runtime"]["level"] != -1 {
		t.Fatalf("got %+v", got)
	}
	// set output to 140W
	w = do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"output","watts":140}`)
	if w.Code != 200 || fc.limits[proto.LimitOutput] != 5 {
		t.Fatalf("set code %d, level %d", w.Code, fc.limits[proto.LimitOutput])
	}
	// invalid watts
	if w = do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"output","watts":99}`); w.Code != 400 {
		t.Fatalf("bad watts code %d", w.Code)
	}
	// invalid type
	if w = do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"runtime","watts":100}`); w.Code != 400 {
		t.Fatalf("runtime set should be rejected, code %d", w.Code)
	}
	// clear
	if w = do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"global","clear":true}`); w.Code != 200 {
		t.Fatalf("clear code %d", w.Code)
	}
}

func TestBypassThresholdAPI(t *testing.T) {
	fc := &fakeControl{threshold: 20.0}
	h := ctlServer(t, fc)
	w := do(t, h, "GET", "/api/v1/device/bypass-threshold", "tok", "")
	if w.Code != 200 {
		t.Fatalf("GET %d", w.Code)
	}
	w = do(t, h, "POST", "/api/v1/device/bypass-threshold", "tok", `{"volts":19.6}`)
	if w.Code != 200 || fc.threshold != 19.6 {
		t.Fatalf("set code %d thr %v", w.Code, fc.threshold)
	}
	if w = do(t, h, "POST", "/api/v1/device/bypass-threshold", "tok", `{"volts":0}`); w.Code != 400 {
		t.Fatalf("zero volts should be 400, got %d", w.Code)
	}
}

func TestScheduleAPI(t *testing.T) {
	fc := &fakeControl{nextID: 3, timers: []proto.Timer{{ID: 0, Status: 1, Type: proto.TimerDaily, Hour: 3, Action: 1}}}
	h := ctlServer(t, fc)
	w := do(t, h, "GET", "/api/v1/device/schedules", "tok", "")
	if w.Code != 200 {
		t.Fatalf("GET %d", w.Code)
	}
	var list []proto.Timer
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Hour != 3 {
		t.Fatalf("list %+v", list)
	}
	// add (no id) -> assigned nextID 3
	w = do(t, h, "POST", "/api/v1/device/schedules", "tok", `{"type":1,"hour":6,"minute":30,"action":1}`)
	if w.Code != 200 {
		t.Fatalf("add %d", w.Code)
	}
	var added proto.Timer
	json.Unmarshal(w.Body.Bytes(), &added)
	if added.ID != 3 || added.Status != 1 || added.Hour != 6 {
		t.Fatalf("added %+v", added)
	}
	// invalid hour
	if w = do(t, h, "POST", "/api/v1/device/schedules", "tok", `{"type":1,"hour":25}`); w.Code != 400 {
		t.Fatalf("bad hour %d", w.Code)
	}
	// delete
	if w = do(t, h, "DELETE", "/api/v1/device/schedules/0", "tok", ""); w.Code != 200 || len(fc.deleted) != 1 {
		t.Fatalf("delete %d deleted=%v", w.Code, fc.deleted)
	}
	if w = do(t, h, "DELETE", "/api/v1/device/schedules/abc", "tok", ""); w.Code != 400 {
		t.Fatalf("bad id delete %d", w.Code)
	}
}

func TestControlUnavailable503(t *testing.T) {
	h, _, _ := testServerWith(t, func(d *Deps) { d.Control = func() Control { return nil } })
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/v1/device/usbc-limit"},
		{"GET", "/api/v1/device/bypass-threshold"},
		{"GET", "/api/v1/device/schedules"},
	} {
		if w := do(t, h, c.m, c.p, "tok", ""); w.Code != 503 {
			t.Fatalf("%s %s = %d, want 503", c.m, c.p, w.Code)
		}
	}
}
