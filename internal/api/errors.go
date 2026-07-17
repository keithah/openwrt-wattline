package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/keithah/openwrt-wattline/internal/control"
)

type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

var errorCatalog = map[string]struct {
	status  int
	message string
}{
	"unauthorized":           {http.StatusUnauthorized, "Bearer token is missing or invalid"},
	"invalid_request":        {http.StatusBadRequest, "Request is invalid"},
	"admin_required":         {http.StatusForbidden, "Administrator token required"},
	"advanced_disabled":      {http.StatusForbidden, "Advanced operations are disabled"},
	"not_found":              {http.StatusNotFound, "Resource was not found"},
	"capability_unsupported": {http.StatusConflict, "Operation is not supported"},
	"operation_in_progress":  {http.StatusConflict, "Pairing operation already in progress"},
	"ble_operation_failed":   {http.StatusBadGateway, "BLE operation failed"},
	"device_disconnected":    {http.StatusServiceUnavailable, "Link-Power is not connected"},
	"command_timeout":        {http.StatusGatewayTimeout, "Device telemetry did not confirm the command"},
	"invalid_or_expired_pin": {http.StatusUnauthorized, "Pairing PIN is invalid or expired"},
	"internal_error":         {http.StatusInternalServerError, "Internal server error"},
}

func writeAPIError(w http.ResponseWriter, code string) {
	entry, ok := errorCatalog[code]
	if !ok {
		entry = errorCatalog["internal_error"]
		code = "internal_error"
	}
	writeJSON(w, entry.status, apiErrorBody{Error: apiError{Code: code, Message: entry.message, Details: map[string]any{}}})
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, control.ErrDisconnected):
		writeAPIError(w, "device_disconnected")
	case errors.Is(err, control.ErrUnsupported):
		writeAPIError(w, "capability_unsupported")
	case errors.Is(err, control.ErrAdvancedDisabled):
		writeAPIError(w, "advanced_disabled")
	case errors.Is(err, control.ErrTimeout):
		writeAPIError(w, "command_timeout")
	case errors.Is(err, control.ErrNotFound):
		writeAPIError(w, "not_found")
	default:
		writeAPIError(w, "ble_operation_failed")
	}
}

// decodeJSON accepts exactly one JSON object. Pointer fields distinguish a
// required false/zero value from a missing field in endpoint request structs.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains trailing JSON")
		}
		return err
	}
	return nil
}

func requireNoBody(r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return errors.New("request body must be empty")
	}
	return nil
}
