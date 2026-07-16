package api

import (
	"encoding/json"
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
		http.Error(w, "device control unavailable on this platform", http.StatusServiceUnavailable)
		return nil
	}
	c := s.d.Control()
	if c == nil {
		http.Error(w, "device not connected", http.StatusServiceUnavailable)
		return nil
	}
	return c
}

// getUSBCLimit returns every settable limit type as {level, watts}, plus the
// read-only runtime limit.
func (s *server) getUSBCLimit(w http.ResponseWriter, r *http.Request) {
	c := s.ctl(w)
	if c == nil {
		return
	}
	out := map[string]any{}
	for name, typ := range map[string]int{"global": proto.LimitGlobal, "input": proto.LimitInput,
		"output": proto.LimitOutput, "runtime": proto.LimitRuntime} {
		lvl, err := c.USBCLimit(typ)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	typ, ok := limitTypes[req.Type]
	if !ok {
		http.Error(w, "type must be global|input|output", http.StatusBadRequest)
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if req.Clear {
		if err := c.ClearUSBCLimit(typ); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "cleared"})
		return
	}
	level := proto.WattsToLevel(req.Watts)
	if level < 0 {
		http.Error(w, "watts must be one of 30, 45, 60, 65, 100, 140", http.StatusBadRequest)
		return
	}
	if err := c.SetUSBCLimit(typ, level); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, 200, map[string]int{"watts": req.Watts, "level": level})
}

func (s *server) getBypassThreshold(w http.ResponseWriter, r *http.Request) {
	c := s.ctl(w)
	if c == nil {
		return
	}
	v, err := c.BypassThreshold()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, 200, map[string]float64{"volts": v})
}

func (s *server) setBypassThreshold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Volts float64 `json:"volts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Volts <= 0 || req.Volts > 60 {
		http.Error(w, "volts must be between 0 and 60", http.StatusBadRequest)
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if err := c.SetBypassThreshold(req.Volts); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
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
		http.Error(w, err.Error(), http.StatusBadGateway)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Type > proto.TimerMonthly || req.Hour > 23 || req.Minute > 59 || req.Action > 1 {
		http.Error(w, "invalid timer: type 0-3, hour 0-23, minute 0-59, action 0-1", http.StatusBadRequest)
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
			http.Error(w, "id out of range", http.StatusBadRequest)
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
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	t.ID = newID
	writeJSON(w, 200, t)
}

func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 0 || id > 254 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	c := s.ctl(w)
	if c == nil {
		return
	}
	if err := c.DeleteSchedule(byte(id)); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
