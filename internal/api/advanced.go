package api

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

func (s *server) getClock(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	deviceTime, available, err := service.ReadClock(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if !available {
		writeJSON(w, http.StatusOK, struct {
			Available  bool `json:"available"`
			DeviceTime any  `json:"device_time"`
			SystemTime any  `json:"system_time"`
			Drift      any  `json:"drift_seconds"`
		}{false, nil, nil, nil})
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	writeJSON(w, http.StatusOK, struct {
		Available  bool      `json:"available"`
		DeviceTime time.Time `json:"device_time"`
		SystemTime time.Time `json:"system_time"`
		Drift      int64     `json:"drift_seconds"`
	}{true, deviceTime.UTC(), now, int64(deviceTime.Sub(now) / time.Second)})
}

func (s *server) syncClock(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	now := s.now().UTC().Truncate(time.Second)
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.SyncClock(r.Context(), now); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Synced     bool      `json:"synced"`
		SystemTime time.Time `json:"system_time"`
	}{true, now})
}

func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.Restart(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status    string `json:"status"`
		Reconnect string `json:"reconnect"`
	}{"restarting", "armed"})
}

func (s *server) shutdown(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.Shutdown(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status    string `json:"status"`
		Reconnect string `json:"reconnect"`
	}{"shutdown", "disarmed"})
}

func otaMode(mode byte) string {
	if mode == 2 {
		return "ota"
	}
	return "app"
}

func (s *server) otaInfo(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	info, err := service.OTAInfo(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	firmware := ""
	if device := s.d.Store.Snapshot().Device; device != nil {
		firmware = device.BootloaderFirmware
	}
	writeJSON(w, http.StatusOK, struct {
		Mode     string `json:"mode"`
		CID      uint16 `json:"cid"`
		Firmware string `json:"bootloader_firmware"`
	}{otaMode(info.Mode), info.CID, firmware})
}

func (s *server) enterOTA(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Confirm *bool `json:"confirm"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Confirm == nil || !*request.Confirm {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.EnterOTA(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Mode      string `json:"mode"`
		Reconnect string `json:"reconnect"`
	}{"ota", "bootloader"})
}

func (s *server) exitOTA(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.ExitOTA(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Mode      string `json:"mode"`
		Reconnect string `json:"reconnect"`
	}{"app", "armed"})
}

func (s *server) putRunningMode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Mode *int `json:"mode"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Mode == nil || *request.Mode < 0 || *request.Mode > 1 {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.SetRunningMode(r.Context(), byte(*request.Mode)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Mode int `json:"mode"`
	}{*request.Mode})
}

func (s *server) getBarrierFree(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	enabled, err := service.BarrierFree(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled bool `json:"enabled"`
	}{enabled})
}

func (s *server) putBarrierFree(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Enabled == nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	enabled, err := service.SetBarrierFree(r.Context(), *request.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled bool `json:"enabled"`
	}{enabled})
}

func (s *server) getUSBFirmware(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	raw, err := service.USBFirmwareVersion(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if len(raw) < 3 {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Raw   string `json:"raw"`
		Major byte   `json:"major"`
		Minor byte   `json:"minor"`
		Patch byte   `json:"patch"`
	}{hex.EncodeToString(raw), raw[0], raw[1], raw[2]})
}

func (s *server) putBLEPIN(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PIN *string `json:"pin"`
	}
	if err := decodeJSON(r, &request); err != nil || request.PIN == nil || len(*request.PIN) != 6 {
		writeAPIError(w, "invalid_request")
		return
	}
	for _, digit := range *request.PIN {
		if digit < '0' || digit > '9' {
			writeAPIError(w, "invalid_request")
			return
		}
	}
	pin, err := strconv.ParseUint(*request.PIN, 10, 32)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if err := service.SetBLEPIN(r.Context(), uint32(pin)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Updated bool `json:"updated"`
	}{true})
}
