package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	controlpkg "github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type canonicalSession struct {
	store     *state.Store
	err       error
	limits    map[int]int
	threshold float64
}

func (f *canonicalSession) DCControl(on bool) error {
	if f.err != nil {
		return f.err
	}
	f.store.SetDC(proto.DCPort{Enabled: on})
	return nil
}
func (f *canonicalSession) TypeCOutput(on bool) error {
	if f.err != nil {
		return f.err
	}
	mode := uint8(1)
	if on {
		mode = 3
	}
	f.store.SetTypeC(proto.TypeCPort{Enabled: !on, Mode: mode})
	return nil
}
func (f *canonicalSession) BypassControl(on bool) error {
	if f.err != nil {
		return f.err
	}
	f.store.SetDC(proto.DCPort{Bypass: on})
	return nil
}
func (f *canonicalSession) GetUSBCLimit(typ int) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.limits[typ], nil
}
func (f *canonicalSession) PutUSBCLimit(typ, level int) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.limits[typ] = level
	return level, nil
}
func (f *canonicalSession) DeleteUSBCLimit(typ int) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.limits[typ] = 0
	return 0, nil
}
func (f *canonicalSession) BypassThreshold() (float64, error)  { return f.threshold, f.err }
func (f *canonicalSession) SetBypassThreshold(v float64) error { f.threshold = v; return f.err }
func (f *canonicalSession) PutBypassThreshold(v float64) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.threshold = v
	return v, nil
}
func (*canonicalSession) ListTimers() ([]proto.Timer, error)                { return nil, nil }
func (*canonicalSession) AddTimer(proto.Timer) ([]proto.Timer, byte, error) { return nil, 0, nil }
func (*canonicalSession) PutTimer(byte, proto.Timer) ([]proto.Timer, error) { return nil, nil }
func (*canonicalSession) DeleteTimer(byte) ([]proto.Timer, error)           { return nil, nil }
func (*canonicalSession) BarrierFree() (bool, error)                        { return false, nil }
func (*canonicalSession) SetBarrierFree(bool) (bool, error)                 { return false, nil }
func (*canonicalSession) SetRunningMode(byte) error                         { return nil }
func (*canonicalSession) USBFirmwareVersion() ([]byte, error)               { return nil, nil }
func (*canonicalSession) SetBLEPIN(uint32) error                            { return nil }
func (*canonicalSession) ReadClock() (time.Time, bool, error)               { return time.Time{}, false, nil }
func (*canonicalSession) SyncClock(time.Time, byte) error                   { return nil }
func (*canonicalSession) OTAInfo() (proto.OTAInfo, error)                   { return proto.OTAInfo{}, nil }
func (*canonicalSession) EnterOTA(context.Context) error                    { return nil }
func (*canonicalSession) ExitOTA(context.Context) error                     { return nil }
func (*canonicalSession) Restart() error                                    { return nil }
func (*canonicalSession) Shutdown() error                                   { return nil }

func canonicalServer(t *testing.T, connected, supported, advanced bool, sessionErr error) (http.Handler, *state.Store, *canonicalSession) {
	t.Helper()
	var fixtureSession *canonicalSession
	h, store, _ := testServerWith(t, func(d *Deps) {
		features := proto.FeatureSet{}
		chars := map[string]bool{}
		if supported {
			features = proto.FeatureSet{FactoryMode: true, Shutdown: true, DCOutControl: true, USBPort: true,
				USBPowerLimit: true, USBOutputControl: true, DCBypass: true, DCBypassControl: true}
			chars = map[string]bool{"command": true, "dc": true, "typec": true, "current_time": true, "ota": true, "factory": true}
		}
		d.Store.SetIdentity(state.Identity{Model: "BP4SL3V2", HWRev: "V2", AppFirmware: "1.4.9",
			BootloaderFirmware: "1.0.3", MAC: "DC:04:5A:EB:72:2B", CID: 773, Features: 4095,
			FeatureSet: features, Mode: "app", Characteristics: chars})
		phase := state.ConnectionDisconnected
		if connected {
			phase = state.ConnectionReady
		}
		d.Store.SetConnection(state.Connection{Phase: phase, ReconnectArmed: connected,
			Since: time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)})
		d.Store.SetConnected(connected)
		fake := &canonicalSession{store: d.Store, err: sessionErr,
			limits: map[int]int{proto.LimitGlobal: 4, proto.LimitInput: 3, proto.LimitOutput: 4, proto.LimitRuntime: -1}, threshold: 20}
		resolve := func() controlpkg.Session {
			if !connected {
				return nil
			}
			return fake
		}
		fixtureSession = fake
		d.DeviceControl = controlpkg.NewService(resolve, d.Store, nil, func() bool { return advanced })
		d.MagicDNSName = func() string { return "wattline.example.ts.net" }
	})
	return h, store, fixtureSession
}

func exactBody(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := strings.TrimSpace(rr.Body.String()); got != want {
		t.Fatalf("body\n got: %s\nwant: %s", got, want)
	}
}

func TestDeviceIdentityExactJSON(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	rr := do(t, h, http.MethodGet, "/api/v1/device", "tok", "")
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	want := `{"id":"DC:04:5A:EB:72:2B","model":"BP4SL3V2","hardware_revision":"V2","application_firmware":"1.4.9","ota_firmware":"1.0.3","cid":773,"features_raw":4095,"features":{"shutdown":true,"dc_bypass":true,"dc_bypass_control":true,"running_mode":true,"barrier_free":true,"usb_firmware":true,"ble_pin":true},"available":{"current_time":true,"ota":true,"dc":true,"usbc":true},"mode":"app","connection":{"connected":true,"phase":"ready","reconnect":"armed"},"commands":{"active":[],"recent":[]},"magic_dns_name":"wattline.example.ts.net"}`
	exactBody(t, rr, want)
}

func TestDeviceCachedWhileDisconnected(t *testing.T) {
	h, _, _ := canonicalServer(t, false, true, true, nil)
	rr := do(t, h, http.MethodGet, "/api/v1/device", "tok", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var got deviceView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "DC:04:5A:EB:72:2B" || got.Connection.Connected {
		t.Fatalf("cached device: %+v", got)
	}
}

func TestCanonicalErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{controlpkg.ErrDisconnected, 503, "device_disconnected"},
		{controlpkg.ErrUnsupported, 409, "capability_unsupported"},
		{controlpkg.ErrAdvancedDisabled, 403, "advanced_disabled"},
		{controlpkg.ErrTimeout, 504, "command_timeout"},
		{controlpkg.ErrNotFound, 404, "not_found"},
		{errors.New("gatt"), 502, "ble_operation_failed"},
	}
	for _, tt := range tests {
		rr := httptest.NewRecorder()
		writeError(rr, tt.err)
		if rr.Code != tt.status {
			t.Fatalf("%v status %d, want %d", tt.err, rr.Code, tt.status)
		}
		var body apiErrorBody
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != tt.code || body.Error.Details == nil {
			t.Fatalf("%v body: %+v", tt.err, body)
		}
	}
}

func TestCanonicalErrorResponses(t *testing.T) {
	bleErr := errors.New("gatt write failed")
	tests := []struct {
		name                           string
		connected, supported, advanced bool
		sessionErr                     error
		method, path, token, body      string
		status                         int
		want                           string
	}{
		{"unauthorized", true, true, true, nil, "GET", "/api/v1/device", "", "", 401, `{"error":{"code":"unauthorized","message":"Bearer token is missing or invalid","details":{}}}`},
		{"disconnected", false, true, true, nil, "POST", "/api/v1/device/dc", "tok", `{"enabled":true}`, 503, `{"error":{"code":"device_disconnected","message":"Link-Power is not connected","details":{}}}`},
		{"unsupported", true, false, true, nil, "POST", "/api/v1/device/dc", "tok", `{"enabled":true}`, 409, `{"error":{"code":"capability_unsupported","message":"Operation is not supported","details":{}}}`},
		{"advanced disabled", true, true, false, nil, "GET", "/api/v1/device/dc/bypass/threshold", "tok", "", 403, `{"error":{"code":"advanced_disabled","message":"Advanced operations are disabled","details":{}}}`},
		{"ble failure", true, true, true, bleErr, "GET", "/api/v1/device/usbc/limit/output", "tok", "", 502, `{"error":{"code":"ble_operation_failed","message":"BLE operation failed","details":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := canonicalServer(t, tt.connected, tt.supported, tt.advanced, tt.sessionErr)
			rr := do(t, h, tt.method, tt.path, tt.token, tt.body)
			if rr.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.status, rr.Body.String())
			}
			exactBody(t, rr, tt.want)
		})
	}
}

func TestDeviceCanonicalRequestValidation(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	tests := []struct{ name, method, path, body string }{
		{"missing", "POST", "/api/v1/device/dc", ""},
		{"malformed", "POST", "/api/v1/device/dc", `{"enabled":`},
		{"unknown", "POST", "/api/v1/device/dc", `{"enabled":true,"extra":1}`},
		{"trailing", "POST", "/api/v1/device/dc", `{"enabled":true}{}`},
		{"missing field", "POST", "/api/v1/device/dc", `{}`},
		{"bad limit type", "GET", "/api/v1/device/usbc/limit/bogus", ""},
		{"runtime put", "PUT", "/api/v1/device/usbc/limit/runtime", `{"watts":100}`},
		{"runtime delete", "DELETE", "/api/v1/device/usbc/limit/runtime", ""},
		{"bad watts", "PUT", "/api/v1/device/usbc/limit/output", `{"watts":99}`},
		{"threshold missing", "PUT", "/api/v1/device/dc/bypass/threshold", `{}`},
		{"threshold zero", "PUT", "/api/v1/device/dc/bypass/threshold", `{"volts":0}`},
		{"device body", "GET", "/api/v1/device", `{}`},
		{"limit get body", "GET", "/api/v1/device/usbc/limit/output", `{}`},
		{"limit delete body", "DELETE", "/api/v1/device/usbc/limit/output", `{}`},
		{"threshold get body", "GET", "/api/v1/device/dc/bypass/threshold", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := do(t, h, tt.method, tt.path, "tok", tt.body)
			if rr.Code != 400 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			exactBody(t, rr, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
		})
	}
}

func TestDCTypeCBypassLimitCanonicalRoutes(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	tests := []struct {
		method, path, body string
		check              func(*testing.T, map[string]any)
	}{
		{"POST", "/api/v1/device/dc", `{"enabled":true}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != true {
				t.Fatal(v)
			}
			terminalCommand(t, v, "dc_output")
		}},
		{"POST", "/api/v1/device/usbc/output", `{"enabled":false}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != false || v["mode"] != float64(1) {
				t.Fatal(v)
			}
			terminalCommand(t, v, "usbc_output")
		}},
		{"POST", "/api/v1/device/dc/bypass", `{"enabled":true}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != true {
				t.Fatal(v)
			}
			terminalCommand(t, v, "dc_bypass")
		}},
		{"GET", "/api/v1/device/usbc/limit/output", "", func(t *testing.T, v map[string]any) {
			if v["type"] != "output" || v["level"] != float64(4) || v["watts"] != float64(100) {
				t.Fatal(v)
			}
		}},
		{"GET", "/api/v1/device/usbc/limit/runtime", "", func(t *testing.T, v map[string]any) {
			if v["level"] != float64(-1) || v["watts"] != nil {
				t.Fatal(v)
			}
		}},
		{"PUT", "/api/v1/device/usbc/limit/output", `{"watts":140}`, func(t *testing.T, v map[string]any) {
			if v["level"] != float64(5) || v["watts"] != float64(140) {
				t.Fatal(v)
			}
		}},
		{"DELETE", "/api/v1/device/usbc/limit/output", "", func(t *testing.T, v map[string]any) {
			if v["level"] != float64(0) || v["watts"] != float64(30) {
				t.Fatal(v)
			}
		}},
		{"GET", "/api/v1/device/dc/bypass/threshold", "", func(t *testing.T, v map[string]any) {
			if v["volts"] != float64(20) {
				t.Fatal(v)
			}
		}},
		{"PUT", "/api/v1/device/dc/bypass/threshold", `{"volts":19.6}`, func(t *testing.T, v map[string]any) {
			if v["volts"] != 19.6 {
				t.Fatal(v)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rr := do(t, h, tt.method, tt.path, "tok", tt.body)
			if rr.Code != 200 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			tt.check(t, got)
		})
	}
}

func terminalCommand(t *testing.T, v map[string]any, operation string) {
	t.Helper()
	cmd, ok := v["command"].(map[string]any)
	if !ok {
		t.Fatalf("command: %#v", v["command"])
	}
	if cmd["operation"] != operation || cmd["phase"] != state.CommandConfirmed || cmd["error"] != nil {
		t.Fatalf("command: %#v", cmd)
	}
}

func TestSSEPendingThenConfirmedCompatibility(t *testing.T) {
	h, store, _ := canonicalServer(t, true, true, true, nil)
	store.SetBattery(proto.Battery{Level: 42})
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	read := func() map[string]any {
		t.Helper()
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				var v map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
		}
		t.Fatalf("stream ended: %v", scanner.Err())
		return nil
	}
	initial := read()
	if initial["battery"] == nil || initial["connected"] == nil || initial["updated_at"] == nil {
		t.Fatalf("legacy telemetry fields missing: %#v", initial)
	}
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	store.BeginCommand(state.Command{ID: "cmd-1", Operation: "dc_output", Requested: map[string]any{"enabled": true}, StartedAt: now, UpdatedAt: now})
	pending := read()
	commands := pending["commands"].(map[string]any)
	if len(commands["active"].([]any)) != 1 {
		t.Fatalf("pending frame: %#v", pending)
	}
	store.FinishCommand("cmd-1", state.CommandConfirmed, proto.DCPort{Enabled: true}, nil)
	confirmed := read()
	commands = confirmed["commands"].(map[string]any)
	if len(commands["active"].([]any)) != 0 || len(commands["recent"].([]any)) != 1 {
		t.Fatalf("confirmed frame: %#v", confirmed)
	}
}
