package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/keithah/openwrt-wattline/internal/ble"
)

var (
	macRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	// The Link-Power PIN is a number 0–999999 (API.md BLE_PIN). Rejecting
	// anything else also keeps arbitrary strings out of the UCI config file.
	pinRe = regexp.MustCompile(`^[0-9]{0,6}$`)
	pin6Re = regexp.MustCompile(`^[0-9]{6}$`)
)

// pairing guards the pairing endpoints: 503 when the platform has no BlueZ
// pairing support (non-Linux dev hosts, adapter missing at startup).
func (s *server) pairing(next func(http.ResponseWriter, *http.Request, *ble.Pairing)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.d.Pairing == nil {
			writeAPIError(w, "capability_unsupported")
			return
		}
		next(w, r, s.d.Pairing)
	}
}

// pairingErr maps pairing-manager errors without exposing BlueZ/DBus details.
func pairingErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ble.ErrBusy) {
		writeAPIError(w, "operation_in_progress")
		return
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "pin not requested") { writeAPIError(w, "pairing_pin_not_requested"); return }
	if strings.Contains(msg, "unsupported") { writeAPIError(w, "capability_unsupported"); return }
	writeAPIError(w, "ble_operation_failed")
}

func (s *server) pairingStatus(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	writeJSON(w, 200, p.Status())
}

func (s *server) pairingScan(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if err := p.StartScan(); err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
}

func (s *server) pairingPair(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	s.pairingStart(w, r, p, false)
}

func (s *server) pairingRecover(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	s.pairingStart(w, r, p, true)
}

func (s *server) pairingRequestCode(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	var req struct { MAC string `json:"mac"`; Recover *bool `json:"recover"` }
	if err := decodeJSON(r, &req); err != nil || !macRe.MatchString(req.MAC) {
		writeAPIError(w, "invalid_request"); return
	}
	recover := req.Recover != nil && *req.Recover
	if err := p.StartInteractive(req.MAC, recover); err != nil { pairingErr(w, err); return }
	writeJSON(w, http.StatusAccepted, map[string]string{"status":"pairing"})
}

func (s *server) pairingSubmitPIN(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	var req struct { PIN string `json:"pin"` }
	if err := decodeJSON(r, &req); err != nil || !pin6Re.MatchString(req.PIN) { writeAPIError(w, "invalid_request"); return }
	if err := p.SubmitPIN(req.PIN); err != nil { pairingErr(w, err); return }
	writeJSON(w, http.StatusAccepted, map[string]string{"status":"pin_submitted"})
}

func (s *server) pairingCancel(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	if requireNoBody(r) != nil { writeAPIError(w, "invalid_request"); return }
	if err := p.Cancel(); err != nil { pairingErr(w, err); return }
	writeJSON(w, http.StatusOK, map[string]string{"status":"canceled"})
}

func (s *server) pairingStart(w http.ResponseWriter, r *http.Request, p *ble.Pairing, recover bool) {
	var req struct {
		MAC string `json:"mac"`
		PIN string `json:"pin"`
	}
	if err := decodeJSON(r, &req); err != nil || !macRe.MatchString(req.MAC) {
		writeAPIError(w, "invalid_request")
		return
	}
	if !pinRe.MatchString(req.PIN) {
		writeAPIError(w, "invalid_request")
		return
	}
	var err error
	if recover {
		err = p.StartRecover(req.MAC, req.PIN)
	} else {
		err = p.StartPair(req.MAC, req.PIN)
	}
	if err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pairing"})
}

func (s *server) pairingUnpair(w http.ResponseWriter, r *http.Request, p *ble.Pairing) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	mac := r.PathValue("mac")
	if !macRe.MatchString(mac) {
		writeAPIError(w, "invalid_request")
		return
	}
	if err := p.Unpair(mac); err != nil {
		pairingErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "removed"})
}
