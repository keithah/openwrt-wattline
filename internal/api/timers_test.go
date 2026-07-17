package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

func TestTimerCRUDUsesAssignedIDAndAuthoritativeCollections(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	session.timers = []proto.Timer{{ID: 2, Status: 1, Type: proto.TimerWeekly, Hour: 6, Minute: 30, Repeat: 62, Action: 1}}

	if rr := do(t, h, http.MethodGet, "/api/v1/device/timers", "tok", ""); rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	add := do(t, h, http.MethodPost, "/api/v1/device/timers", "tok", `{"status":1,"type":1,"hour":7,"minute":15,"repeat":0,"action":1}`)
	if add.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", add.Code, add.Body.String())
	}
	exactBody(t, add, `{"id":3,"status":1,"type":1,"hour":7,"minute":15,"repeat":0,"action":1}`)

	put := do(t, h, http.MethodPut, "/api/v1/device/timers/3", "tok", `{"status":-1,"type":3,"hour":8,"minute":5,"repeat":2147483650,"action":0}`)
	if put.Code != 200 {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	exactBody(t, put, `{"id":3,"status":-1,"type":3,"hour":8,"minute":5,"repeat":2147483650,"action":0}`)
	if session.getTimerCalls != 0 {
		t.Fatalf("PUT performed non-atomic API preflight GET %d times", session.getTimerCalls)
	}

	del := do(t, h, http.MethodDelete, "/api/v1/device/timers/3", "tok", "")
	if del.Code != 200 {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
	exactBody(t, del, `{"deleted":3,"timers":[{"id":2,"status":1,"type":2,"hour":6,"minute":30,"repeat":62,"action":1}]}`)
	if session.getTimerCalls != 0 {
		t.Fatalf("DELETE performed non-atomic API preflight GET %d times", session.getTimerCalls)
	}
}

func TestTimerRecurrenceValidationAndExactJSON(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	valid := []string{
		`{"status":1,"type":0,"hour":6,"minute":30,"repeat":302450666,"action":1}`,
		`{"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`,
		`{"status":1,"type":2,"hour":6,"minute":30,"repeat":254,"action":1}`,
		`{"status":1,"type":3,"hour":6,"minute":30,"repeat":2147483650,"action":1}`,
	}
	for i, body := range valid {
		if rr := do(t, h, http.MethodPost, "/api/v1/device/timers", "tok", body); rr.Code != 201 {
			t.Fatalf("valid %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	invalid := []string{
		`{}`, `{"id":1,"status":1,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`,
		`{"status":-2,"type":1,"hour":6,"minute":30,"repeat":0,"action":1}`,
		`{"status":1,"type":0,"hour":6,"minute":30,"repeat":503449578,"action":1}`,
		`{"status":1,"type":1,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":2,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":3,"hour":6,"minute":30,"repeat":1,"action":1}`,
		`{"status":1,"type":1,"hour":24,"minute":0,"repeat":0,"action":1}`,
		`{"status":1,"type":1,"hour":0,"minute":60,"repeat":0,"action":1}`,
		`{"status":1,"type":1,"hour":0,"minute":0,"repeat":0,"action":2}`,
		`{"status":1,"type":1,"hour":0,"minute":0,"repeat":0,"action":1,"extra":true}`,
	}
	for i, body := range invalid {
		if rr := do(t, h, http.MethodPost, "/api/v1/device/timers", "tok", body); rr.Code != 400 {
			t.Fatalf("invalid %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
}

func TestTimerIDsAndExistence(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	session.timers = []proto.Timer{{ID: 4, Status: 1, Type: proto.TimerDaily, Repeat: 0}}
	for _, id := range []string{"-1", "255", "x"} {
		if rr := do(t, h, http.MethodGet, "/api/v1/device/timers/"+id, "tok", ""); rr.Code != 400 {
			t.Fatalf("GET id %s: %d", id, rr.Code)
		}
	}
	if rr := do(t, h, http.MethodGet, "/api/v1/device/timers/5", "tok", ""); rr.Code != 404 {
		t.Fatalf("missing GET: %d", rr.Code)
	}
	body := `{"status":1,"type":1,"hour":0,"minute":0,"repeat":0,"action":0}`
	if rr := do(t, h, http.MethodPut, "/api/v1/device/timers/5", "tok", body); rr.Code != 404 {
		t.Fatalf("missing PUT: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/api/v1/device/timers/5", "tok", ""); rr.Code != 404 {
		t.Fatalf("missing DELETE: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodDelete, "/api/v1/device/timers/4", "tok", `{"x":1}`); rr.Code != 400 {
		t.Fatalf("DELETE body: %d", rr.Code)
	}

	var got []proto.Timer
	rr := do(t, h, http.MethodGet, "/api/v1/device/timers", "tok", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got == nil {
		t.Fatalf("timer JSON: %v %+v", err, got)
	}
}
