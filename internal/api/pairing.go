package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/keithah/openwrt-wattline/internal/ble"
)

var (
	macRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	// The Link-Power PIN is a number 0–999999 (API.md BLE_PIN). Rejecting
	// anything else also keeps arbitrary strings out of the UCI config file.
	pinRe = regexp.MustCompile(`^[0-9]{0,6}$`)
)

// pairing guards the pairing endpoints: 503 when the platform has no BlueZ
// pairing support (non-Linux dev hosts, adapter missing at startup).
func (s *server) pairing(next func(http.ResponseWriter, *http.Request, *ble.Pairing)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.d.Pairing == nil {
			http.Error(w, "pairing unavailable on this platform", http.StatusServiceUnavailable)
			return
		}
		next(w, r, s.d.Pairing)
	}
}

// pairingErr maps pairing-manager errors to HTTP: ErrBusy → 409, rest → 500.
func pairingErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, ble.ErrBusy) {
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}

func (s *server) pairingStatus(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	writeJSON(w, 200, p.Status())
}

func (s *server) pairingScan(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	if err := p.StartScan(); err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
}

func (s *server) pairingPair(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	var req struct {
		MAC string `json:"mac"`
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !macRe.MatchString(req.MAC) {
		http.Error(w, "body must be JSON with a valid \"mac\" (AA:BB:CC:DD:EE:FF)", http.StatusBadRequest)
		return
	}
	if !pinRe.MatchString(req.PIN) {
		http.Error(w, "\"pin\" must be up to 6 digits", http.StatusBadRequest)
		return
	}
	if err := p.StartPair(req.MAC, req.PIN); err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pairing"})
}

func (s *server) pairingUnpair(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	mac := r.PathValue("mac")
	if !macRe.MatchString(mac) {
		http.Error(w, "invalid MAC", http.StatusBadRequest)
		return
	}
	if err := p.Unpair(mac); err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "removed"})
}
