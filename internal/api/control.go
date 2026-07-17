package api

import (
	"net/http"
	"strconv"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

// Control is the live-device settings surface (implemented by *ble.Session).
// The daemon provides a resolver that returns nil when disconnected.
type Control interface {
	USBCLimit(typ int) (int, error)
	SetUSBCLimit(typ, level int) error
	ClearUSBCLimit(typ int) error
	BypassThreshold() (float64, error)
	SetBypassThreshold(volts float64) error
	Schedules() ([]proto.Timer, error)
	UpsertSchedule(id byte, t proto.Timer) (byte, error)
	DeleteSchedule(id byte) error
}

var limitTypes = map[string]int{
	"global": proto.LimitGlobal,
	"input":  proto.LimitInput,
	"output": proto.LimitOutput,
}

// ctl resolves the live device or writes 503 and returns nil.
func (s *server) ctl(w http.ResponseWriter) Control {
	if s.d.Control == nil {
		writeAPIError(w, "device_disconnected")
		return nil
	}
	c := s.d.Control()
	if c == nil {
		writeAPIError(w, "device_disconnected")
		return nil
	}
	return c
}

// getUSBCLimit returns every settable limit type as {level, watts}, plus the
// read-only runtime limit.
func (s *server) getUSBCLimit(w http.ResponseWriter, r *http.Request) {
	if s.d.DeviceControl != nil {
		out := map[string]any{}
		for name, typ := range canonicalLimitTypes {
			level, err := s.d.DeviceControl.GetUSBCLimit(r.Context(), typ)
			if err != nil {
				writeError(w, err)
				return
			}
			out[name] = map[string]int{"level": level, "watts": proto.LevelToWatts(level)}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	out := map[string]any{}
	for name, typ := range map[string]int{"global": proto.LimitGlobal, "input": proto.LimitInput,
		"output": proto.LimitOutput, "runtime": proto.LimitRuntime} {
		lvl, err := c.USBCLimit(typ)
		if err != nil {
			writeAPIError(w, "ble_operation_failed")
			return
		}
		out[name] = map[string]int{"level": lvl, "watts": proto.LevelToWatts(lvl)}
	}
	writeJSON(w, 200, out)
}

func (s *server) setUSBCLimit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type  string `json:"type"`
		Watts int    `json:"watts"`
		Clear bool   `json:"clear"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	typ, ok := limitTypes[req.Type]
	if !ok {
		writeAPIError(w, "invalid_request")
		return
	}
	if req.Clear && s.d.DeviceControl != nil {
		if _, err := s.d.DeviceControl.DeleteUSBCLimit(r.Context(), typ); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return
	}
	level := proto.WattsToLevel(req.Watts)
	if !req.Clear && level < 0 {
		writeAPIError(w, "invalid_request")
		return
	}
	if !req.Clear && s.d.DeviceControl != nil {
		observed, err := s.d.DeviceControl.PutUSBCLimit(r.Context(), typ, level)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"watts": proto.LevelToWatts(observed), "level": observed})
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if req.Clear {
		if err := c.ClearUSBCLimit(typ); err != nil {
			writeAPIError(w, "ble_operation_failed")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "cleared"})
		return
	}
	if level < 0 {
		writeAPIError(w, "invalid_request")
		return
	}
	if err := c.SetUSBCLimit(typ, level); err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]int{"watts": req.Watts, "level": level})
}

func (s *server) getBypassThreshold(w http.ResponseWriter, r *http.Request) {
	if s.d.DeviceControl != nil {
		v, err := s.d.DeviceControl.GetBypassThreshold(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]float64{"volts": v})
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	v, err := c.BypassThreshold()
	if err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]float64{"volts": v})
}

func (s *server) setBypassThreshold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Volts float64 `json:"volts"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if req.Volts <= 0 || req.Volts > 60 {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl != nil {
		v, err := s.d.DeviceControl.PutBypassThreshold(r.Context(), req.Volts)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]float64{"volts": v})
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if err := c.SetBypassThreshold(req.Volts); err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]float64{"volts": req.Volts})
}

func (s *server) getSchedules(w http.ResponseWriter, r *http.Request) {
	c := s.ctl(w)
	if c == nil {
		return
	}
	list, err := c.Schedules()
	if err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	if list == nil {
		list = []proto.Timer{}
	}
	writeJSON(w, 200, list)
}

func (s *server) postSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     *int   `json:"id"`
		Status *int8  `json:"status"`
		Type   byte   `json:"type"`
		Hour   byte   `json:"hour"`
		Minute byte   `json:"minute"`
		Repeat uint32 `json:"repeat"`
		Action byte   `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if req.Type > proto.TimerMonthly || req.Hour > 23 || req.Minute > 59 || req.Action > 1 {
		writeAPIError(w, "invalid_request")
		return
	}
	status := int8(1) // default enabled
	if req.Status != nil {
		status = *req.Status
	}
	t := proto.Timer{Status: status, Type: req.Type, Hour: req.Hour,
		Minute: req.Minute, Repeat: req.Repeat, Action: req.Action}
	id := byte(0xFF) // add
	if req.ID != nil {
		if *req.ID < 0 || *req.ID > 254 {
			writeAPIError(w, "invalid_request")
			return
		}
		id = byte(*req.ID)
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	newID, err := c.UpsertSchedule(id, t)
	if err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	t.ID = newID
	writeJSON(w, 200, t)
}

func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 0 || id > 254 {
		writeAPIError(w, "invalid_request")
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if err := c.DeleteSchedule(byte(id)); err != nil {
		writeAPIError(w, "ble_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
