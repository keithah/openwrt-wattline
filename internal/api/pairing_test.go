package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/ble"
)

type scriptedOps struct {
	devices   []ble.Found
	scanErr   error
	pairErr   error
	trustErr  error
	unpairErr error
	block     chan struct{}
	pairBlock chan struct{}
	mu        sync.Mutex
	recover   bool
}

func (s *scriptedOps) Scan(time.Duration) ([]ble.Found, error) {
	if s.block != nil {
		<-s.block
	}
	return s.devices, s.scanErr
}
func (s *scriptedOps) Pair(_ string, recover bool, report ble.PairProgress) error {
	s.mu.Lock()
	s.recover = recover
	s.mu.Unlock()
	if recover {
		report(ble.PhaseClearingStaleBond, "Clearing the router's stale pairing record")
	}
	if s.pairBlock != nil {
		<-s.pairBlock
	}
	return s.pairErr
}
func (s *scriptedOps) Trust(string) error  { return s.trustErr }
func (s *scriptedOps) Unpair(string) error { return s.unpairErr }

func pairingServer(t *testing.T, ops ble.PairOps) http.Handler {
	h, _, _ := testServerWith(t, func(d *Deps) {
		d.Pairing = ble.NewPairing(ble.PairingDeps{Ops: ops, ScanFor: time.Millisecond})
	})
	return h
}

func waitStage(t *testing.T, h http.Handler, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w := do(t, h, "GET", "/api/v1/pairing/status", "tok", "")
		if w.Code != 200 {
			t.Fatalf("status code %d", w.Code)
		}
		var got map[string]any
		json.Unmarshal(w.Body.Bytes(), &got)
		if got["stage"] == want {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("never reached stage %q", want)
	return nil
}

func TestPairingScanFlow(t *testing.T) {
	ops := &scriptedOps{devices: []ble.Found{{MAC: "DC:04:5A:EB:72:2B", Name: "Link-Power-2", RSSI: -60}}}
	h := pairingServer(t, ops)
	if w := do(t, h, "POST", "/api/v1/pairing/scan", "tok", ""); w.Code != 202 {
		t.Fatalf("scan code = %d", w.Code)
	}
	got := waitStage(t, h, "idle")
	devs := got["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["name"] != "Link-Power-2" {
		t.Fatalf("devices = %v", devs)
	}
}

func TestPairingIdleStatusOmitsOperationMetadata(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	w := do(t, h, http.MethodGet, "/api/v1/pairing/status", "tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"started_at", "updated_at", "elapsed_ms", "events"} {
		if value, ok := got[key]; ok {
			t.Fatalf("idle status includes %s=%v", key, value)
		}
	}
}

func TestPairingScanBusyIs409(t *testing.T) {
	ops := &scriptedOps{block: make(chan struct{})}
	h := pairingServer(t, ops)
	defer close(ops.block)
	if w := do(t, h, "POST", "/api/v1/pairing/scan", "tok", ""); w.Code != 202 {
		t.Fatalf("first scan = %d", w.Code)
	}
	waitStage(t, h, "scanning")
	if w := do(t, h, "POST", "/api/v1/pairing/scan", "tok", ""); w.Code != 409 {
		t.Fatalf("busy scan = %d, want 409", w.Code)
	} else {
		exactBody(t, w, `{"error":{"code":"operation_in_progress","message":"Pairing operation already in progress","details":{}}}`)
	}
}

func TestPairingPairFlow(t *testing.T) {
	ops := &scriptedOps{}
	h := pairingServer(t, ops)
	body := `{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}`
	if w := do(t, h, "POST", "/api/v1/pairing/pair", "tok", body); w.Code != 202 {
		t.Fatalf("pair code = %d", w.Code)
	}
	got := waitStage(t, h, "paired")
	if got["target"] != "DC:04:5A:EB:72:2B" {
		t.Fatalf("target = %v", got["target"])
	}
}

func TestPairingRecoverFlow(t *testing.T) {
	ops := &scriptedOps{}
	h := pairingServer(t, ops)
	body := `{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}`
	if w := do(t, h, "POST", "/api/v1/pairing/recover", "tok", body); w.Code != 202 {
		t.Fatalf("recover code = %d body=%s", w.Code, w.Body.String())
	}
	got := waitStage(t, h, "paired")
	if got["target"] != "DC:04:5A:EB:72:2B" || got["phase"] != "complete" {
		t.Fatalf("recover status = %v", got)
	}
	ops.mu.Lock()
	defer ops.mu.Unlock()
	if !ops.recover {
		t.Fatal("recovery flag did not reach PairOps")
	}
}

func TestPairingRecoverBusyIs409(t *testing.T) {
	ops := &scriptedOps{pairBlock: make(chan struct{})}
	h := pairingServer(t, ops)
	defer close(ops.pairBlock)
	body := `{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}`
	if w := do(t, h, "POST", "/api/v1/pairing/recover", "tok", body); w.Code != 202 {
		t.Fatalf("recover code = %d", w.Code)
	}
	waitStage(t, h, "pairing")
	if w := do(t, h, "POST", "/api/v1/pairing/recover", "tok", body); w.Code != 409 {
		t.Fatalf("busy recover = %d, want 409", w.Code)
	}
}

func TestPairingPairValidatesMAC(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, path := range []string{"/api/v1/pairing/pair", "/api/v1/pairing/recover"} {
		for _, body := range []string{`{}`, `{"mac":"nonsense"}`, `not json`,
			`{"mac":"DC:04:5A:EB:72:2B","pin":"junk"}`,
			`{"mac":"DC:04:5A:EB:72:2B","pin":"0123456"}`} {
			if w := do(t, h, "POST", path, "tok", body); w.Code != 400 {
				t.Fatalf("%s body %q -> %d, want 400", path, body, w.Code)
			} else {
				exactBody(t, w, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
			}
		}
	}
}

func TestPairingPairUsesExactJSON(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, path := range []string{"/api/v1/pairing/pair", "/api/v1/pairing/recover"} {
		for _, body := range []string{
			`{"mac":"DC:04:5A:EB:72:2B","pin":"020555","extra":true}`,
			`{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}{}`,
		} {
			w := do(t, h, http.MethodPost, path, "tok", body)
			if w.Code != 400 {
				t.Fatalf("%s body %q status %d", path, body, w.Code)
			}
			exactBody(t, w, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
		}
	}
}

func TestPairingBodylessRoutesRejectBodies(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/pairing/status"},
		{http.MethodPost, "/api/v1/pairing/scan"},
		{http.MethodDelete, "/api/v1/pairing/device/DC:04:5A:EB:72:2B"},
	} {
		w := do(t, h, tc.method, tc.path, "tok", `{}`)
		if w.Code != 400 {
			t.Fatalf("%s %s status %d", tc.method, tc.path, w.Code)
		}
		exactBody(t, w, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
	}
}

func TestPairingCanonicalErrorsDoNotLeakOperations(t *testing.T) {
	wantBLE := `{"error":{"code":"ble_operation_failed","message":"BLE operation failed","details":{}}}`
	h := pairingServer(t, &scriptedOps{unpairErr: errors.New("secret dbus name")})
	w := do(t, h, http.MethodDelete, "/api/v1/pairing/device/DC:04:5A:EB:72:2B", "tok", "")
	if w.Code != 502 {
		t.Fatalf("unpair status %d", w.Code)
	}
	exactBody(t, w, wantBLE)
}

func TestPairingUnpair(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	if w := do(t, h, "DELETE", "/api/v1/pairing/device/DC:04:5A:EB:72:2B", "tok", ""); w.Code != 200 {
		t.Fatalf("unpair = %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/v1/pairing/device/junk", "tok", ""); w.Code != 400 {
		t.Fatalf("bad mac unpair = %d, want 400", w.Code)
	}
}

func TestPairingUnavailableIsCapabilityUnsupported(t *testing.T) {
	h, _, _ := testServerWith(t, nil) // no Pairing configured
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/pairing/scan"},
		{"GET", "/api/v1/pairing/status"},
		{"POST", "/api/v1/pairing/pair"},
		{"POST", "/api/v1/pairing/recover"},
		{"DELETE", "/api/v1/pairing/device/DC:04:5A:EB:72:2B"},
	} {
		if w := do(t, h, c.method, c.path, "tok", ""); w.Code != 409 {
			t.Fatalf("%s %s = %d, want 409", c.method, c.path, w.Code)
		} else {
			exactBody(t, w, `{"error":{"code":"capability_unsupported","message":"Operation is not supported","details":{}}}`)
		}
	}
}

func TestPairingRequiresAuth(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, path := range []string{"/api/v1/pairing/scan", "/api/v1/pairing/recover"} {
		if w := do(t, h, "POST", path, "", ""); w.Code != 401 {
			t.Fatalf("unauthed %s = %d, want 401", path, w.Code)
		}
	}
}
