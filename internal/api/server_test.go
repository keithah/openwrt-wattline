package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/rules"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type nopDev struct{}

func (nopDev) DCControl(bool) error     { return nil }
func (nopDev) TypeCOutput(bool) error   { return nil }
func (nopDev) BypassControl(bool) error { return nil }
func (nopDev) Restart() error           { return nil }
func (nopDev) Shutdown() error          { return nil }

func testServer(t *testing.T) (http.Handler, *state.Store, *[]config.Rule) {
	store := state.NewStore()
	store.SetBattery(proto.Battery{Level: 77})
	eng, _ := rules.NewEngine(nil)
	saved := &[]config.Rule{}
	d := Deps{
		Store: store, Engine: eng,
		Exec:      actions.NewExecutor(nopDev{}, "Link-Power-2"),
		Token:     "tok",
		Identity:  func() ble.Identity { return ble.Identity{Model: "BP4SL3V2", MAC: "DC:04:5A:EB:72:2B"} },
		Connected: func() bool { return true },
		LoadRules: func() []config.Rule { return *saved },
		SaveRules: func(rs []config.Rule) error { *saved = rs; return eng.SetRules(rs) },
	}
	return NewServer(d), store, saved
}

func do(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAuth(t *testing.T) {
	h, _, _ := testServer(t)
	if rr := do(t, h, "GET", "/api/v1/status", "", ""); rr.Code != 401 {
		t.Fatalf("no token: %d", rr.Code)
	}
	if rr := do(t, h, "GET", "/api/v1/status", "wrong", ""); rr.Code != 401 {
		t.Fatalf("bad token: %d", rr.Code)
	}
	if rr := do(t, h, "GET", "/api/v1/status", "tok", ""); rr.Code != 200 {
		t.Fatalf("good token: %d", rr.Code)
	}
}

func TestCORS(t *testing.T) {
	h, _, _ := testServer(t)
	// Preflight OPTIONS must be answered (mux only registers GET/POST/etc)
	// with 204 and the CORS headers the browser requires.
	rr := do(t, h, "OPTIONS", "/api/v1/telemetry", "", "")
	if rr.Code != 204 {
		t.Fatalf("OPTIONS preflight code: %d, want 204", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("preflight Allow-Origin: %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Fatalf("preflight Allow-Headers missing Authorization: %q", got)
	}
	// A real authed GET response must also carry Allow-Origin.
	rr = do(t, h, "GET", "/api/v1/status", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("GET code: %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("GET Allow-Origin: %q", got)
	}
}

func TestAuthEmptyTokenAlwaysDenies(t *testing.T) {
	store := state.NewStore()
	eng, _ := rules.NewEngine(nil)
	saved := &[]config.Rule{}
	d := Deps{
		Store: store, Engine: eng,
		Exec:      actions.NewExecutor(nopDev{}, "Link-Power-2"),
		Token:     "", // empty token must never be satisfiable
		Identity:  func() ble.Identity { return ble.Identity{Model: "BP4SL3V2", MAC: "DC:04:5A:EB:72:2B"} },
		Connected: func() bool { return true },
		LoadRules: func() []config.Rule { return *saved },
		SaveRules: func(rs []config.Rule) error { *saved = rs; return eng.SetRules(rs) },
	}
	h := NewServer(d)
	if rr := do(t, h, "GET", "/api/v1/status", "", ""); rr.Code != 401 {
		t.Fatalf("empty token, no auth header: %d", rr.Code)
	}
	if rr := do(t, h, "GET", "/api/v1/status", "anything", ""); rr.Code != 401 {
		t.Fatalf("empty token, arbitrary bearer: %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/v1/device/action", "", `{"action":"shutdown"}`); rr.Code != 401 {
		t.Fatalf("empty token must not allow shutdown bypass: %d", rr.Code)
	}
}

func TestStatusAndTelemetry(t *testing.T) {
	h, _, _ := testServer(t)
	rr := do(t, h, "GET", "/api/v1/status", "tok", "")
	var st map[string]any
	json.Unmarshal(rr.Body.Bytes(), &st)
	if st["connected"] != true {
		t.Fatalf("status: %v", st)
	}
	rr = do(t, h, "GET", "/api/v1/telemetry", "tok", "")
	var snap state.Snapshot
	json.Unmarshal(rr.Body.Bytes(), &snap)
	if snap.Battery == nil || snap.Battery.Level != 77 {
		t.Fatalf("telemetry: %+v", snap)
	}
}

func TestCreateRuleValidation(t *testing.T) {
	h, _, saved := testServer(t)
	bad := `{"name":"x","enabled":true,"condition":"input_power","state":"absent","actions":["dc_toggle"]}`
	if rr := do(t, h, "POST", "/api/v1/rules", "tok", bad); rr.Code != 400 {
		t.Fatalf("bad action should 400, got %d", rr.Code)
	}
	shutdownNoConfirm := `{"name":"y","enabled":true,"condition":"input_power","state":"absent","actions":["shutdown"]}`
	if rr := do(t, h, "POST", "/api/v1/rules", "tok", shutdownNoConfirm); rr.Code != 400 {
		t.Fatalf("unconfirmed shutdown should 400, got %d", rr.Code)
	}
	good := `{"name":"z","enabled":true,"condition":"input_power","state":"absent","hold":600000000000,"actions":["dc_off"]}`
	if rr := do(t, h, "POST", "/api/v1/rules", "tok", good); rr.Code != 200 {
		t.Fatalf("good rule: %d body=%s", rr.Code, rr.Body)
	}
	if len(*saved) != 1 || (*saved)[0].Name != "z" {
		t.Fatalf("not persisted: %+v", *saved)
	}
}

func TestCreateBatteryRuleDefaultsHysteresisMargin(t *testing.T) {
	h, _, saved := testServer(t)
	body := `{"name":"low_batt","enabled":true,"condition":"battery_level","op":"below","percent":15,"actions":["dc_off"]}`
	rr := do(t, h, "POST", "/api/v1/rules", "tok", body)
	if rr.Code != 200 {
		t.Fatalf("create rule: %d body=%s", rr.Code, rr.Body)
	}
	var got config.Rule
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.HysteresisMargin != 5 {
		t.Fatalf("response hysteresis_margin: want 5, got %d", got.HysteresisMargin)
	}
	if len(*saved) != 1 || (*saved)[0].HysteresisMargin != 5 {
		t.Fatalf("persisted hysteresis_margin: %+v", *saved)
	}
}

func TestDeviceAction(t *testing.T) {
	h, _, _ := testServer(t)
	if rr := do(t, h, "POST", "/api/v1/device/action", "tok", `{"action":"dc_off"}`); rr.Code != 200 {
		t.Fatalf("action: %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/v1/device/action", "tok", `{"action":"bogus"}`); rr.Code != 400 {
		t.Fatalf("bad action: %d", rr.Code)
	}
}

func TestDeleteRule(t *testing.T) {
	h, _, saved := testServer(t)
	*saved = []config.Rule{{Name: "gone", Condition: "input_power", State: "absent", Actions: []string{"dc_off"}}}
	if rr := do(t, h, "DELETE", "/api/v1/rules/nope", "tok", ""); rr.Code != 404 {
		t.Fatalf("delete nonexistent: want 404, got %d", rr.Code)
	}
	if len(*saved) != 1 {
		t.Fatalf("unrelated 404 delete must not mutate rules: %+v", *saved)
	}
	if rr := do(t, h, "DELETE", "/api/v1/rules/gone", "tok", ""); rr.Code != 200 {
		t.Fatalf("delete: %d", rr.Code)
	}
	if len(*saved) != 0 {
		t.Fatalf("not deleted: %+v", *saved)
	}
}

// TestSSEStreamsSnapshot spins up a real HTTP server and verifies that a GET
// to /api/v1/events actually streams: the first "data: " frame received must
// be non-empty and unmarshal into a state.Snapshot (the initial frame the
// handler flushes immediately on connect, before blocking on the store's
// subscription channel).
func TestSSEStreamsSnapshot(t *testing.T) {
	h, store, _ := testServer(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	store.SetBattery(proto.Battery{Level: 42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("events status: %d", resp.StatusCode)
	}

	type result struct {
		line string
		err  error
	}
	lineCh := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				lineCh <- result{line: strings.TrimPrefix(line, "data: ")}
				return
			}
		}
		lineCh <- result{err: scanner.Err()}
	}()

	select {
	case res := <-lineCh:
		if res.err != nil {
			t.Fatalf("scan error: %v", res.err)
		}
		if res.line == "" {
			t.Fatalf("expected non-empty initial data frame")
		}
		var snap state.Snapshot
		if err := json.Unmarshal([]byte(res.line), &snap); err != nil {
			t.Fatalf("initial frame not a Snapshot: %v (line=%q)", err, res.line)
		}
		if snap.Battery == nil || snap.Battery.Level != 42 {
			t.Fatalf("unexpected initial snapshot: %+v", snap)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial SSE frame")
	}

	cancel()
}
