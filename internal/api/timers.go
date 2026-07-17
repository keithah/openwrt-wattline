package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/proto"
)

type timerRequest struct {
	Status *int8   `json:"status"`
	Type   *byte   `json:"type"`
	Hour   *byte   `json:"hour"`
	Minute *byte   `json:"minute"`
	Repeat *uint32 `json:"repeat"`
	Action *byte   `json:"action"`
}

func (r timerRequest) timer() (proto.Timer, error) {
	if r.Status == nil || r.Type == nil || r.Hour == nil || r.Minute == nil || r.Repeat == nil || r.Action == nil {
		return proto.Timer{}, errors.New("all timer fields are required")
	}
	timer := proto.Timer{Status: *r.Status, Type: *r.Type, Hour: *r.Hour, Minute: *r.Minute, Repeat: *r.Repeat, Action: *r.Action}
	if err := proto.ValidateTimerWrite(timer); err != nil {
		return proto.Timer{}, err
	}
	return timer, nil
}

func timerID(r *http.Request) (byte, error) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 0 || id > 254 {
		return 0, fmt.Errorf("invalid timer ID %q", r.PathValue("id"))
	}
	return byte(id), nil
}

func decodeTimer(r *http.Request) (proto.Timer, error) {
	var request timerRequest
	if err := decodeJSON(r, &request); err != nil {
		return proto.Timer{}, err
	}
	return request.timer()
}

func (s *server) listTimers(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	timers, err := service.ListTimers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if timers == nil {
		timers = []proto.Timer{}
	}
	writeJSON(w, http.StatusOK, timers)
}

func (s *server) addTimer(w http.ResponseWriter, r *http.Request) {
	timer, err := decodeTimer(r)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	timers, id, err := service.AddTimer(r.Context(), timer)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, observed := range timers {
		if observed.ID == id {
			writeJSON(w, http.StatusCreated, observed)
			return
		}
	}
	writeError(w, fmt.Errorf("%w: assigned timer %d missing from authoritative list", control.ErrBLE, id))
}

func (s *server) getTimer(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	id, err := timerID(r)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	timer, err := service.GetTimer(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, timer)
}

func (s *server) putTimer(w http.ResponseWriter, r *http.Request) {
	id, err := timerID(r)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	timer, err := decodeTimer(r)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if _, err := service.GetTimer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	timers, err := service.PutTimer(r.Context(), id, timer)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, observed := range timers {
		if observed.ID == id {
			writeJSON(w, http.StatusOK, observed)
			return
		}
	}
	writeError(w, control.ErrNotFound)
}

func (s *server) deleteTimer(w http.ResponseWriter, r *http.Request) {
	if err := requireNoBody(r); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	id, err := timerID(r)
	if err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	service := s.controlService(w)
	if service == nil {
		return
	}
	if _, err := service.GetTimer(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	timers, err := service.DeleteTimer(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if timers == nil {
		timers = []proto.Timer{}
	}
	writeJSON(w, http.StatusOK, struct {
		Deleted byte          `json:"deleted"`
		Timers  []proto.Timer `json:"timers"`
	}{Deleted: id, Timers: timers})
}
