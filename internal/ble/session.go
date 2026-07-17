package ble

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

var _ actions.Device = (*Session)(nil)

type Identity struct {
	Model              string `json:"model"`
	HWRev              string `json:"hw_rev"`
	Firmware           string `json:"firmware"`
	BootloaderFirmware string `json:"bootloader_firmware"`
	MAC                string `json:"mac"`
	CID                uint16 `json:"cid"`
	Features           uint32 `json:"features"`
	Mode               string `json:"mode"`
}

type Session struct {
	t               Transport
	store           *state.Store
	mu              sync.Mutex // serializes command transactions (API.md §3: one in flight)
	contextMu       sync.Mutex
	cancel          context.CancelFunc
	settle          time.Duration
	mode            string
	lifecycle       lifecyclePolicy
	disconnectGrace time.Duration
}

func NewSession(t Transport, store *state.Store) *Session {
	return &Session{t: t, store: store, settle: 2 * time.Second, disconnectGrace: 5 * time.Second}
}

func (s *Session) setCancel(cancel context.CancelFunc) {
	s.contextMu.Lock()
	s.cancel = cancel
	s.contextMu.Unlock()
}

func (s *Session) cancelContext() {
	s.contextMu.Lock()
	cancel := s.cancel
	s.contextMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close cancels session work, then drops the underlying BLE connection.
func (s *Session) Close() error {
	s.cancelContext()
	return s.t.Close()
}

// HasChar reports whether uuid was present in the discovery inventory.
func (s *Session) HasChar(uuid string) bool { return s.t.HasChar(strings.ToLower(uuid)) }

// Mode is "app" for the normal firmware and "ota" for the bootloader.
func (s *Session) Mode() string { return s.mode }

// command performs the write-then-read transaction on 0x4302.
func (s *Session) command(req []byte) (byte, []byte, error) {
	return s.commandContext(context.Background(), req)
}

func (s *Session) commandContext(ctx context.Context, req []byte) (byte, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commandContextLocked(ctx, req)
}

func (s *Session) commandLocked(req []byte) (byte, []byte, error) {
	return s.commandContextLocked(context.Background(), req)
}

func (s *Session) commandContextLocked(ctx context.Context, req []byte) (byte, []byte, error) {
	if len(req) == 0 {
		return 0, nil, fmt.Errorf("invalid command frame: empty request")
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if err := s.t.WriteChar(CharCmd, req); err != nil {
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	reply, err := s.t.ReadChar(CharCmd)
	if err != nil {
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	return proto.ValidateReply(req, reply)
}

func (s *Session) readString(uuid string) string {
	value, _ := s.readStringContext(context.Background(), uuid)
	return value
}

func (s *Session) readStringContext(ctx context.Context, uuid string) (string, error) {
	if !s.HasChar(uuid) {
		return "", ctx.Err()
	}
	b, err := s.readCharContext(ctx, uuid)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", nil
	}
	return strings.TrimRight(string(b), "\x00"), nil
}

func (s *Session) writeCharContext(ctx context.Context, uuid string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.t.WriteChar(uuid, data); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Session) readCharContext(ctx context.Context, uuid string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := s.t.ReadChar(uuid)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Handshake mirrors the PWA connect sequence (API.md §8).
func (s *Session) Handshake() (Identity, error) {
	return s.HandshakeContext(context.Background())
}

// HandshakeContext performs the handshake while preventing any subsequent
// GATT operation or state application once ctx is canceled.
func (s *Session) HandshakeContext(ctx context.Context) (Identity, error) {
	var id Identity
	if err := waitContext(ctx, s.settle); err != nil {
		return id, err
	}

	if err := s.writeCharContext(ctx, CharOTA, proto.OTAInfoQuery()); err != nil {
		return id, fmt.Errorf("ota info write: %w", err)
	}
	info, err := s.readCharContext(ctx, CharOTA)
	if err != nil {
		return id, fmt.Errorf("ota info read: %w", err)
	}
	ota, err := proto.ParseOTAInfo(info)
	if err != nil {
		return id, err
	}
	switch ota.Mode {
	case 1:
		s.mode = "app"
	case 2:
		s.mode = "ota"
	}
	id.Mode = s.mode
	id.CID = ota.CID

	if id.Model, err = s.readStringContext(ctx, CharModel); err != nil {
		return id, err
	}
	if id.HWRev, err = s.readStringContext(ctx, CharHWRev); err != nil {
		return id, err
	}
	if id.BootloaderFirmware, err = s.readStringContext(ctx, CharFWRev); err != nil {
		return id, err
	}
	if id.Firmware, err = s.readStringContext(ctx, CharSWRev); err != nil {
		return id, err
	}
	if s.mode == "ota" {
		if err := ctx.Err(); err != nil {
			return id, err
		}
		return id, nil
	}

	if s.HasChar(CharCmd) {
		if _, payload, err := s.commandContext(ctx, proto.DeviceIDQuery()); err == nil {
			if mac, err := proto.ParseDeviceID(payload); err == nil {
				id.MAC = mac
			}
		} else if ctx.Err() != nil {
			return id, ctx.Err()
		}
		if _, payload, err := s.commandContext(ctx, proto.FeaturesQuery()); err == nil {
			if f, err := proto.ParseFeatures(payload); err == nil {
				id.Features = f
			}
		} else if ctx.Err() != nil {
			return id, ctx.Err()
		}
	}

	subs := []struct {
		uuid  string
		apply func([]byte)
	}{
		{CharBattery, func(b []byte) {
			if v, err := proto.ParseBattery(b); err == nil {
				s.store.SetBattery(v)
			}
		}},
		{CharDC, func(b []byte) {
			if v, err := proto.ParseDC(b); err == nil {
				s.store.SetDC(v)
			}
		}},
		{CharTypeC, func(b []byte) {
			if v, err := proto.ParseTypeC(b); err == nil {
				s.store.SetTypeC(v)
			}
		}},
	}
	for _, sub := range subs {
		if !s.HasChar(sub.uuid) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return id, err
		}
		apply := func(b []byte) {
			if ctx.Err() == nil {
				sub.apply(b)
			}
		}
		if err := s.t.Subscribe(sub.uuid, apply); err != nil {
			return id, fmt.Errorf("subscribe %s: %w", sub.uuid, err)
		}
		if err := ctx.Err(); err != nil {
			return id, err
		}
		if b, err := s.readCharContext(ctx, sub.uuid); err == nil {
			if err := ctx.Err(); err != nil {
				return id, err
			}
			apply(b)
		} else if ctx.Err() != nil {
			return id, ctx.Err()
		}
	}

	if s.HasChar(CharTime) {
		if err := ctx.Err(); err != nil {
			return id, err
		}
		if err := s.writeCharContext(ctx, CharTime, proto.CurrentTimeAt(time.Now(), 1)); err != nil {
			if ctx.Err() != nil {
				return id, ctx.Err()
			}
			log.Printf("wattline: time sync failed (non-fatal): %v", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return id, err
	}
	return id, nil
}

func (s *Session) DCControl(on bool) error {
	_, _, err := s.command(proto.DCControl(on))
	return err
}

func (s *Session) TypeCOutput(on bool) error {
	_, _, err := s.command(proto.TypeCOutput(on))
	return err
}

// BypassControl ignores the result byte (0xFF/0xFD on success, live-verified);
// callers reconcile the real state from DcPortStatus telemetry.
func (s *Session) BypassControl(on bool) error {
	_, _, err := s.command(proto.BypassControl(on))
	return err
}
