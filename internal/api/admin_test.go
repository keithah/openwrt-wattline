package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/config"
)

type adminFixture struct {
	h            http.Handler
	store        *auth.Store
	pairing      *auth.Pairing
	clientSecret string
	clientID     string
	config       *config.Config
	saves        int
	lastSaved    *config.Config
	deps         Deps
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.OpenStore(filepath.Join(dir, "tokens.json"), "tok")
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
	f := &adminFixture{store: store, pairing: pairing, clientSecret: clientSecret, clientID: clientMeta.ID, config: cfg}
	h, _, _ := testServerWith(t, func(d *Deps) {
		d.Auth = store
		d.ClientPairing = pairing
		d.Settings = func() *config.Config {
			copy := *f.config
			copy.MDNSInterfaces = append([]string(nil), f.config.MDNSInterfaces...)
			return &copy
		}
		d.SaveMain = func(next *config.Config) error {
			f.saves++
			copy := *next
			copy.MDNSInterfaces = append([]string(nil), next.MDNSInterfaces...)
			f.config = &copy
			f.lastSaved = &copy
			return nil
		}
		d.TLSFingerprint = func() string { return strings.Repeat("a", 64) }
		d.PreferredHost = func() string { return "wattline.lan" }
		d.MagicDNSName = func() string { return "wattline.example.ts.net" }
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
		{"managed advanced route", http.MethodGet, "/api/v1/device/advanced/barrier-free", f.clientSecret, 403},
		{"managed clock route", http.MethodGet, "/api/v1/device/clock", f.clientSecret, 403},
		{"managed OTA route", http.MethodGet, "/api/v1/device/ota", f.clientSecret, 403},
		{"managed threshold route", http.MethodGet, "/api/v1/device/dc/bypass/threshold", f.clientSecret, 403},
		{"managed threshold alias", http.MethodGet, "/api/v1/device/bypass-threshold", f.clientSecret, 403},
		{"managed BLE pairing route", http.MethodGet, "/api/v1/pairing/status", f.clientSecret, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, f.h, tc.method, tc.path, tc.token, "")
			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
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
	rr = do(t, f.h, http.MethodPut, "/api/v1/settings", "tok", `{"advanced":true,"pairing_always_on":true}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"restart_required":false`) || !f.config.Advanced || !f.config.PairingAlwaysOn {
		t.Fatalf("policy update: %d %s cfg=%+v", rr.Code, rr.Body.String(), f.config)
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
