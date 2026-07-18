package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type eofSignalReader struct {
	reader *strings.Reader
	once   sync.Once
	done   chan struct{}
}

func (r *eofSignalReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		r.once.Do(func() { close(r.done) })
	}
	return n, err
}

func testServer(t *testing.T) (http.Handler, *state.Store, *[]config.Rule) {
	return testServerWith(t, nil)
}

// testServerWith builds the standard test server, letting a test adjust Deps
// (e.g. attach a Pairing manager) before the handler is constructed.
func testServerWith(t *testing.T, mutate func(*Deps)) (http.Handler, *state.Store, *[]config.Rule) {
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
	if mutate != nil {
		mutate(&d)
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

func TestAuthRequiresExactBearerScheme(t *testing.T) {
	h, _, _ := testServer(t)
	for _, header := range []string{"tok", "Basic tok", "bearer tok", "Bearer", "Bearer ", " Bearer tok"} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
			req.Header.Set("Authorization", header)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%q status %d", header, rr.Code)
			}
			exactBody(t, rr, `{"error":{"code":"unauthorized","message":"Bearer token is missing or invalid","details":{}}}`)
		})
	}
}

func TestCanonicalErrorCompatibilityRoutes(t *testing.T) {
	h, _, _ := testServer(t)
	tests := []struct {
		name, method, path, body string
		status                   int
		want                     string
	}{
		{"invalid action", "POST", "/api/v1/device/action", `{"action":"bogus"}`, 400, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`},
		{"missing rule", "DELETE", "/api/v1/rules/missing", "", 404, `{"error":{"code":"not_found","message":"Resource was not found","details":{}}}`},
		{"disconnected alias", "GET", "/api/v1/device/usbc-limit", "", 503, `{"error":{"code":"device_disconnected","message":"Link-Power is not connected","details":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := do(t, h, tt.method, tt.path, "tok", tt.body)
			if rr.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.status, rr.Body.String())
			}
			exactBody(t, rr, tt.want)
		})
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

func TestConcurrentRuleHandlersSerializeLoadModifySave(t *testing.T) {
	var dataMu sync.Mutex
	saved := []config.Rule{{Name: "removed", Condition: "input_power", State: "absent", Actions: []string{"dc_off"}}}
	s := &server{d: Deps{
		LoadRules: func() []config.Rule {
			dataMu.Lock()
			defer dataMu.Unlock()
			return append([]config.Rule(nil), saved...)
		},
		SaveRules: func(rules []config.Rule) error {
			dataMu.Lock()
			defer dataMu.Unlock()
			saved = append([]config.Rule(nil), rules...)
			return nil
		},
	}}

	s.rulesMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			s.rulesMu.Unlock()
		}
	})

	type result struct {
		response *httptest.ResponseRecorder
		bodyDone <-chan struct{}
		callDone <-chan struct{}
	}
	start := func(name string) result {
		bodyDone := make(chan struct{})
		callDone := make(chan struct{})
		body := `{"name":"` + name + `","condition":"input_power","state":"absent","actions":["dc_off"]}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/rules", &eofSignalReader{
			reader: strings.NewReader(body),
			done:   bodyDone,
		})
		response := httptest.NewRecorder()
		go func() {
			s.postRule(response, request)
			close(callDone)
		}()
		return result{response: response, bodyDone: bodyDone, callDone: callDone}
	}

	first := start("first")
	second := start("second")
	deleteDone := make(chan struct{})
	deleteStarted := make(chan struct{})
	deleteResponse := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/rules/removed", nil)
	deleteRequest.SetPathValue("name", "removed")
	go func() {
		close(deleteStarted)
		s.deleteRule(deleteResponse, deleteRequest)
		close(deleteDone)
	}()
	<-first.bodyDone
	<-second.bodyDone
	<-deleteStarted
	s.rulesMu.Unlock()
	locked = false

	for _, call := range []result{first, second} {
		<-call.callDone
		if call.response.Code != http.StatusOK {
			t.Fatalf("concurrent rule response %d: %s", call.response.Code, call.response.Body.String())
		}
	}
	<-deleteDone
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("concurrent delete response %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	dataMu.Lock()
	defer dataMu.Unlock()
	if len(saved) != 2 {
		t.Fatalf("concurrent rules saved %+v, want two updates and the delete preserved", saved)
	}
	names := map[string]bool{}
	for _, rule := range saved {
		names[rule.Name] = true
	}
	if !names["first"] || !names["second"] || names["removed"] {
		t.Fatalf("concurrent rules saved %+v, want first and second only", saved)
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
