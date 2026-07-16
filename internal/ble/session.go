package ble

import (
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
	Model    string `json:"model"`
	HWRev    string `json:"hw_rev"`
	Firmware string `json:"firmware"`
	MAC      string `json:"mac"`
	CID      uint16 `json:"cid"`
	Features uint32 `json:"features"`
}

type Session struct {
	t      Transport
	store  *state.Store
	mu     sync.Mutex // serializes command transactions (API.md §3: one in flight)
	settle time.Duration
}

func NewSession(t Transport, store *state.Store) *Session {
	return &Session{t: t, store: store, settle: 2 * time.Second}
}

// command performs the write-then-read transaction on 0x4302.
func (s *Session) command(req []byte) (byte, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.t.WriteChar(CharCmd, req); err != nil {
		return 0, nil, err
	}
	reply, err := s.t.ReadChar(CharCmd)
	if err != nil {
		return 0, nil, err
	}
	return proto.ValidateReply(req, reply)
}

func (s *Session) readString(uuid string) string {
	b, err := s.t.ReadChar(uuid)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\x00")
}

// Handshake mirrors the PWA connect sequence (API.md §8).
func (s *Session) Handshake() (Identity, error) {
	time.Sleep(s.settle) // firmware settle time; PWA waits ~2s
	var id Identity

	if err := s.t.WriteChar(CharOTA, proto.OTAInfoQuery()); err != nil {
		return id, fmt.Errorf("ota info write: %w", err)
	}
	info, err := s.t.ReadChar(CharOTA)
	if err != nil {
		return id, fmt.Errorf("ota info read: %w", err)
	}
	mode, cid, err := proto.ParseOTAMode(info)
	if err != nil {
		return id, err
	}
	if mode == 2 {
		return id, ErrBootloader
	}
	id.CID = cid

	id.Model = s.readString(CharModel)
	id.HWRev = s.readString(CharHWRev)
	id.Firmware = s.readString(CharSWRev)

	if _, payload, err := s.command(proto.DeviceIDQuery()); err == nil {
		if mac, err := proto.ParseDeviceID(payload); err == nil {
			id.MAC = mac
		}
	}
	if _, payload, err := s.command(proto.FeaturesQuery()); err == nil {
		if f, err := proto.ParseFeatures(payload); err == nil {
			id.Features = f
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
		if err := s.t.Subscribe(sub.uuid, sub.apply); err != nil {
			return id, fmt.Errorf("subscribe %s: %w", sub.uuid, err)
		}
		if b, err := s.t.ReadChar(sub.uuid); err == nil {
			sub.apply(b)
		}
	}

	if err := s.t.WriteChar(CharTime, proto.CurrentTime(time.Now())); err != nil {
		log.Printf("wattline: time sync failed (non-fatal): %v", err)
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

// Restart succeeds by disconnecting (API.md §3.4) — write errors are success.
func (s *Session) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.t.WriteChar(CharCmd, proto.Restart()); err != nil {
		log.Printf("wattline: restart write ended with %v (expected: disconnect-as-success)", err)
	}
	s.store.SetConnected(false)
	return nil
}

// Shutdown writes the "FM" magic to 0x4310 (API.md §3.5) — disconnect-as-success.
func (s *Session) Shutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.t.WriteChar(CharFactory, proto.ShutdownMagic()); err != nil {
		log.Printf("wattline: shutdown write ended with %v (expected: disconnect-as-success)", err)
	}
	s.store.SetConnected(false)
	return nil
}
