package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestClockRoutesExactJSONAndManualReason(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	rr := do(t, h, http.MethodGet, "/api/v1/device/clock", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("clock: %d %s", rr.Code, rr.Body.String())
	}
	if session.clockReads != 1 {
		t.Fatalf("clock reads %d", session.clockReads)
	}
	exactBody(t, rr, `{"available":true,"device_time":"2026-07-17T20:00:00Z","system_time":"2026-07-17T20:00:02Z","drift_seconds":-2}`)

	rr = do(t, h, http.MethodPost, "/api/v1/device/clock/sync", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("sync: %d %s", rr.Code, rr.Body.String())
	}
	if session.clockSyncReason != 0 {
		t.Fatalf("reason %d", session.clockSyncReason)
	}
	exactBody(t, rr, `{"synced":true,"system_time":"2026-07-17T20:00:02Z"}`)
	if rr = do(t, h, http.MethodPost, "/api/v1/device/clock/sync", "tok", `{}`); rr.Code != 400 {
		t.Fatalf("sync body: %d", rr.Code)
	}
}

func TestClockAbsentIsAvailableFalseAndZeroIO(t *testing.T) {
	h, store, session := canonicalServer(t, true, true, true, nil)
	id := *store.Snapshot().Device
	id.Characteristics["current_time"] = false
	store.SetIdentity(id)
	session.clockOK = false
	rr := do(t, h, http.MethodGet, "/api/v1/device/clock", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("clock: %d %s", rr.Code, rr.Body.String())
	}
	exactBody(t, rr, `{"available":false,"device_time":null,"system_time":"2026-07-17T20:00:02Z","drift_seconds":null}`)
	if session.clockReads != 0 {
		t.Fatalf("absent clock did %d reads", session.clockReads)
	}
}

func TestLifecycleAndOTARequestBodies(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	tests := []struct{ method, path, body, want string }{
		{http.MethodPost, "/api/v1/device/restart", "", `{"status":"restarting","reconnect":"armed"}`},
		{http.MethodPost, "/api/v1/device/shutdown", `{"confirm":true}`, `{"status":"shutdown","reconnect":"disarmed"}`},
		{http.MethodPost, "/api/v1/device/ota/enter", `{"confirm":true}`, `{"mode":"ota","reconnect":"bootloader"}`},
		{http.MethodPost, "/api/v1/device/ota/exit", "", `{"mode":"app","reconnect":"armed"}`},
	}
	for _, tt := range tests {
		rr := do(t, h, tt.method, tt.path, "tok", tt.body)
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", tt.path, rr.Code, rr.Body.String())
		}
		exactBody(t, rr, tt.want)
	}
	if !session.restarted || !session.shutdown || !session.enteredOTA || !session.exitedOTA {
		t.Fatalf("lifecycle not invoked: %+v", session)
	}
	for _, path := range []string{"/api/v1/device/restart", "/api/v1/device/ota/exit"} {
		if rr := do(t, h, http.MethodPost, path, "tok", `{}`); rr.Code != 400 {
			t.Fatalf("body accepted by %s: %d", path, rr.Code)
		}
	}
	before := session.shutdownCalls
	for _, body := range []string{"", `{}`, `{"confirm":false}`, `{"confirm":true,"extra":1}`, `{"confirm":true} {}`} {
		if rr := do(t, h, http.MethodPost, "/api/v1/device/shutdown", "tok", body); rr.Code != 400 {
			t.Fatalf("shutdown accepted %q: %d", body, rr.Code)
		}
	}
	if session.shutdownCalls != before {
		t.Fatalf("invalid shutdown invoked lifecycle %d times", session.shutdownCalls-before)
	}
	for _, body := range []string{"", `{}`, `{"confirm":false}`, `{"confirm":true,"extra":1}`} {
		if rr := do(t, h, http.MethodPost, "/api/v1/device/ota/enter", "tok", body); rr.Code != 400 {
			t.Fatalf("enter accepted %q: %d", body, rr.Code)
		}
	}
}

func TestOTAInfoAndNoFlashingRoutes(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	rr := do(t, h, http.MethodGet, "/api/v1/device/ota", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("info: %d %s", rr.Code, rr.Body.String())
	}
	exactBody(t, rr, `{"mode":"app","cid":773,"bootloader_firmware":"1.0.3"}`)
	for _, path := range []string{"/api/v1/device/ota/flash", "/api/v1/device/ota/erase", "/api/v1/device/ota/program"} {
		if rr := do(t, h, http.MethodPost, path, "tok", ""); rr.Code != 404 {
			t.Fatalf("flashing route %s exists: %d", path, rr.Code)
		}
	}
}

func TestAdvancedRoutesExactShapes(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	tests := []struct{ method, path, body, want string }{
		{http.MethodPut, "/api/v1/device/advanced/running-mode", `{"mode":1}`, `{"mode":1}`},
		{http.MethodGet, "/api/v1/device/advanced/barrier-free", "", `{"enabled":false}`},
		{http.MethodPut, "/api/v1/device/advanced/barrier-free", `{"enabled":true}`, `{"enabled":true}`},
		{http.MethodGet, "/api/v1/device/advanced/usb-fw-version", "", `{"raw":"010409","major":1,"minor":4,"patch":9}`},
		{http.MethodPut, "/api/v1/device/advanced/ble-pin", `{"pin":"020555"}`, `{"updated":true}`},
	}
	for _, tt := range tests {
		rr := do(t, h, tt.method, tt.path, "tok", tt.body)
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", tt.path, rr.Code, rr.Body.String())
		}
		exactBody(t, rr, tt.want)
	}
	if session.runningMode != 1 || session.blePIN != 20555 {
		t.Fatalf("mode=%d pin=%d", session.runningMode, session.blePIN)
	}
	if session.savedPIN != "020555" || strings.Join(session.pinOrder, ",") != "ble,save" {
		t.Fatalf("PIN persistence value=%q order=%v", session.savedPIN, session.pinOrder)
	}
	if rr := do(t, h, http.MethodGet, "/api/v1/device/advanced/running-mode", "tok", ""); rr.Code != 405 {
		t.Fatalf("running GET: %d", rr.Code)
	}
	for _, body := range []string{`{"mode":2}`, `{"mode":-1}`, `{"mode":"1"}`, `{}`} {
		if rr := do(t, h, http.MethodPut, "/api/v1/device/advanced/running-mode", "tok", body); rr.Code != 400 {
			t.Fatalf("mode %s: %d", body, rr.Code)
		}
	}
	for _, body := range []string{`{"pin":"20555"}`, `{"pin":"0000000"}`, `{"pin":"abcdef"}`, `{"pin":20555}`, `{}`} {
		if rr := do(t, h, http.MethodPut, "/api/v1/device/advanced/ble-pin", "tok", body); rr.Code != 400 {
			t.Fatalf("pin %s: %d", body, rr.Code)
		}
	}
}

func TestBLEPINRequiresPersistenceBeforeDeviceWrite(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil, nil)
	rr := do(t, h, http.MethodPut, "/api/v1/device/advanced/ble-pin", "tok", `{"pin":"020555"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if len(session.pinOrder) != 0 {
		t.Fatalf("device/persistence touched: %v", session.pinOrder)
	}
}

func TestBLEPINDoesNotPersistAfterBLEFailure(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, errors.New("gatt"))
	rr := do(t, h, http.MethodPut, "/api/v1/device/advanced/ble-pin", "tok", `{"pin":"020555"}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Join(session.pinOrder, ",") != "ble" || session.savedPIN != "" {
		t.Fatalf("order=%v saved=%q", session.pinOrder, session.savedPIN)
	}
}

func TestBLEPINPersistenceFailureIsInternal(t *testing.T) {
	h, _, session := canonicalServer(t, true, true, true, nil)
	session.savePINError = errors.New("disk")
	rr := do(t, h, http.MethodPut, "/api/v1/device/advanced/ble-pin", "tok", `{"pin":"020555"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Join(session.pinOrder, ",") != "ble,save" {
		t.Fatalf("order=%v", session.pinOrder)
	}
}

func TestAdvancedControlErrorOrdering(t *testing.T) {
	h, store, _ := canonicalServer(t, true, true, false, nil)
	if rr := do(t, h, http.MethodGet, "/api/v1/device/advanced/barrier-free", "tok", ""); rr.Code != 403 {
		t.Fatalf("disabled: %d", rr.Code)
	}
	id := *store.Snapshot().Device
	id.Mode = "ota"
	store.SetIdentity(id)
	if rr := do(t, h, http.MethodGet, "/api/v1/device/advanced/barrier-free", "tok", ""); rr.Code != 409 {
		t.Fatalf("unsupported before disabled: %d", rr.Code)
	}
}

func TestRemainingRoutesWithoutDeviceControlReturnDisconnected(t *testing.T) {
	h, _, _ := testServer(t)
	tests := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/device/timers", ""},
		{http.MethodPost, "/api/v1/device/timers", `{"status":1,"type":1,"hour":0,"minute":0,"repeat":0,"action":0}`},
		{http.MethodGet, "/api/v1/device/clock", ""},
		{http.MethodPost, "/api/v1/device/restart", ""},
		{http.MethodGet, "/api/v1/device/ota", ""},
		{http.MethodPut, "/api/v1/device/advanced/running-mode", `{"mode":0}`},
	}
	for _, tt := range tests {
		rr := do(t, h, tt.method, tt.path, "tok", tt.body)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: %d %s", tt.method, tt.path, rr.Code, rr.Body.String())
		}
	}
}

func TestReadOnlyRemainingRoutesRejectBodies(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	for _, path := range []string{
		"/api/v1/device/timers", "/api/v1/device/timers/0", "/api/v1/device/clock",
		"/api/v1/device/ota", "/api/v1/device/advanced/barrier-free",
		"/api/v1/device/advanced/usb-fw-version",
	} {
		if rr := do(t, h, http.MethodGet, path, "tok", `{}`); rr.Code != http.StatusBadRequest {
			t.Fatalf("GET %s accepted body: %d %s", path, rr.Code, rr.Body.String())
		}
	}
}
