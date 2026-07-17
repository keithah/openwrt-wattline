// Package control coordinates BLE commands with cached device capabilities and
// telemetry. It deliberately leaves frame encoding, transaction serialization,
// and reconnect policy in the BLE session.
package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

var (
	ErrDisconnected     = errors.New("device disconnected")
	ErrUnsupported      = errors.New("operation unsupported")
	ErrAdvancedDisabled = errors.New("advanced operations disabled")
	ErrTimeout          = errors.New("command confirmation timeout")
	ErrNotFound         = errors.New("resource not found")
)

type Session interface {
	DCControl(bool) error
	TypeCOutput(bool) error
	BypassControl(bool) error
	GetUSBCLimit(int) (int, error)
	PutUSBCLimit(int, int) (int, error)
	DeleteUSBCLimit(int) (int, error)
	BypassThreshold() (float64, error)
	SetBypassThreshold(float64) error
	PutBypassThreshold(float64) (float64, error)
	ListTimers() ([]proto.Timer, error)
	AddTimer(proto.Timer) ([]proto.Timer, byte, error)
	PutTimer(byte, proto.Timer) ([]proto.Timer, error)
	DeleteTimer(byte) ([]proto.Timer, error)
	BarrierFree() (bool, error)
	SetBarrierFree(bool) (bool, error)
	SetRunningMode(byte) error
	USBFirmwareVersion() ([]byte, error)
	SetBLEPIN(uint32) error
	ReadClock() (time.Time, bool, error)
	SyncClock(time.Time, byte) error
	OTAInfo() (proto.OTAInfo, error)
	EnterOTA(context.Context) error
	ExitOTA(context.Context) error
	Restart() error
	Shutdown() error
}

// Connector is retained as an integration seam for callers constructing the
// service. Lifecycle reconnect policy is owned by the live BLE Session and the
// service never invokes Connector a second time.
type Connector interface {
	ArmReconnect(time.Duration)
	DisarmReconnect()
	ResumeReconnect()
}

type Service struct {
	resolve   func() Session
	store     *state.Store
	connector Connector
	advanced  func() bool

	confirmTimeout time.Duration
	bypassTimeout  time.Duration
	now            func() time.Time
	newID          func() (string, error)
}

func NewService(resolve func() Session, store *state.Store, connector Connector, advanced func() bool) *Service {
	if resolve == nil {
		resolve = func() Session { return nil }
	}
	if advanced == nil {
		advanced = func() bool { return false }
	}
	return &Service{
		resolve: resolve, store: store, connector: connector, advanced: advanced,
		confirmTimeout: 5 * time.Second, bypassTimeout: 10 * time.Second,
		now: time.Now, newID: randomCommandID,
	}
}

func randomCommandID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "cmd_" + hex.EncodeToString(raw[:]), nil
}

type telemetryKind byte

const (
	telemetryDC telemetryKind = iota
	telemetryTypeC
)

func (s *Service) SetDC(ctx context.Context, on bool) (proto.DCPort, error) {
	observed, err := s.reconcile(ctx, "dc_output", map[string]any{"enabled": on}, telemetryDC, s.confirmTimeout,
		func(sn state.Snapshot) bool { return supportsDC(sn) },
		func(session Session) error { return session.DCControl(on) },
		func(sn state.Snapshot) bool { return sn.DC != nil && sn.DC.Enabled == on },
	)
	return observedDC(observed), err
}

func (s *Service) SetTypeCOutput(ctx context.Context, on bool) (proto.TypeCPort, error) {
	wantMode := uint8(1)
	if on {
		wantMode = 3
	}
	observed, err := s.reconcile(ctx, "usbc_output", map[string]any{"enabled": on}, telemetryTypeC, s.confirmTimeout,
		func(sn state.Snapshot) bool { return supportsTypeCOutput(sn) },
		func(session Session) error { return session.TypeCOutput(on) },
		func(sn state.Snapshot) bool { return sn.TypeC != nil && sn.TypeC.Mode == wantMode },
	)
	return observedTypeC(observed), err
}

func (s *Service) SetBypass(ctx context.Context, on bool) (proto.DCPort, error) {
	observed, err := s.reconcile(ctx, "dc_bypass", map[string]any{"enabled": on}, telemetryDC, s.bypassTimeout,
		func(sn state.Snapshot) bool { return supportsBypass(sn) },
		func(session Session) error { return session.BypassControl(on) },
		func(sn state.Snapshot) bool { return sn.DC != nil && sn.DC.Bypass == on },
	)
	return observedDC(observed), err
}

func (s *Service) reconcile(
	ctx context.Context,
	operation string,
	requested any,
	kind telemetryKind,
	timeout time.Duration,
	supported func(state.Snapshot) bool,
	issue func(Session) error,
	confirmed func(state.Snapshot) bool,
) (any, error) {
	if err := ctx.Err(); err != nil {
		return observedTelemetry(s.store.Snapshot(), kind), err
	}
	session := s.resolve()
	started := s.now()
	id, err := s.newID()
	if err != nil {
		return observedTelemetry(s.store.Snapshot(), kind), fmt.Errorf("generate command ID: %w", err)
	}
	if id == "" {
		return observedTelemetry(s.store.Snapshot(), kind), errors.New("generate command ID: empty ID")
	}
	s.store.BeginCommand(state.Command{ID: id, Operation: operation, Requested: requested, StartedAt: started, UpdatedAt: started})
	if err := ctx.Err(); err != nil {
		observed := observedTelemetry(s.store.Snapshot(), kind)
		s.store.FinishCommand(id, state.CommandFailed, observed, commandError(err))
		return observed, err
	}

	finish := func(phase string, observed any, err error) (any, error) {
		s.store.FinishCommand(id, phase, observed, commandError(err))
		return observed, err
	}
	current := func() any { return observedTelemetry(s.store.Snapshot(), kind) }

	if session == nil {
		return finish(state.CommandFailed, current(), ErrDisconnected)
	}
	snap := s.store.Snapshot()
	if !supported(snap) {
		return finish(state.CommandFailed, observedTelemetry(snap, kind), ErrUnsupported)
	}
	if err := issue(session); err != nil {
		return finish(state.CommandFailed, current(), err)
	}
	if err := ctx.Err(); err != nil {
		return finish(state.CommandFailed, current(), err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err = s.store.Wait(waitCtx, confirmed)
	if err == nil {
		return finish(state.CommandConfirmed, observedTelemetry(snap, kind), nil)
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return finish(state.CommandTimeout, current(), ErrTimeout)
	}
	return finish(state.CommandFailed, current(), err)
}

func observedTelemetry(sn state.Snapshot, kind telemetryKind) any {
	if kind == telemetryTypeC {
		if sn.TypeC == nil {
			return proto.TypeCPort{}
		}
		return *sn.TypeC
	}
	if sn.DC == nil {
		return proto.DCPort{}
	}
	return *sn.DC
}

func observedDC(v any) proto.DCPort {
	if got, ok := v.(proto.DCPort); ok {
		return got
	}
	return proto.DCPort{}
}
func observedTypeC(v any) proto.TypeCPort {
	if got, ok := v.(proto.TypeCPort); ok {
		return got
	}
	return proto.TypeCPort{}
}

func commandError(err error) *state.CommandError {
	if err == nil {
		return nil
	}
	code := "ble_operation_failed"
	switch {
	case errors.Is(err, ErrDisconnected):
		code = "device_disconnected"
	case errors.Is(err, ErrUnsupported):
		code = "capability_unsupported"
	case errors.Is(err, ErrAdvancedDisabled):
		code = "advanced_disabled"
	case errors.Is(err, ErrTimeout):
		code = "command_timeout"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "canceled"
	}
	return &state.CommandError{Code: code, Message: err.Error()}
}

func identity(sn state.Snapshot) (state.Identity, bool) {
	if sn.Device == nil {
		return state.Identity{}, false
	}
	return *sn.Device, true
}

func appCommand(sn state.Snapshot) (state.Identity, bool) {
	id, ok := identity(sn)
	return id, ok && id.Mode == "app" && id.Characteristics["command"]
}

func supportsDC(sn state.Snapshot) bool {
	id, ok := appCommand(sn)
	return ok && id.FeatureSet.DCOutControl && id.Characteristics["dc"]
}
func supportsTypeCOutput(sn state.Snapshot) bool {
	id, ok := appCommand(sn)
	return ok && id.FeatureSet.USBOutputControl && id.Characteristics["typec"]
}
func supportsBypass(sn state.Snapshot) bool {
	id, ok := appCommand(sn)
	return ok && id.FeatureSet.DCBypassControl && id.Characteristics["dc"]
}
func supportsLimits(sn state.Snapshot) bool {
	id, ok := appCommand(sn)
	return ok && id.FeatureSet.USBPowerLimit
}
func supportsTimers(sn state.Snapshot) bool {
	id, ok := appCommand(sn)
	return ok && id.FeatureSet.DCOutScheduler
}

func (s *Service) sessionFor(ctx context.Context, supported func(state.Snapshot) bool, advanced bool) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session := s.resolve()
	if session == nil {
		return nil, ErrDisconnected
	}
	if !supported(s.store.Snapshot()) {
		return nil, ErrUnsupported
	}
	if advanced && !s.advanced() {
		return nil, ErrAdvancedDisabled
	}
	return session, nil
}

func (s *Service) advancedCommand(ctx context.Context, capability func(state.Identity) bool) (Session, error) {
	return s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := appCommand(sn)
		return ok && capability(id)
	}, true)
}

func (s *Service) GetBypassThreshold(ctx context.Context) (float64, error) {
	session, err := s.advancedCommand(ctx, func(id state.Identity) bool { return id.FeatureSet.DCBypass })
	if err != nil {
		return 0, err
	}
	return session.BypassThreshold()
}

func (s *Service) PutBypassThreshold(ctx context.Context, volts float64) (float64, error) {
	if volts <= 0 || volts > 60 {
		return 0, fmt.Errorf("bypass threshold %.3g is outside (0,60]", volts)
	}
	session, err := s.advancedCommand(ctx, func(id state.Identity) bool { return id.FeatureSet.DCBypass })
	if err != nil {
		return 0, err
	}
	return session.PutBypassThreshold(volts)
}

func (s *Service) BarrierFree(ctx context.Context) (bool, error) {
	session, err := s.advancedCommand(ctx, func(state.Identity) bool { return true })
	if err != nil {
		return false, err
	}
	return session.BarrierFree()
}

func (s *Service) SetBarrierFree(ctx context.Context, on bool) (bool, error) {
	session, err := s.advancedCommand(ctx, func(state.Identity) bool { return true })
	if err != nil {
		return false, err
	}
	return session.SetBarrierFree(on)
}

func (s *Service) SetRunningMode(ctx context.Context, mode byte) error {
	if mode > 1 {
		return fmt.Errorf("running mode %d is outside 0..1", mode)
	}
	session, err := s.advancedCommand(ctx, func(id state.Identity) bool { return id.FeatureSet.FactoryMode })
	if err != nil {
		return err
	}
	return session.SetRunningMode(mode)
}

func (s *Service) USBFirmwareVersion(ctx context.Context) ([]byte, error) {
	session, err := s.advancedCommand(ctx, func(id state.Identity) bool { return id.FeatureSet.USBPort })
	if err != nil {
		return nil, err
	}
	return session.USBFirmwareVersion()
}

func (s *Service) SetBLEPIN(ctx context.Context, pin uint32) error {
	if pin > 999999 {
		return fmt.Errorf("BLE PIN %d is outside 0..999999", pin)
	}
	session, err := s.advancedCommand(ctx, func(state.Identity) bool { return true })
	if err != nil {
		return err
	}
	return session.SetBLEPIN(pin)
}

func (s *Service) ReadClock(ctx context.Context) (time.Time, bool, error) {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && id.Mode == "app"
	}, true)
	if err != nil {
		return time.Time{}, false, err
	}
	return session.ReadClock()
}

func (s *Service) SyncClock(ctx context.Context, now time.Time) error {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && id.Mode == "app" && id.Characteristics["current_time"]
	}, true)
	if err != nil {
		return err
	}
	return session.SyncClock(now, 0)
}

func (s *Service) OTAInfo(ctx context.Context) (proto.OTAInfo, error) {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && (id.Mode == "app" || id.Mode == "ota") && id.Characteristics["ota"]
	}, true)
	if err != nil {
		return proto.OTAInfo{}, err
	}
	return session.OTAInfo()
}

func (s *Service) EnterOTA(ctx context.Context) error {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && id.Mode == "app" && id.Characteristics["ota"]
	}, true)
	if err != nil {
		return err
	}
	return session.EnterOTA(ctx)
}

func (s *Service) ExitOTA(ctx context.Context) error {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && id.Mode == "ota" && id.Characteristics["ota"]
	}, true)
	if err != nil {
		return err
	}
	return session.ExitOTA(ctx)
}

func (s *Service) Restart(ctx context.Context) error {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool { _, ok := appCommand(sn); return ok }, false)
	if err != nil {
		return err
	}
	if err := session.Restart(); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Service) Shutdown(ctx context.Context) error {
	session, err := s.sessionFor(ctx, func(sn state.Snapshot) bool {
		id, ok := identity(sn)
		return ok && id.Mode == "app" && id.FeatureSet.Shutdown && id.Characteristics["factory"]
	}, false)
	if err != nil {
		return err
	}
	if err := session.Shutdown(); err != nil {
		return err
	}
	return ctx.Err()
}
