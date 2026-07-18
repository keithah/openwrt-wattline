// Package api serves the daemon's REST + SSE control surface.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/rules"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type Deps struct {
	Store         *state.Store
	Engine        *rules.Engine
	Exec          *actions.Executor
	Token         string
	Identity      func() ble.Identity
	Connected     func() bool
	SaveRules     func([]config.Rule) error
	LoadRules     func() []config.Rule
	Pairing       *ble.Pairing   // nil when the platform has no pairing support
	Control       func() Control // returns nil when no device is connected
	DeviceControl *control.Service
	MagicDNSName  func() string
	Now           func() time.Time
	SaveBLEPIN    func(string) error
	Auth          *auth.Store
	// AuthStore is used when token_store can be switched live. It must return
	// the same store used by ClientPairing for managed-token issuance.
	AuthStore     func() *auth.Store
	ClientPairing *auth.Pairing
	Settings      func() *config.Config
	SaveMain      func(*config.Config) error
	// ApplySettings atomically applies non-restart settings (pairing policy,
	// advanced, BLE PIN, and token store) and returns an idempotent rollback.
	// On error it must leave runtime state unchanged.
	ApplySettings func(before, after *config.Config) (rollback func(), err error)
	// CommitSettings publishes irreversible runtime notifications after SaveMain
	// succeeds. It is required for a live token-store cutover.
	CommitSettings func(before, after *config.Config)
	TLSFingerprint func() string
	// RotateTLS replaces the on-disk certificate and returns its DER SHA-256
	// fingerprint. Active listeners continue using their loaded certificate
	// until the daemon restarts.
	RotateTLS     func() (string, error)
	PreferredHost func() string
}

type server struct {
	d          Deps
	settingsMu sync.RWMutex
	rulesMu    sync.Mutex
}

func (s *server) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now()
	}
	return time.Now()
}

func NewServer(d Deps) http.Handler {
	s := &server{d: d}
	mux := http.NewServeMux()
	for _, route := range routeDescriptors {
		route := route
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route.handler(s, w, r)
		})
		mux.HandleFunc(route.method+" "+route.path, route.middleware(s, limitRequestBody(handler)))
	}
	return cors(mux)
}

type routeDescriptor struct {
	method        string
	path          string
	handler       func(*server, http.ResponseWriter, *http.Request)
	middleware    func(*server, http.HandlerFunc) http.HandlerFunc
	compatibility bool
}

func publicRoute(_ *server, next http.HandlerFunc) http.HandlerFunc { return next }
func clientRoute(s *server, next http.HandlerFunc) http.HandlerFunc { return s.auth(next) }
func adminRoute(s *server, next http.HandlerFunc) http.HandlerFunc  { return s.admin(next) }
func adminSettingsRoute(s *server, next http.HandlerFunc) http.HandlerFunc {
	return s.admin(s.settingsRead(next))
}
func clientSettingsRoute(s *server, next http.HandlerFunc) http.HandlerFunc {
	return s.auth(s.settingsRead(next))
}

func pairingStatusRoute(s *server, w http.ResponseWriter, r *http.Request) {
	s.pairing(s.pairingStatus)(w, r)
}
func pairingScanRoute(s *server, w http.ResponseWriter, r *http.Request) {
	s.pairing(s.pairingScan)(w, r)
}
func pairingPairRoute(s *server, w http.ResponseWriter, r *http.Request) {
	s.pairing(s.pairingPair)(w, r)
}
func pairingUnpairRoute(s *server, w http.ResponseWriter, r *http.Request) {
	s.pairing(s.pairingUnpair)(w, r)
}

// routeDescriptors is the single inventory used to register and document the
// HTTP API. Keeping aliases here makes accidental undocumented routes visible.
var routeDescriptors = []routeDescriptor{
	{"GET", "/api/v1/status", (*server).status, clientRoute, true},
	{"GET", "/api/v1/telemetry", (*server).telemetry, clientRoute, true},
	{"GET", "/api/v1/history", (*server).history, clientRoute, true},
	{"GET", "/api/v1/events", (*server).events, clientRoute, true},
	{"GET", "/api/v1/device", (*server).device, clientRoute, false},
	{"POST", "/api/v1/device/dc", (*server).setDC, clientRoute, false},
	{"POST", "/api/v1/device/usbc/output", (*server).setTypeCOutput, clientRoute, false},
	{"GET", "/api/v1/device/usbc/limit/{type}", (*server).getLimit, clientRoute, false},
	{"PUT", "/api/v1/device/usbc/limit/{type}", (*server).putLimit, clientRoute, false},
	{"DELETE", "/api/v1/device/usbc/limit/{type}", (*server).deleteLimit, clientRoute, false},
	{"POST", "/api/v1/device/dc/bypass", (*server).setBypass, clientRoute, false},
	{"GET", "/api/v1/device/dc/bypass/threshold", (*server).getThreshold, adminSettingsRoute, false},
	{"PUT", "/api/v1/device/dc/bypass/threshold", (*server).putThreshold, adminSettingsRoute, false},
	{"GET", "/api/v1/device/timers", (*server).listTimers, clientRoute, false},
	{"POST", "/api/v1/device/timers", (*server).addTimer, clientRoute, false},
	{"GET", "/api/v1/device/timers/{id}", (*server).getTimer, clientRoute, false},
	{"PUT", "/api/v1/device/timers/{id}", (*server).putTimer, clientRoute, false},
	{"DELETE", "/api/v1/device/timers/{id}", (*server).deleteTimer, clientRoute, false},
	{"GET", "/api/v1/device/clock", (*server).getClock, adminSettingsRoute, false},
	{"POST", "/api/v1/device/clock/sync", (*server).syncClock, adminSettingsRoute, false},
	{"POST", "/api/v1/device/restart", (*server).restart, clientRoute, false},
	{"POST", "/api/v1/device/shutdown", (*server).shutdown, clientRoute, false},
	{"GET", "/api/v1/device/ota", (*server).otaInfo, adminSettingsRoute, false},
	{"POST", "/api/v1/device/ota/enter", (*server).enterOTA, adminSettingsRoute, false},
	{"POST", "/api/v1/device/ota/exit", (*server).exitOTA, adminSettingsRoute, false},
	{"PUT", "/api/v1/device/advanced/running-mode", (*server).putRunningMode, adminSettingsRoute, false},
	{"GET", "/api/v1/device/advanced/barrier-free", (*server).getBarrierFree, adminSettingsRoute, false},
	{"PUT", "/api/v1/device/advanced/barrier-free", (*server).putBarrierFree, adminSettingsRoute, false},
	{"GET", "/api/v1/device/advanced/usb-fw-version", (*server).getUSBFirmware, adminSettingsRoute, false},
	{"PUT", "/api/v1/device/advanced/ble-pin", (*server).putBLEPIN, adminSettingsRoute, false},
	{"GET", "/api/v1/rules", (*server).getRules, clientRoute, true},
	{"POST", "/api/v1/rules", (*server).postRule, adminRoute, true},
	{"PUT", "/api/v1/rules/{name}", (*server).putRule, adminRoute, true},
	{"DELETE", "/api/v1/rules/{name}", (*server).deleteRule, adminRoute, true},
	{"POST", "/api/v1/device/action", (*server).deviceAction, clientRoute, true},
	{"GET", "/api/v1/pairing/status", pairingStatusRoute, clientSettingsRoute, true},
	{"POST", "/api/v1/pairing/scan", pairingScanRoute, clientSettingsRoute, true},
	{"POST", "/api/v1/pairing/pair", pairingPairRoute, clientSettingsRoute, true},
	{"DELETE", "/api/v1/pairing/device/{mac}", pairingUnpairRoute, clientSettingsRoute, true},
	{"GET", "/api/v1/device/usbc-limit", (*server).getUSBCLimit, clientRoute, true},
	{"POST", "/api/v1/device/usbc-limit", (*server).setUSBCLimit, clientRoute, true},
	{"GET", "/api/v1/device/bypass-threshold", (*server).getBypassThreshold, adminSettingsRoute, true},
	{"POST", "/api/v1/device/bypass-threshold", (*server).setBypassThreshold, adminSettingsRoute, true},
	{"GET", "/api/v1/device/schedules", (*server).getSchedules, clientRoute, true},
	{"POST", "/api/v1/device/schedules", (*server).postSchedule, clientRoute, true},
	{"DELETE", "/api/v1/device/schedules/{id}", (*server).deleteSchedule, clientRoute, true},
	{"POST", "/api/v1/pair", (*server).clientPair, publicRoute, false},
	{"GET", "/api/v1/pairing-mode", (*server).pairingModeStatus, adminRoute, false},
	{"POST", "/api/v1/pairing-mode", (*server).openPairingMode, adminRoute, false},
	{"DELETE", "/api/v1/pairing-mode", (*server).closePairingMode, adminRoute, false},
	{"GET", "/api/v1/pairing-mode/qr.png", (*server).pairingQRCode, adminRoute, false},
	{"GET", "/api/v1/tokens", (*server).listTokens, adminRoute, false},
	{"DELETE", "/api/v1/tokens/{id}", (*server).revokeToken, adminRoute, false},
	{"GET", "/api/v1/settings", (*server).getSettings, adminRoute, false},
	{"PUT", "/api/v1/settings", (*server).putSettings, adminRoute, false},
	{"POST", "/api/v1/tls/rotate", (*server).rotateTLS, adminRoute, false},
}

// cors lets the LuCI web UI (served on port 80) call the API on port 8377 —
// a cross-origin request the browser otherwise blocks. The API is bearer-token
// authed and takes no cookies, so a wildcard origin is safe. Preflight OPTIONS
// requests are answered here (the mux only registers GET/POST/PUT/DELETE).
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, store, ok := s.authenticate(r)
		if !ok {
			writeAPIError(w, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		if store != nil {
			ctx = context.WithValue(ctx, authStoreContextKey{}, store)
		}
		next(w, r.WithContext(ctx))
	}
}

type principalContextKey struct{}
type authStoreContextKey struct{}

func principalFromContext(ctx context.Context) (auth.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(auth.Principal)
	return principal, ok
}

func (s *server) authenticate(r *http.Request) (auth.Principal, *auth.Store, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return auth.Principal{}, nil, false
	}
	authorization := values[0]
	if !strings.HasPrefix(authorization, "Bearer ") {
		return auth.Principal{}, nil, false
	}
	secret := strings.TrimPrefix(authorization, "Bearer ")
	if secret == "" || strings.ContainsAny(secret, " \t\r\n") {
		return auth.Principal{}, nil, false
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if store := s.authStore(); store != nil {
		principal, ok := store.Authenticate(secret)
		return principal, store, ok
	}
	if s.d.Token != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.d.Token)) == 1 {
		return auth.Principal{TokenID: "bootstrap", Role: auth.RoleAdmin}, nil, true
	}
	return auth.Principal{}, nil, false
}

func (s *server) authStore() *auth.Store {
	if s.d.AuthStore != nil {
		return s.d.AuthStore()
	}
	return s.d.Auth
}

func (s *server) admin(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromContext(r.Context())
		if !ok || principal.Role != auth.RoleAdmin {
			writeAPIError(w, "admin_required")
			return
		}
		next(w, r)
	})
}

func (s *server) settingsRead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.settingsMu.RLock()
		defer s.settingsMu.RUnlock()
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"connected": s.d.Connected(),
		"device":    s.d.Identity(),
		"rules":     s.d.Engine.Status(),
	})
}

func (s *server) telemetry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, snapshotResponse(s.d.Store.Snapshot()))
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.d.Store.History())
}

func (s *server) getRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.d.LoadRules())
}

func (s *server) validate(rule config.Rule) error {
	for _, a := range rule.Actions {
		if err := actions.ValidateAction(a); err != nil {
			return err
		}
	}
	return rule.Validate()
}

func (s *server) upsert(w http.ResponseWriter, rule config.Rule, name string) {
	if name != "" {
		rule.Name = name
	}
	if rule.HysteresisMargin == 0 {
		rule.HysteresisMargin = 5
	}
	if err := s.validate(rule); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	rulesList := s.d.LoadRules()
	replaced := false
	for i := range rulesList {
		if rulesList[i].Name == rule.Name {
			rulesList[i] = rule
			replaced = true
		}
	}
	if !replaced {
		rulesList = append(rulesList, rule)
	}
	if err := s.d.SaveRules(rulesList); err != nil {
		writeAPIError(w, "internal_error")
		return
	}
	writeJSON(w, 200, rule)
}

func (s *server) postRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if err := decodeJSON(r, &rule); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.upsert(w, rule, "")
}

func (s *server) putRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Rule
	if err := decodeJSON(r, &rule); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.upsert(w, rule, r.PathValue("name"))
}

func (s *server) deleteRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	rulesList := s.d.LoadRules()
	found := false
	out := rulesList[:0]
	for _, rr := range rulesList {
		if rr.Name != name {
			out = append(out, rr)
		} else {
			found = true
		}
	}
	if !found {
		writeAPIError(w, "not_found")
		return
	}
	if err := s.d.SaveRules(out); err != nil {
		writeAPIError(w, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]string{"deleted": name})
}

func (s *server) deviceAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if err := actions.ValidateAction(body.Action); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if strings.HasPrefix(body.Action, "webhook:") {
		principal, ok := principalFromContext(r.Context())
		if !ok || principal.Role != auth.RoleAdmin {
			writeAPIError(w, "admin_required")
			return
		}
	}
	snap := s.d.Store.Snapshot()
	errs := s.d.Exec.Execute([]string{body.Action}, snap, "manual", snap.UpdatedAt)
	if len(errs) > 0 {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": body.Action})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	var revoked <-chan struct{}
	cancelRevocation := func() {}
	if principal, ok := principalFromContext(r.Context()); ok && principal.Role == auth.RoleClient {
		if store, ok := r.Context().Value(authStoreContextKey{}).(*auth.Store); ok {
			var active bool
			revoked, cancelRevocation, active = store.SubscribeRevocation(principal.TokenID)
			if !active {
				writeAPIError(w, "unauthorized")
				return
			}
			defer cancelRevocation()
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	ch, cancel := s.d.Store.Subscribe()
	defer cancel()

	send(snapshotResponse(s.d.Store.Snapshot())) // initial frame, flushed before blocking on subscription

	for {
		select {
		case <-revoked:
			return
		case <-r.Context().Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			send(snapshotResponse(snap))
		}
	}
}
