package api

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	controlpkg "github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type canonicalSession struct {
	store                                      *state.Store
	err                                        error
	limits                                     map[int]int
	threshold                                  float64
	timers                                     []proto.Timer
	getTimerCalls                              int
	nextTimer                                  byte
	clockTime                                  time.Time
	clockOK                                    bool
	clockReads                                 int
	clockSyncReason                            byte
	barrier                                    bool
	runningMode                                byte
	usbFirmware                                []byte
	blePIN                                     uint32
	shutdownCalls                              int
	savedPIN                                   string
	pinOrder                                   []string
	savePINError                               error
	otaInfo                                    proto.OTAInfo
	restarted, shutdown, enteredOTA, exitedOTA bool
	entered                                    chan string
}

type orderedDCSession struct {
	*canonicalSession
	entered chan bool
	release map[bool]chan struct{}
}

func (f *orderedDCSession) DCControl(on bool) error {
	f.entered <- on
	<-f.release[on]
	f.store.SetDC(proto.DCPort{Enabled: on})
	return nil
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
func (f *canonicalSession) ListTimers() ([]proto.Timer, error) {
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *canonicalSession) GetTimer(id byte) (proto.Timer, error) {
	f.getTimerCalls++
	if f.err != nil {
		return proto.Timer{}, f.err
	}
	for _, timer := range f.timers {
		if timer.ID == id {
			return timer, nil
		}
	}
	return proto.Timer{}, proto.ErrTimerNotFound
}
func (f *canonicalSession) AddTimer(timer proto.Timer) ([]proto.Timer, byte, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	timer.ID = f.nextTimer
	f.nextTimer++
	f.timers = append(f.timers, timer)
	return append([]proto.Timer(nil), f.timers...), timer.ID, nil
}
func (f *canonicalSession) PutTimer(id byte, timer proto.Timer) ([]proto.Timer, error) {
	if f.err != nil {
		return nil, f.err
	}
	found := false
	for i := range f.timers {
		if f.timers[i].ID == id {
			found = true
			timer.ID = id
			f.timers[i] = timer
		}
	}
	if !found {
		return nil, proto.ErrTimerNotFound
	}
	return append([]proto.Timer(nil), f.timers...), nil
}
func (f *canonicalSession) DeleteTimer(id byte) ([]proto.Timer, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.timers[:0]
	found := false
	for _, timer := range f.timers {
		if timer.ID != id {
			out = append(out, timer)
		} else {
			found = true
		}
	}
	if !found {
		return nil, proto.ErrTimerNotFound
	}
	f.timers = out
	return append([]proto.Timer(nil), f.timers...), nil
}
func (f *canonicalSession) BarrierFree() (bool, error)           { return f.barrier, f.err }
func (f *canonicalSession) SetBarrierFree(on bool) (bool, error) { f.barrier = on; return on, f.err }
func (f *canonicalSession) SetRunningMode(mode byte) error       { f.runningMode = mode; return f.err }
func (f *canonicalSession) USBFirmwareVersion() ([]byte, error)  { return f.usbFirmware, f.err }
func (f *canonicalSession) SetBLEPIN(pin uint32) error {
	f.pinOrder = append(f.pinOrder, "ble")
	f.blePIN = pin
	return f.err
}
func (f *canonicalSession) ReadClock() (time.Time, bool, error) {
	if f.entered != nil {
		f.entered <- "clock-read"
	}
	if device := f.store.Snapshot().Device; device != nil && !device.Characteristics["current_time"] {
		return time.Time{}, false, nil
	}
	f.clockReads++
	return f.clockTime, f.clockOK, f.err
}
func (f *canonicalSession) SyncClock(_ time.Time, reason byte) error {
	if f.entered != nil {
		f.entered <- "clock-sync"
	}
	f.clockSyncReason = reason
	return f.err
}
func (f *canonicalSession) OTAInfo() (proto.OTAInfo, error) {
	if f.entered != nil {
		f.entered <- "ota-info"
	}
	return f.otaInfo, f.err
}
func (f *canonicalSession) EnterOTA(context.Context) error {
	if f.entered != nil {
		f.entered <- "ota-enter"
	}
	f.enteredOTA = true
	if f.err == nil {
		device := *f.store.Snapshot().Device
		device.Mode = "ota"
		f.store.SetIdentity(device)
	}
	return f.err
}
func (f *canonicalSession) ExitOTA(context.Context) error {
	if f.entered != nil {
		f.entered <- "ota-exit"
	}
	f.exitedOTA = true
	if f.err == nil {
		device := *f.store.Snapshot().Device
		device.Mode = "app"
		f.store.SetIdentity(device)
	}
	return f.err
}
func (f *canonicalSession) Restart() error { f.restarted = true; return f.err }
func (f *canonicalSession) Shutdown() error {
	f.shutdown = true
	f.shutdownCalls++
	return f.err
}

func canonicalServer(t *testing.T, connected, supported, advanced bool, sessionErr error, saveOverride ...func(string) error) (http.Handler, *state.Store, *canonicalSession) {
	t.Helper()
	var fixtureSession *canonicalSession
	h, store, _ := testServerWith(t, func(d *Deps) {
		features := proto.FeatureSet{}
		var featureBits uint32
		chars := map[string]bool{}
		if supported {
			featureBits = 0x7fff
			features = proto.DecodeFeatures(featureBits)
			chars = map[string]bool{"command": true, "dc": true, "typec": true, "current_time": true, "ota": true, "factory": true}
		}
		d.Store.SetIdentity(state.Identity{Model: "BP4SL3V2", HWRev: "V2", AppFirmware: "1.4.9",
			BootloaderFirmware: "1.0.3", MAC: "DC:04:5A:EB:72:2B", CID: 773, Features: featureBits,
			FeatureSet: features, Mode: "app", Characteristics: chars})
		phase := state.ConnectionDisconnected
		if connected {
			phase = state.ConnectionReady
		}
		d.Store.SetConnection(state.Connection{Phase: phase, ReconnectArmed: connected,
			Since: time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)})
		d.Store.SetConnected(connected)
		fake := &canonicalSession{store: d.Store, err: sessionErr,
			limits: map[int]int{proto.LimitGlobal: 4, proto.LimitInput: 3, proto.LimitOutput: 4, proto.LimitRuntime: -1}, threshold: 20,
			nextTimer: 3, clockTime: time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC), clockOK: true,
			usbFirmware: []byte{1, 4, 9}, otaInfo: proto.OTAInfo{Mode: 1, CID: 773, Revision: 3}}
		resolve := func() controlpkg.Session {
			if !connected {
				return nil
			}
			return fake
		}
		fixtureSession = fake
		d.DeviceControl = controlpkg.NewService(resolve, d.Store, nil, func() bool { return advanced })
		if len(saveOverride) > 0 {
			d.SaveBLEPIN = saveOverride[0]
		} else {
			d.SaveBLEPIN = func(pin string) error {
				fake.pinOrder = append(fake.pinOrder, "save")
				fake.savedPIN = pin
				return fake.savePINError
			}
		}
		d.MagicDNSName = func() string { return "wattline.example.ts.net" }
		d.Now = func() time.Time { return time.Date(2026, 7, 17, 20, 0, 2, 0, time.UTC) }
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
	want := `{"id":"DC:04:5A:EB:72:2B","model":"BP4SL3V2","hardware_revision":"V2","application_firmware":"1.4.9","ota_firmware":"1.0.3","cid":773,"features_raw":32767,"features":{"display":true,"factory_mode":true,"sleep":true,"shutdown":true,"battery_capacity":true,"dc_out_port":true,"dc_out_control":true,"dc_out_scheduler":true,"usb_port":true,"usb_power_limit":true,"usb_output_control":true,"dc_bypass":true,"dc_bypass_control":true,"usb_dc_input":true,"usb_dc_input_power":true,"running_mode":true,"barrier_free":true,"usb_firmware":true,"ble_pin":true},"available":{"current_time":true,"ota":true,"dc":true,"usbc":true},"mode":"app","connection":{"connected":true,"phase":"ready","reconnect":"armed"},"commands":{"active":[],"recent":[]},"magic_dns_name":"wattline.example.ts.net"}`
	exactBody(t, rr, want)
}

func TestDeviceIdentityMapsEveryDecodedFeatureBit(t *testing.T) {
	h, store, _ := canonicalServer(t, true, true, true, nil)
	featureNames := []string{
		"display", "factory_mode", "sleep", "shutdown", "battery_capacity",
		"dc_out_port", "dc_out_control", "dc_out_scheduler", "usb_port",
		"usb_power_limit", "usb_output_control", "dc_bypass", "dc_bypass_control",
		"usb_dc_input", "usb_dc_input_power",
	}
	for bit, wantName := range featureNames {
		t.Run(wantName, func(t *testing.T) {
			identity := *store.Snapshot().Device
			identity.Features = 1 << bit
			identity.FeatureSet = proto.DecodeFeatures(identity.Features)
			store.SetIdentity(identity)
			rr := do(t, h, http.MethodGet, "/api/v1/device", "tok", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var body struct {
				Features map[string]bool `json:"features"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for _, name := range featureNames {
				if got, want := body.Features[name], name == wantName; got != want {
					t.Errorf("bit %d feature %q = %t, want %t", bit, name, got, want)
				}
			}
		})
	}
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
		{fmt.Errorf("%w: gatt", controlpkg.ErrBLE), 502, "ble_operation_failed"},
		{fmt.Errorf("%w: %w", controlpkg.ErrBLE, controlpkg.ErrDisconnected), 502, "ble_operation_failed"},
		{fmt.Errorf("%w: entropy", controlpkg.ErrInternal), 500, "internal_error"},
		{errors.New("unknown"), 500, "internal_error"},
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

func TestDCCommandIDGenerationFailureIsInternalError(t *testing.T) {
	h, _, _ := canonicalServer(t, true, true, true, nil)
	original := cryptorand.Reader
	cryptorand.Reader = failingReader{err: errors.New("secret entropy detail")}
	defer func() { cryptorand.Reader = original }()
	rr := do(t, h, http.MethodPost, "/api/v1/device/dc", "tok", `{"on":true}`)
	if rr.Code != 500 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	exactBody(t, rr, `{"error":{"code":"internal_error","message":"Internal server error","details":{}}}`)
	for _, path := range []string{"/api/v1/device", "/api/v1/telemetry"} {
		cached := do(t, h, http.MethodGet, path, "tok", "")
		if strings.Contains(cached.Body.String(), "secret entropy detail") {
			t.Fatalf("%s leaked entropy failure: %s", path, cached.Body.String())
		}
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			if strings.Contains(scanner.Text(), "secret entropy detail") {
				t.Fatalf("SSE leaked entropy failure: %s", scanner.Text())
			}
			return
		}
	}
	t.Fatalf("SSE ended without data frame: %v", scanner.Err())
}

func TestDCBLEFailureNeverLeaksThroughCommandState(t *testing.T) {
	const secret = "SECRET-GATT-/org/bluez/hci0/dev"
	h, _, _ := canonicalServer(t, true, true, true, errors.New(secret))
	immediate := do(t, h, http.MethodPost, "/api/v1/device/dc", "tok", `{"on":true}`)
	if immediate.Code != 502 {
		t.Fatalf("status %d: %s", immediate.Code, immediate.Body.String())
	}
	exactBody(t, immediate, `{"error":{"code":"ble_operation_failed","message":"BLE operation failed","details":{}}}`)
	wantCommandError := `"error":{"code":"ble_operation_failed","message":"BLE operation failed"}`
	for _, path := range []string{"/api/v1/device", "/api/v1/telemetry"} {
		cached := do(t, h, http.MethodGet, path, "tok", "")
		body := cached.Body.String()
		if strings.Contains(body, secret) || strings.Contains(body, "GATT") {
			t.Fatalf("%s leaked transport error: %s", path, body)
		}
		if !strings.Contains(body, wantCommandError) {
			t.Fatalf("%s missing canonical command error: %s", path, body)
		}
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if strings.Contains(line, secret) || strings.Contains(line, "GATT") {
			t.Fatalf("SSE leaked transport error: %s", line)
		}
		if !strings.Contains(line, wantCommandError) {
			t.Fatalf("SSE missing canonical command error: %s", line)
		}
		return
	}
	t.Fatalf("SSE ended without data frame: %v", scanner.Err())
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
		{"disconnected", false, true, true, nil, "POST", "/api/v1/device/dc", "tok", `{"on":true}`, 503, `{"error":{"code":"device_disconnected","message":"Link-Power is not connected","details":{}}}`},
		{"unsupported", true, false, true, nil, "POST", "/api/v1/device/dc", "tok", `{"on":true}`, 409, `{"error":{"code":"capability_unsupported","message":"Operation is not supported","details":{}}}`},
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
		{"malformed", "POST", "/api/v1/device/dc", `{"on":`},
		{"unknown", "POST", "/api/v1/device/dc", `{"on":true,"extra":1}`},
		{"enabled rejected", "POST", "/api/v1/device/dc", `{"enabled":true}`},
		{"trailing", "POST", "/api/v1/device/dc", `{"on":true}{}`},
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
		{"POST", "/api/v1/device/dc", `{"on":true}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != true {
				t.Fatal(v)
			}
			terminalCommand(t, v, "dc_output", true)
		}},
		{"POST", "/api/v1/device/usbc/output", `{"on":false}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != false || v["mode"] != float64(1) {
				t.Fatal(v)
			}
			terminalCommand(t, v, "usbc_output", false)
		}},
		{"POST", "/api/v1/device/dc/bypass", `{"on":true}`, func(t *testing.T, v map[string]any) {
			if v["enabled"] != true {
				t.Fatal(v)
			}
			terminalCommand(t, v, "dc_bypass", true)
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

func terminalCommand(t *testing.T, v map[string]any, operation string, on bool) {
	t.Helper()
	cmd, ok := v["command"].(map[string]any)
	if !ok {
		t.Fatalf("command: %#v", v["command"])
	}
	if cmd["operation"] != operation || cmd["phase"] != state.CommandConfirmed || cmd["error"] != nil {
		t.Fatalf("command: %#v", cmd)
	}
	requested, ok := cmd["requested"].(map[string]any)
	if !ok || requested["on"] != on {
		t.Fatalf("requested: %#v", cmd["requested"])
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
	store.BeginCommand(state.Command{ID: "cmd-1", Operation: "dc_output", Requested: map[string]any{"on": true}, StartedAt: now, UpdatedAt: now})
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

func TestDCConcurrentResponsesKeepExactCommandAssociation(t *testing.T) {
	var session *orderedDCSession
	h, store, _ := testServerWith(t, func(d *Deps) {
		d.Store.SetConnected(true)
		d.Store.SetIdentity(state.Identity{Mode: "app", FeatureSet: proto.FeatureSet{DCOutControl: true}, Characteristics: map[string]bool{"command": true, "dc": true}})
		base := &canonicalSession{store: d.Store, limits: map[int]int{}}
		session = &orderedDCSession{canonicalSession: base, entered: make(chan bool, 2), release: map[bool]chan struct{}{true: make(chan struct{}), false: make(chan struct{})}}
		d.DeviceControl = controlpkg.NewService(func() controlpkg.Session { return session }, d.Store, nil, func() bool { return true })
	})
	store.SetDC(proto.DCPort{Enabled: false})
	type response struct {
		requested bool
		rr        *httptest.ResponseRecorder
	}
	responses := make(chan response, 2)
	for _, on := range []bool{true, false} {
		on := on
		go func() {
			responses <- response{on, do(t, h, http.MethodPost, "/api/v1/device/dc", "tok", fmt.Sprintf(`{"on":%t}`, on))}
		}()
	}
	seen := map[bool]bool{<-session.entered: true, <-session.entered: true}
	if !seen[true] || !seen[false] {
		t.Fatalf("entered: %+v", seen)
	}
	close(session.release[false])
	first := <-responses
	close(session.release[true])
	second := <-responses
	ids := map[string]bool{}
	for _, response := range []response{first, second} {
		if response.rr.Code != 200 {
			t.Fatalf("request %t status %d: %s", response.requested, response.rr.Code, response.rr.Body.String())
		}
		var body struct {
			Command commandView `json:"command"`
		}
		if err := json.Unmarshal(response.rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		requested := body.Command.Requested.(map[string]any)["on"]
		if requested != response.requested {
			t.Fatalf("request %t received command %+v", response.requested, body.Command)
		}
		if ids[body.Command.ID] {
			t.Fatalf("duplicate command ID %q", body.Command.ID)
		}
		ids[body.Command.ID] = true
	}
}
