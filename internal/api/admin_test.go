package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	controlpkg "github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
	qrcode "github.com/skip2/go-qrcode"
)

type adminFixture struct {
	h              http.Handler
	store          *auth.Store
	activeStore    *auth.Store
	alternateStore *auth.Store
	pairing        *auth.Pairing
	clientSecret   string
	clientID       string
	config         *config.Config
	saves          int
	lastSaved      *config.Config
	deps           Deps
	fingerprint    string
	preferredHost  string
	magicDNS       string
	deviceID       string
	live           liveSettingsFixture
	applyCalls     int
	rollbacks      int
	applyErr       error
	saveErr        error
}

type liveSettingsFixture struct {
	PairingTTL      time.Duration
	PairingAlwaysOn bool
	Advanced        bool
	BLEPIN          string
	TokenStore      string
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.OpenStore(filepath.Join(dir, "tokens.json"), "tok")
	if err != nil {
		t.Fatal(err)
	}
	alternateStore, err := auth.OpenStore(filepath.Join(dir, "tokens-next.json"), "tok")
	if err != nil {
		t.Fatal(err)
	}
	clientSecret, clientMeta, err := store.Issue("existing client")
	if err != nil {
		t.Fatal(err)
	}
	pairing := auth.NewPairing(store, 5*time.Minute, false)
	cfgPath := filepath.Join(dir, "wattline")
	if err := os.WriteFile(cfgPath, []byte("config wattline 'main'\n\toption token 'tok'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	f := &adminFixture{store: store, activeStore: store, alternateStore: alternateStore, pairing: pairing, clientSecret: clientSecret, clientID: clientMeta.ID, config: cfg,
		fingerprint: strings.Repeat("a", 64), preferredHost: "wattline.lan", magicDNS: "wattline.example.ts.net",
		deviceID: "DC:04:5A:EB:72:2B", live: liveSettingsFixture{cfg.PairingTTL, cfg.PairingAlwaysOn, cfg.Advanced, cfg.BLEPIN, cfg.TokenStore}}
	h, _, _ := testServerWith(t, func(d *Deps) {
		d.Auth = store
		d.AuthStore = func() *auth.Store { return f.activeStore }
		d.ClientPairing = pairing
		d.Settings = func() *config.Config {
			if f.config == nil {
				return nil
			}
			copy := *f.config
			copy.MDNSInterfaces = append([]string(nil), f.config.MDNSInterfaces...)
			return &copy
		}
		d.SaveMain = func(next *config.Config) error {
			f.saves++
			if f.saveErr != nil {
				return f.saveErr
			}
			copy := *next
			copy.MDNSInterfaces = append([]string(nil), next.MDNSInterfaces...)
			f.config = &copy
			f.lastSaved = &copy
			return nil
		}
		d.ApplySettings = func(before, after *config.Config) (func(), error) {
			f.applyCalls++
			if f.applyErr != nil {
				return nil, f.applyErr
			}
			previous := f.live
			pairRollback := func() {}
			storeRollback := func() {}
			if before.PairingTTL != after.PairingTTL || before.PairingAlwaysOn != after.PairingAlwaysOn {
				var err error
				pairRollback, err = f.pairing.Reconfigure(after.PairingTTL, after.PairingAlwaysOn)
				if err != nil {
					return nil, err
				}
			}
			if before.TokenStore != after.TokenStore {
				previousStore := f.activeStore
				f.activeStore = f.alternateStore
				pairingRollback := f.pairing.RebindStore(f.alternateStore)
				storeRollback = func() { pairingRollback(); f.activeStore = previousStore }
			}
			f.live = liveSettingsFixture{after.PairingTTL, after.PairingAlwaysOn, after.Advanced, after.BLEPIN, after.TokenStore}
			return func() { storeRollback(); pairRollback(); f.live = previous; f.rollbacks++ }, nil
		}
		d.TLSFingerprint = func() string { return f.fingerprint }
		d.PreferredHost = func() string { return f.preferredHost }
		d.MagicDNSName = func() string { return f.magicDNS }
		d.Identity = func() ble.Identity { return ble.Identity{MAC: f.deviceID, Model: "BP4SL3V2"} }
		f.deps = *d
	})
	f.h = h
	return f
}

func TestAuthRolesAndPrincipalContext(t *testing.T) {
	f := newAdminFixture(t)
	for _, tc := range []struct {
		name, method, path, token string
		want                      int
	}{
		{"bootstrap client route", http.MethodGet, "/api/v1/status", "tok", 200},
		{"managed client route", http.MethodGet, "/api/v1/status", f.clientSecret, 200},
		{"managed admin route", http.MethodGet, "/api/v1/settings", f.clientSecret, 403},
		{"bootstrap admin route", http.MethodGet, "/api/v1/settings", "tok", 200},
		{"managed BLE pairing route", http.MethodGet, "/api/v1/pairing/status", f.clientSecret, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, f.h, tc.method, tc.path, tc.token, "")
			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	adminRoutes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/device/dc/bypass/threshold"}, {http.MethodPut, "/api/v1/device/dc/bypass/threshold"},
		{http.MethodGet, "/api/v1/device/bypass-threshold"}, {http.MethodPost, "/api/v1/device/bypass-threshold"},
		{http.MethodGet, "/api/v1/device/clock"}, {http.MethodPost, "/api/v1/device/clock/sync"},
		{http.MethodGet, "/api/v1/device/ota"}, {http.MethodPost, "/api/v1/device/ota/enter"}, {http.MethodPost, "/api/v1/device/ota/exit"},
		{http.MethodPut, "/api/v1/device/advanced/running-mode"},
		{http.MethodGet, "/api/v1/device/advanced/barrier-free"}, {http.MethodPut, "/api/v1/device/advanced/barrier-free"},
		{http.MethodGet, "/api/v1/device/advanced/usb-fw-version"}, {http.MethodPut, "/api/v1/device/advanced/ble-pin"},
		{http.MethodGet, "/api/v1/pairing-mode"}, {http.MethodPost, "/api/v1/pairing-mode"}, {http.MethodDelete, "/api/v1/pairing-mode"},
		{http.MethodGet, "/api/v1/pairing-mode/qr.png"}, {http.MethodGet, "/api/v1/tokens"}, {http.MethodDelete, "/api/v1/tokens/client"},
		{http.MethodGet, "/api/v1/settings"}, {http.MethodPut, "/api/v1/settings"},
		{http.MethodPost, "/api/v1/tls/rotate"},
	}
	for _, route := range adminRoutes {
		rr := do(t, f.h, route.method, route.path, f.clientSecret, "")
		if rr.Code != http.StatusForbidden {
			t.Errorf("managed %s %s status %d: %s", route.method, route.path, rr.Code, rr.Body.String())
		}
	}
	for _, header := range []string{"Bearer", "Bearer ", "Bearer  tok", "bearer tok", "Bearer tok extra"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		req.Header.Set("Authorization", header)
		rr := httptest.NewRecorder()
		f.h.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Fatalf("header %q got %d", header, rr.Code)
		}
	}
}

func TestTLSRotateRequiresAdminConfirmationAndReturnsNewPin(t *testing.T) {
	f := newAdminFixture(t)
	old := f.fingerprint
	rotations := 0
	f.deps.RotateTLS = func() (string, error) {
		rotations++
		return strings.Repeat("b", 64), nil
	}
	h := NewServer(f.deps)
	for _, tc := range []struct {
		name, token, body string
		want              int
	}{
		{"client forbidden", f.clientSecret, `{"confirm":true}`, http.StatusForbidden},
		{"confirmation absent", "tok", `{}`, http.StatusBadRequest},
		{"confirmation false", "tok", `{"confirm":false}`, http.StatusBadRequest},
		{"unknown field", "tok", `{"confirm":true,"extra":1}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, h, http.MethodPost, "/api/v1/tls/rotate", tc.token, tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
	if rotations != 0 || f.fingerprint != old {
		t.Fatalf("rotation ran before confirmed admin request")
	}
	rr := do(t, h, http.MethodPost, "/api/v1/tls/rotate", "tok", `{"confirm":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != `{"sha256":"`+strings.Repeat("b", 64)+`","restart_required":true}`+"\n" {
		t.Fatalf("body %q", rr.Body.String())
	}
	if rotations != 1 {
		t.Fatalf("rotations = %d", rotations)
	}
	settings := do(t, h, http.MethodGet, "/api/v1/settings", "tok", "")
	if !strings.Contains(settings.Body.String(), old) || strings.Contains(settings.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("settings must retain the fingerprint served until restart: %s", settings.Body.String())
	}
	status := f.pairing.Open()
	uri := (&server{d: f.deps}).pairingURI(status.PIN)
	if !strings.Contains(uri, "tls="+old) || strings.Contains(uri, strings.Repeat("b", 64)) {
		t.Fatalf("QR/pairing URI switched before listener restart: %s", uri)
	}
	paired := do(t, h, http.MethodPost, "/api/v1/pair", "", `{"pin":"`+status.PIN+`","label":"post-rotation client"}`)
	if paired.Code != http.StatusCreated || !strings.Contains(paired.Body.String(), old) || strings.Contains(paired.Body.String(), strings.Repeat("b", 64)) {
		t.Fatalf("pair metadata switched before listener restart: %d %s", paired.Code, paired.Body.String())
	}
}

func TestTLSRotateFailureIsCanonicalInternalError(t *testing.T) {
	f := newAdminFixture(t)
	f.deps.RotateTLS = func() (string, error) { return "", errors.New("disk full") }
	rr := do(t, NewServer(f.deps), http.MethodPost, "/api/v1/tls/rotate", "tok", `{"confirm":true}`)
	if rr.Code != http.StatusInternalServerError || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\",\"details\":{}}}\n" {
		t.Fatalf("status %d body %q", rr.Code, rr.Body.String())
	}
}

func TestRequesterIPPreservesScopedIPv6Identity(t *testing.T) {
	tests := map[string]string{
		"[fe80::1%br-lan]:1234":     "fe80::1%br-lan",
		"[fe80::1%tailscale0]:9876": "fe80::1%tailscale0",
		"[::ffff:192.0.2.4]:1234":   "192.0.2.4",
		"192.0.2.4:9999":            "192.0.2.4",
	}
	for remote, want := range tests {
		if got := requesterIP(remote); got != want {
			t.Errorf("requesterIP(%q)=%q want %q", remote, got, want)
		}
	}
	if requesterIP("[fe80::1%br-lan]:1") == requesterIP("[fe80::1%tailscale0]:1") {
		t.Fatal("distinct IPv6 scopes collapsed into one rate-limit identity")
	}
}

func TestClientPairIsPublicOneTimeAndUsesRemoteAddress(t *testing.T) {
	f := newAdminFixture(t)
	status := f.pairing.Open()
	body := `{"pin":"` + status.PIN + `","label":"Keith's iPhone"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pair", bytes.NewBufferString(body))
	req.RemoteAddr = "192.0.2.40:43210"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Token         string            `json:"token"`
		TokenMetadata auth.TokenMeta    `json:"token_metadata"`
		DeviceID      string            `json:"device_id"`
		BaseURLs      map[string]string `json:"base_urls"`
		TLSSHA256     string            `json:"tls_sha256"`
		MagicDNSName  string            `json:"magic_dns_name"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Token, "wlt_") || got.TokenMetadata.Label != "Keith's iPhone" || got.DeviceID != "DC:04:5A:EB:72:2B" {
		t.Fatalf("pair response: %+v", got)
	}
	if got.BaseURLs["http"] != "http://wattline.example.ts.net:8377/api/v1" || got.BaseURLs["https"] != "https://wattline.example.ts.net:8378/api/v1" || got.TLSSHA256 != strings.Repeat("a", 64) || got.MagicDNSName != "wattline.example.ts.net" {
		t.Fatalf("connection metadata: %+v", got)
	}
	if _, ok := f.store.Authenticate(got.Token); !ok {
		t.Fatal("issued secret did not authenticate")
	}
	listed, _ := json.Marshal(f.store.List())
	if bytes.Contains(listed, []byte(got.Token)) {
		t.Fatal("secret leaked through token metadata")
	}
}

func TestClientPairRejectsInvalidExpiredAndRateLimitedPIN(t *testing.T) {
	f := newAdminFixture(t)
	f.pairing.Open()
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/pair", strings.NewReader(`{"pin":"999999","label":"phone"}`))
		req.RemoteAddr = "192.0.2.77:1234"
		req.Header.Set("X-Forwarded-For", "192.0.2."+string(rune('1'+i)))
		rr := httptest.NewRecorder()
		f.h.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Fatalf("attempt %d status %d: %s", i, rr.Code, rr.Body.String())
		}
		exactBody(t, rr, `{"error":{"code":"invalid_or_expired_pin","message":"Pairing PIN is invalid or expired","details":{}}}`)
	}
	f.pairing.Close()
	rr := do(t, f.h, http.MethodPost, "/api/v1/pair", "", `{"pin":"123456","label":"phone"}`)
	if rr.Code != 401 {
		t.Fatalf("closed status %d", rr.Code)
	}
	for _, bad := range []string{"", `{}`, `{"pin":123456,"label":"x"}`, `{"pin":"123456","label":"x","extra":1}`} {
		if rr := do(t, f.h, http.MethodPost, "/api/v1/pair", "", bad); rr.Code != 400 {
			t.Fatalf("bad %q status %d", bad, rr.Code)
		}
	}
}

func TestPairingModeAndQRCode(t *testing.T) {
	f := newAdminFixture(t)
	if rr := do(t, f.h, http.MethodGet, "/api/v1/pairing-mode", "tok", ""); rr.Code != 200 {
		t.Fatal(rr.Code)
	} else {
		exactBody(t, rr, `{"open":false,"expires_at":"0001-01-01T00:00:00Z"}`)
	}
	rr := do(t, f.h, http.MethodPost, "/api/v1/pairing-mode", "tok", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"pin":"`) {
		t.Fatalf("open: %d %s", rr.Code, rr.Body.String())
	}
	status := f.pairing.Status(true)
	wantURI := "wattline://pair?v=1&id=DC%3A04%3A5A%3AEB%3A72%3A2B&host=wattline.example.ts.net&http=8377&https=8378&pin=" + status.PIN + "&tls=" + strings.Repeat("a", 64)
	if got := (&server{d: f.deps}).pairingURI(status.PIN); got != wantURI {
		t.Fatalf("URI\n got %s\nwant %s", got, wantURI)
	}
	rr = do(t, f.h, http.MethodGet, "/api/v1/pairing-mode/qr.png", "tok", "")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("Cache-Control") != "no-store" || !bytes.HasPrefix(rr.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("QR: %d headers=%v prefix=%x", rr.Code, rr.Header(), rr.Body.Bytes()[:min(8, rr.Body.Len())])
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("tok")) {
		t.Fatal("bootstrap token leaked in QR bytes")
	}
	wantPNG, err := qrcode.Encode(wantURI, qrcode.Medium, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rr.Body.Bytes(), wantPNG) {
		t.Fatal("QR PNG does not encode the exact documented pairing URI")
	}
	if rr := do(t, f.h, http.MethodGet, "/api/v1/pairing-mode/qr.png?pin="+status.PIN, "tok", ""); rr.Code != 400 {
		t.Fatalf("query PIN status %d", rr.Code)
	}
	if rr := do(t, f.h, http.MethodDelete, "/api/v1/pairing-mode", "tok", ""); rr.Code != 200 {
		t.Fatalf("close status %d", rr.Code)
	} else {
		exactBody(t, rr, `{"open":false}`)
	}
	if rr := do(t, f.h, http.MethodGet, "/api/v1/pairing-mode/qr.png", "tok", ""); rr.Code != 409 {
		t.Fatalf("closed QR status %d", rr.Code)
	}
}

func TestTokensListMetadataAndRevoke(t *testing.T) {
	f := newAdminFixture(t)
	rr := do(t, f.h, http.MethodGet, "/api/v1/tokens", "tok", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), f.clientSecret) || strings.Contains(rr.Body.String(), "hash") {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, f.h, http.MethodDelete, "/api/v1/tokens/bootstrap", "tok", ""); rr.Code != 400 {
		t.Fatalf("bootstrap revoke status %d", rr.Code)
	}
	if rr := do(t, f.h, http.MethodDelete, "/api/v1/tokens/missing", "tok", ""); rr.Code != 404 {
		t.Fatalf("missing revoke status %d", rr.Code)
	}
	if rr := do(t, f.h, http.MethodDelete, "/api/v1/tokens/"+f.clientID, "tok", ""); rr.Code != 200 {
		t.Fatalf("revoke status %d: %s", rr.Code, rr.Body.String())
	} else {
		exactBody(t, rr, `{"revoked":"`+f.clientID+`"}`)
	}
	if rr := do(t, f.h, http.MethodGet, "/api/v1/status", f.clientSecret, ""); rr.Code != 401 {
		t.Fatalf("revoked token status %d", rr.Code)
	}
}

func TestAdminSettingsGetPutMergeAndRestartPolicy(t *testing.T) {
	f := newAdminFixture(t)
	rr := do(t, f.h, http.MethodGet, "/api/v1/settings", "tok", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), `"token":"tok"`) || !strings.Contains(rr.Body.String(), `"sha256":"`+strings.Repeat("a", 64)+`"`) {
		t.Fatalf("get settings: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true,"pairing_always_on":true,"pairing_ttl":"1m30s","ble_pin":"123456","token_store":"/etc/wattline/new-tokens.json"}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"restart_required":false`) || !f.config.Advanced || !f.config.PairingAlwaysOn {
		t.Fatalf("policy update: %d %s cfg=%+v", rr.Code, rr.Body.String(), f.config)
	}
	wantLive := liveSettingsFixture{90 * time.Second, true, true, "123456", "/etc/wattline/new-tokens.json"}
	if !reflect.DeepEqual(f.live, wantLive) {
		t.Fatalf("live settings not applied: got %+v want %+v", f.live, wantLive)
	}
	if f.activeStore != f.alternateStore {
		t.Fatal("token_store update did not switch the live authenticator")
	}
	pairStatus := f.pairing.Status(true)
	if !pairStatus.Open || pairStatus.PIN == "" || time.Until(pairStatus.ExpiresAt) < 80*time.Second {
		t.Fatalf("pairing policy not live: %+v", pairStatus)
	}
	beforeTokens := len(f.alternateStore.List())
	if _, _, err := f.pairing.Exchange("test", pairStatus.PIN, "new-store client"); err != nil {
		t.Fatalf("pairing did not switch token store: %v", err)
	}
	if len(f.alternateStore.List()) != beforeTokens+1 {
		t.Fatal("pairing issued into the old token store")
	}
	oldHTTPS := f.config.HTTPSPort
	rr = do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", `{"http":{"port":8477},"mdns":{"enabled":false,"interfaces":[]},"wan_access":true}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"restart_required":true`) || f.config.HTTPPort != 8477 || f.config.HTTPSPort != oldHTTPS || len(f.config.MDNSInterfaces) != 0 || !f.config.WANAccess {
		t.Fatalf("network update: %d %s cfg=%+v", rr.Code, rr.Body.String(), f.config)
	}
	if f.saves != 2 {
		t.Fatalf("save count %d", f.saves)
	}
}

func TestAdminSettingsTransactionRollsBackRuntimeWhenPersistenceFails(t *testing.T) {
	f := newAdminFixture(t)
	beforeLive := f.live
	f.saveErr = errors.New("disk full")
	rr := do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true,"pairing_always_on":true,"pairing_ttl":"1m0s","token_store":"/etc/wattline/new-tokens.json"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if !reflect.DeepEqual(f.live, beforeLive) || f.pairing.Status(true).Open || f.config.Advanced || f.rollbacks != 1 || f.activeStore != f.store {
		t.Fatalf("split state after failed persistence: live=%+v pairing=%+v config=%+v rollbacks=%d", f.live, f.pairing.Status(true), f.config, f.rollbacks)
	}
}

func TestAdminSettingsApplyFailureDoesNotPersist(t *testing.T) {
	f := newAdminFixture(t)
	f.applyErr = errors.New("runtime rejected")
	rr := do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true}`)
	if rr.Code != http.StatusInternalServerError || f.saves != 0 || f.config.Advanced {
		t.Fatalf("status=%d saves=%d config=%+v", rr.Code, f.saves, f.config)
	}
}

func TestClientPairValidatesConnectionMetadataBeforeIssuingToken(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*adminFixture)
	}{
		{"missing settings", func(f *adminFixture) { f.config = nil }},
		{"missing device id", func(f *adminFixture) { f.deviceID = "" }},
		{"missing host", func(f *adminFixture) { f.magicDNS, f.preferredHost = "", "" }},
		{"no listener", func(f *adminFixture) { f.config.HTTPEnabled, f.config.HTTPSEnabled = false, false }},
		{"missing HTTPS fingerprint", func(f *adminFixture) { f.fingerprint = "" }},
		{"uppercase HTTPS fingerprint", func(f *adminFixture) { f.fingerprint = strings.Repeat("A", 64) }},
		{"short HTTPS fingerprint", func(f *adminFixture) { f.fingerprint = "abc" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			status := f.pairing.Open()
			before := len(f.store.List())
			tc.mutate(f)
			rr := do(t, f.h, http.MethodPost, "/api/v1/pair", "", `{"pin":"`+status.PIN+`","label":"phone"}`)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			if got := len(f.store.List()); got != before {
				t.Fatalf("issued token before metadata validation: before=%d after=%d", before, got)
			}
		})
	}
}

func TestPairAndQRAllowHTTPOnlyWithoutFingerprint(t *testing.T) {
	f := newAdminFixture(t)
	f.config.HTTPSEnabled = false
	f.fingerprint = ""
	status := f.pairing.Open()
	rr := do(t, f.h, http.MethodPost, "/api/v1/pair", "", `{"pin":"`+status.PIN+`","label":"phone"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("pair status %d: %s", rr.Code, rr.Body.String())
	}
	rr = do(t, f.h, http.MethodGet, "/api/v1/pairing-mode/qr.png", "tok", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("QR status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPairMetadataAndQRUseAdvertisedLocalHostWithoutMagicDNS(t *testing.T) {
	f := newAdminFixture(t)
	f.deps.MagicDNSName = func() string { return "" }
	f.deps.PreferredHost = func() string { return "router.local" }
	f.h = NewServer(f.deps)
	status := f.pairing.Open()
	rr := do(t, f.h, http.MethodPost, "/api/v1/pair", "", `{"pin":"`+status.PIN+`","label":"phone"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("pair status %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		BaseURLs map[string]string `json:"base_urls"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BaseURLs["http"] != "http://router.local:8377/api/v1" || response.BaseURLs["https"] != "https://router.local:8378/api/v1" {
		t.Fatalf("base URLs = %#v", response.BaseURLs)
	}
	uri := (&server{d: f.deps}).pairingURI("123456")
	if !strings.Contains(uri, "&host=router.local&") {
		t.Fatalf("pairing URI = %s", uri)
	}
}

func TestPairingQRRejectsInvalidConnectionMetadata(t *testing.T) {
	f := newAdminFixture(t)
	f.pairing.Open()
	f.fingerprint = strings.Repeat("A", 64)
	rr := do(t, f.h, http.MethodGet, "/api/v1/pairing-mode/qr.png", "tok", "")
	if rr.Code != http.StatusInternalServerError || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q body=%s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	exactBody(t, rr, `{"error":{"code":"internal_error","message":"Internal server error","details":{}}}`)
}

func TestAdminSettingsRefusesLiveChangeWithoutApplicationMechanism(t *testing.T) {
	f := newAdminFixture(t)
	deps := f.deps
	deps.ApplySettings = nil
	h := NewServer(deps)
	rr := do(t, h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true}`)
	if rr.Code != http.StatusInternalServerError || f.saves != 0 || f.config.Advanced {
		t.Fatalf("status=%d saves=%d config=%+v", rr.Code, f.saves, f.config)
	}
}

func TestSettingsTransactionSerializesFailedAndSuccessfulUpdates(t *testing.T) {
	f := newAdminFixture(t)
	deps := f.deps
	var diskMu sync.Mutex
	disk := *f.config
	runtime := liveSettingsFixture{disk.PairingTTL, disk.PairingAlwaysOn, disk.Advanced, disk.BLEPIN, disk.TokenStore}
	aSaving := make(chan struct{})
	releaseA := make(chan struct{})
	bApplied := make(chan struct{}, 1)
	deps.Settings = func() *config.Config {
		diskMu.Lock()
		defer diskMu.Unlock()
		copy := disk
		copy.MDNSInterfaces = append([]string(nil), disk.MDNSInterfaces...)
		return &copy
	}
	deps.ApplySettings = func(before, after *config.Config) (func(), error) {
		previous := runtime
		runtime = liveSettingsFixture{after.PairingTTL, after.PairingAlwaysOn, after.Advanced, after.BLEPIN, after.TokenStore}
		if after.PairingAlwaysOn {
			bApplied <- struct{}{}
		}
		return func() { runtime = previous }, nil
	}
	deps.SaveMain = func(next *config.Config) error {
		if next.Advanced {
			close(aSaving)
			<-releaseA
			return errors.New("first save failed")
		}
		diskMu.Lock()
		disk = *next
		disk.MDNSInterfaces = append([]string(nil), next.MDNSInterfaces...)
		diskMu.Unlock()
		return nil
	}
	h := NewServer(deps)
	type result struct{ code int }
	aDone := make(chan result, 1)
	bDone := make(chan result, 1)
	go func() { aDone <- result{do(t, h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true}`).Code} }()
	<-aSaving
	bAttempted := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"pairing_always_on":true}`))
		req.Header.Set("Authorization", "Bearer tok")
		rr := httptest.NewRecorder()
		close(bAttempted)
		h.ServeHTTP(rr, req)
		bDone <- result{rr.Code}
	}()
	<-bAttempted
	select {
	case <-bApplied:
		close(releaseA)
		t.Fatal("second settings update applied before first transaction completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseA)
	if got := (<-aDone).code; got != http.StatusInternalServerError {
		t.Fatalf("failed update status %d", got)
	}
	if got := (<-bDone).code; got != http.StatusOK {
		t.Fatalf("successful update status %d", got)
	}
	diskMu.Lock()
	finalDisk := disk
	diskMu.Unlock()
	if finalDisk.Advanced || !finalDisk.PairingAlwaysOn || runtime.Advanced || !runtime.PairingAlwaysOn {
		t.Fatalf("disk/runtime diverged: disk=%+v runtime=%+v", finalDisk, runtime)
	}
}

func TestClientPairWaitsForTokenStoreCutover(t *testing.T) {
	f := newAdminFixture(t)
	status := f.pairing.Open()
	deps := f.deps
	originalApply := deps.ApplySettings
	cutoverStarted := make(chan struct{})
	releaseCutover := make(chan struct{})
	deps.ApplySettings = func(before, after *config.Config) (func(), error) {
		rollback, err := originalApply(before, after)
		if err != nil {
			return nil, err
		}
		close(cutoverStarted)
		<-releaseCutover
		return rollback, nil
	}
	h := NewServer(deps)
	putDone := make(chan int, 1)
	go func() {
		putDone <- do(t, h, http.MethodPut, "/api/v1/settings", "tok", `{"token_store":"/etc/wattline/new-tokens.json"}`).Code
	}()
	<-cutoverStarted
	pairDone := make(chan *httptest.ResponseRecorder, 1)
	pairAttempted := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/pair", strings.NewReader(`{"pin":"`+status.PIN+`","label":"during cutover"}`))
		rr := httptest.NewRecorder()
		close(pairAttempted)
		h.ServeHTTP(rr, req)
		pairDone <- rr
	}()
	<-pairAttempted
	select {
	case response := <-pairDone:
		close(releaseCutover)
		t.Fatalf("pair completed during token-store cutover: %d %s", response.Code, response.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCutover)
	if got := <-putDone; got != http.StatusOK {
		t.Fatalf("settings status %d", got)
	}
	response := <-pairDone
	if response.Code != http.StatusCreated {
		t.Fatalf("pair status %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.activeStore.Authenticate(body.Token); !ok || f.activeStore != f.alternateStore {
		t.Fatal("issued token does not authenticate in the active post-cutover store")
	}
}

func TestClockAndOTARoutesWaitForSettingsTransaction(t *testing.T) {
	routes := []struct {
		method, path, body, entered string
	}{
		{http.MethodGet, "/api/v1/device/clock", "", "clock-read"},
		{http.MethodPost, "/api/v1/device/clock/sync", "", "clock-sync"},
		{http.MethodGet, "/api/v1/device/ota", "", "ota-info"},
		{http.MethodPost, "/api/v1/device/ota/enter", `{"confirm":true}`, "ota-enter"},
		{http.MethodPost, "/api/v1/device/ota/exit", "", "ota-exit"},
	}
	for _, route := range routes {
		t.Run(route.entered, func(t *testing.T) {
			f := newAdminFixture(t)
			deps := f.deps
			mode := "app"
			if route.entered == "ota-exit" {
				mode = "ota"
			}
			deps.Store.SetIdentity(state.Identity{Model: "BP4SL3V2", MAC: f.deviceID, CID: 773, Mode: mode,
				Features: 4095, FeatureSet: proto.FeatureSet{FactoryMode: true, Shutdown: true},
				Characteristics: map[string]bool{"command": true, "current_time": true, "ota": true}})
			deps.Store.SetConnected(true)
			deps.Store.SetConnection(state.Connection{Phase: state.ConnectionReady, ReconnectArmed: true, Since: time.Now()})
			entered := make(chan string, 1)
			session := &canonicalSession{store: deps.Store, entered: entered, clockOK: true,
				clockTime: time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC), otaInfo: proto.OTAInfo{Mode: 1, CID: 773}}
			deps.DeviceControl = controlpkg.NewService(func() controlpkg.Session { return session }, deps.Store, nil, func() bool { return true })
			originalApply := deps.ApplySettings
			applyStarted := make(chan struct{})
			releaseApply := make(chan struct{})
			deps.ApplySettings = func(before, after *config.Config) (func(), error) {
				close(applyStarted)
				<-releaseApply
				return originalApply(before, after)
			}
			s := &server{d: deps}
			var routeHandler http.HandlerFunc
			switch route.entered {
			case "clock-read":
				routeHandler = s.getClock
			case "clock-sync":
				routeHandler = s.syncClock
			case "ota-info":
				routeHandler = s.otaInfo
			case "ota-enter":
				routeHandler = s.enterOTA
			case "ota-exit":
				routeHandler = s.exitOTA
			}
			routeHandler = s.settingsRead(routeHandler)
			putDone := make(chan int, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"advanced":true}`))
				rr := httptest.NewRecorder()
				s.putSettings(rr, req)
				putDone <- rr.Code
			}()
			<-applyStarted
			requestAttempted := make(chan struct{})
			requestDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				rr := httptest.NewRecorder()
				close(requestAttempted)
				routeHandler(rr, req)
				requestDone <- rr
			}()
			<-requestAttempted
			select {
			case operation := <-entered:
				close(releaseApply)
				t.Fatalf("%s entered control seam during uncommitted settings apply", operation)
			case <-time.After(100 * time.Millisecond):
			}
			close(releaseApply)
			if got := <-putDone; got != http.StatusOK {
				t.Fatalf("settings status %d", got)
			}
			select {
			case operation := <-entered:
				if operation != route.entered {
					t.Fatalf("entered %q want %q", operation, route.entered)
				}
			case <-time.After(time.Second):
				t.Fatal("route did not proceed after settings commit")
			}
			if response := <-requestDone; response.Code != http.StatusOK {
				t.Fatalf("route status %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminSettingsRejectInvalidUnknownReadOnlyAndNull(t *testing.T) {
	f := newAdminFixture(t)
	for _, body := range []string{
		`{"unknown":1}`,
		`{"tls":{"sha256":"abc"}}`,
		`{"http":{"port":0}}`,
		`{"http":{"enabled":false},"https":{"enabled":false}}`,
		`{"pairing_ttl":"0s"}`,
		`{"advanced":null}`,
		`{"mdns":{"interfaces":null}}`,
		`{"http":{"unknown":true}}`,
	} {
		rr := do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", body)
		if rr.Code != 400 {
			t.Fatalf("body %s status %d: %s", body, rr.Code, rr.Body.String())
		}
	}
	if f.saves != 0 {
		t.Fatalf("invalid requests saved %d times", f.saves)
	}
}

func TestPairingURIUsesRFC3986EscapingAndOmissions(t *testing.T) {
	f := newAdminFixture(t)
	f.config.HTTPEnabled = false
	f.config.HTTPSEnabled = true
	f.config.HTTPSPort = 9443
	f.deps.MagicDNSName = func() string { return "" }
	f.deps.PreferredHost = func() string { return "watt line.lan" }
	got := (&server{d: f.deps}).pairingURI("001234")
	want := "wattline://pair?v=1&id=DC%3A04%3A5A%3AEB%3A72%3A2B&host=watt%20line.lan&https=9443&pin=001234&tls=" + strings.Repeat("a", 64)
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if _, err := url.Parse(got); err != nil {
		t.Fatal(err)
	}
}
