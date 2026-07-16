// Package actions dispatches rule actions to the device and webhooks.
package actions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/keithah/openwrt-wattline/internal/state"
)

// Device is implemented by ble.Session (Task 8) and by test fakes.
type Device interface {
	DCControl(on bool) error
	TypeCOutput(on bool) error
	BypassControl(on bool) error
	Restart() error
	Shutdown() error
}

type WebhookPayload struct {
	Rule      string         `json:"rule"`
	Device    string         `json:"device"`
	Telemetry state.Snapshot `json:"telemetry"`
	FiredAt   time.Time      `json:"fired_at"`
}

type Executor struct {
	dev  Device
	name string
	http *http.Client
}

func NewExecutor(dev Device, deviceName string) *Executor {
	return &Executor{dev: dev, name: deviceName,
		http: &http.Client{Timeout: 10 * time.Second}}
}

func ValidActions() []string {
	return []string{"dc_on", "dc_off", "usbc_on", "usbc_off",
		"bypass_on", "bypass_off", "restart", "shutdown", "webhook:<url>"}
}

func ValidateAction(a string) error {
	switch a {
	case "dc_on", "dc_off", "usbc_on", "usbc_off",
		"bypass_on", "bypass_off", "restart", "shutdown":
		return nil
	}
	if url, ok := strings.CutPrefix(a, "webhook:"); ok && url != "" {
		return nil
	}
	return fmt.Errorf("unknown action %q (valid: %s)", a, strings.Join(ValidActions(), ", "))
}

func (x *Executor) one(a string, snap state.Snapshot, rule string, at time.Time) error {
	switch a {
	case "dc_on":
		return x.dev.DCControl(true)
	case "dc_off":
		return x.dev.DCControl(false)
	case "usbc_on":
		return x.dev.TypeCOutput(true)
	case "usbc_off":
		return x.dev.TypeCOutput(false)
	case "bypass_on":
		return x.dev.BypassControl(true)
	case "bypass_off":
		return x.dev.BypassControl(false)
	case "restart":
		return x.dev.Restart()
	case "shutdown":
		return x.dev.Shutdown()
	}
	if url, ok := strings.CutPrefix(a, "webhook:"); ok {
		body, err := json.Marshal(WebhookPayload{Rule: rule, Device: x.name, Telemetry: snap, FiredAt: at})
		if err != nil {
			return err
		}
		resp, err := x.http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("webhook %s: %w", url, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("webhook %s: HTTP %d", url, resp.StatusCode)
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", a)
}

// Execute runs every action even when earlier ones fail, collecting errors.
func (x *Executor) Execute(actions []string, snap state.Snapshot, rule string, at time.Time) []error {
	var errs []error
	for _, a := range actions {
		if err := x.one(a, snap, rule, at); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a, err))
		}
	}
	return errs
}
