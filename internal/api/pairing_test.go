package api

import (
	"encoding/json"
	"errors"
	"net/http"
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
}

func (s *scriptedOps) Scan(time.Duration) ([]ble.Found, error) {
	if s.block != nil {
		<-s.block
	}
	return s.devices, s.scanErr
}
func (s *scriptedOps) Pair(string) error   { return s.pairErr }
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

func TestPairingPairValidatesMAC(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, body := range []string{`{}`, `{"mac":"nonsense"}`, `not json`,
		`{"mac":"DC:04:5A:EB:72:2B","pin":"junk"}`,
		`{"mac":"DC:04:5A:EB:72:2B","pin":"0123456"}`} {
		if w := do(t, h, "POST", "/api/v1/pairing/pair", "tok", body); w.Code != 400 {
			t.Fatalf("body %q -> %d, want 400", body, w.Code)
		} else {
			exactBody(t, w, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
		}
	}
}

func TestPairingPairUsesExactJSON(t *testing.T) {
	h := pairingServer(t, &scriptedOps{})
	for _, body := range []string{
		`{"mac":"DC:04:5A:EB:72:2B","pin":"020555","extra":true}`,
		`{"mac":"DC:04:5A:EB:72:2B","pin":"020555"}{}`,
	} {
		w := do(t, h, http.MethodPost, "/api/v1/pairing/pair", "tok", body)
		if w.Code != 400 {
			t.Fatalf("body %q status %d", body, w.Code)
		}
		exactBody(t, w, `{"error":{"code":"invalid_request","message":"Request is invalid","details":{}}}`)
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
	if w := do(t, h, "POST", "/api/v1/pairing/scan", "", ""); w.Code != 401 {
		t.Fatalf("unauthed scan = %d, want 401", w.Code)
	}
}
