package api

import (
	"encoding/json"
	"errors"
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
	w = do(t, h, "POST", "/api/v1/device/schedules", "tok", `{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`)
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

func TestScheduleAliasesStrictBodiesAndRecurrenceValidation(t *testing.T) {
	fc := &fakeControl{nextID: 3, timers: []proto.Timer{{ID: 4, Status: 1, Type: proto.TimerDaily}}}
	h := ctlServer(t, fc)
	if rr := do(t, h, http.MethodGet, "/api/v1/device/schedules", "tok", `{}`); rr.Code != 400 {
		t.Fatalf("GET body: %d", rr.Code)
	}
	if rr := do(t, h, http.MethodDelete, "/api/v1/device/schedules/4", "tok", `{}`); rr.Code != 400 {
		t.Fatalf("DELETE body: %d", rr.Code)
	}
	invalid := []string{
		`{"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`,
		`{"status":-2,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`,
		`{"status":1,"type":1,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":2,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":3,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1,"extra":true}`,
	}
	for _, body := range invalid {
		if rr := do(t, h, http.MethodPost, "/api/v1/device/schedules", "tok", body); rr.Code != 400 {
			t.Fatalf("accepted %s: %d", body, rr.Code)
		}
	}
	if fc.lastUpsert != (proto.Timer{}) || len(fc.deleted) != 0 {
		t.Fatalf("invalid aliases touched BLE: upsert=%+v deleted=%v", fc.lastUpsert, fc.deleted)
	}
}

func TestScheduleAliasesUseControlServiceAndCanonicalErrors(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	session.timers = []proto.Timer{{ID: 4, Status: 1, Type: proto.TimerDaily, Hour: 3}}
	if rr := do(t, h, http.MethodGet, "/api/v1/device/schedules", "tok", ""); rr.Code != 200 {
		t.Fatalf("GET: %d %s", rr.Code, rr.Body.String())
	}
	add := do(t, h, http.MethodPost, "/api/v1/device/schedules", "tok", `{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`)
	if add.Code != 200 {
		t.Fatalf("add: %d %s", add.Code, add.Body.String())
	}
	exactBody(t, add, `{"id":3,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`)
	missing := do(t, h, http.MethodPost, "/api/v1/device/schedules", "tok", `{"id":9,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`)
	if missing.Code != 404 {
		t.Fatalf("missing put: %d %s", missing.Code, missing.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/api/v1/device/schedules/9", "tok", ""); rr.Code != 404 {
		t.Fatalf("missing delete: %d %s", rr.Code, rr.Body.String())
	}

	for _, tc := range []struct {
		name                 string
		connected, supported bool
		err                  error
		want                 int
	}{
		{"unsupported", true, false, nil, 409},
		{"disconnected", false, true, nil, 503},
		{"BLE", true, true, errors.New("gatt"), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := canonicalServer(t, tc.connected, tc.supported, true, tc.err)
			if rr := do(t, h, http.MethodGet, "/api/v1/device/schedules", "tok", ""); rr.Code != tc.want {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
		})
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

func TestLimitCompatibilityAliasesUseControlService(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	if w := do(t, h, "GET", "/api/v1/device/usbc-limit", "tok", ""); w.Code != 200 {
		t.Fatalf("GET alias: %d %s", w.Code, w.Body.String())
	}
	w := do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"output","watts":140}`)
	if w.Code != 200 {
		t.Fatalf("PUT alias: %d %s", w.Code, w.Body.String())
	}
	var got map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["level"] != 5 || got["watts"] != 140 {
		t.Fatalf("alias response: %+v", got)
	}
	if w = do(t, h, "POST", "/api/v1/device/usbc-limit", "tok", `{"type":"output","clear":true}`); w.Code != 200 {
		t.Fatalf("DELETE alias: %d %s", w.Code, w.Body.String())
	}
}

func TestBypassCompatibilityAliasUsesControlService(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	if w := do(t, h, "GET", "/api/v1/device/bypass-threshold", "tok", ""); w.Code != 200 {
		t.Fatalf("GET alias: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, h, "POST", "/api/v1/device/bypass-threshold", "tok", `{"volts":19.6}`); w.Code != 200 {
		t.Fatalf("POST alias: %d %s", w.Code, w.Body.String())
	}
}
