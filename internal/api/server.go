// Package api serves the daemon's REST + SSE control surface.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/keithah/openwrt-wattline/internal/actions"
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
}

type server struct{ d Deps }

func NewServer(d Deps) http.Handler {
	s := &server{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.auth(s.status))
	mux.HandleFunc("GET /api/v1/telemetry", s.auth(s.telemetry))
	mux.HandleFunc("GET /api/v1/history", s.auth(s.history))
	mux.HandleFunc("GET /api/v1/events", s.auth(s.events))
	mux.HandleFunc("GET /api/v1/device", s.auth(s.device))
	mux.HandleFunc("POST /api/v1/device/dc", s.auth(s.setDC))
	mux.HandleFunc("POST /api/v1/device/usbc/output", s.auth(s.setTypeCOutput))
	mux.HandleFunc("GET /api/v1/device/usbc/limit/{type}", s.auth(s.getLimit))
	mux.HandleFunc("PUT /api/v1/device/usbc/limit/{type}", s.auth(s.putLimit))
	mux.HandleFunc("DELETE /api/v1/device/usbc/limit/{type}", s.auth(s.deleteLimit))
	mux.HandleFunc("POST /api/v1/device/dc/bypass", s.auth(s.setBypass))
	mux.HandleFunc("GET /api/v1/device/dc/bypass/threshold", s.auth(s.getThreshold))
	mux.HandleFunc("PUT /api/v1/device/dc/bypass/threshold", s.auth(s.putThreshold))
	mux.HandleFunc("GET /api/v1/rules", s.auth(s.getRules))
	mux.HandleFunc("POST /api/v1/rules", s.auth(s.postRule))
	mux.HandleFunc("PUT /api/v1/rules/{name}", s.auth(s.putRule))
	mux.HandleFunc("DELETE /api/v1/rules/{name}", s.auth(s.deleteRule))
	mux.HandleFunc("POST /api/v1/device/action", s.auth(s.deviceAction))
	mux.HandleFunc("GET /api/v1/pairing/status", s.auth(s.pairing(s.pairingStatus)))
	mux.HandleFunc("POST /api/v1/pairing/scan", s.auth(s.pairing(s.pairingScan)))
	mux.HandleFunc("POST /api/v1/pairing/pair", s.auth(s.pairing(s.pairingPair)))
	mux.HandleFunc("DELETE /api/v1/pairing/device/{mac}", s.auth(s.pairing(s.pairingUnpair)))
	mux.HandleFunc("GET /api/v1/device/usbc-limit", s.auth(s.getUSBCLimit))
	mux.HandleFunc("POST /api/v1/device/usbc-limit", s.auth(s.setUSBCLimit))
	mux.HandleFunc("GET /api/v1/device/bypass-threshold", s.auth(s.getBypassThreshold))
	mux.HandleFunc("POST /api/v1/device/bypass-threshold", s.auth(s.setBypassThreshold))
	mux.HandleFunc("GET /api/v1/device/schedules", s.auth(s.getSchedules))
	mux.HandleFunc("POST /api/v1/device/schedules", s.auth(s.postSchedule))
	mux.HandleFunc("DELETE /api/v1/device/schedules/{id}", s.auth(s.deleteSchedule))
	return cors(mux)
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
		if s.d.Token == "" {
			writeAPIError(w, "unauthorized")
			return
		}
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			writeAPIError(w, "unauthorized")
			return
		}
		tok := strings.TrimPrefix(authorization, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.d.Token)) != 1 {
			writeAPIError(w, "unauthorized")
			return
		}
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
	snap := s.d.Store.Snapshot()
	errs := s.d.Exec.Execute([]string{body.Action}, snap, "manual", snap.UpdatedAt)
	if len(errs) > 0 {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]string{"ok": body.Action})
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
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
		case <-r.Context().Done():
			return
		case snap := <-ch:
			send(snapshotResponse(snap))
		}
	}
}
