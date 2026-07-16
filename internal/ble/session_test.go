package ble

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/state"
)

// fakeTransport scripts read replies per char and records writes.
type fakeTransport struct {
	writes     [][2]string         // (uuid, hex)
	replies    map[string][][]byte // uuid -> FIFO of read replies
	subs       map[string]func([]byte)
	failWrites map[string]error // uuid -> error to return on write
	disc       chan struct{}
}

func newFake() *fakeTransport {
	return &fakeTransport{replies: map[string][][]byte{},
		subs: map[string]func([]byte){}, failWrites: map[string]error{},
		disc: make(chan struct{})}
}
func (f *fakeTransport) push(uuid, hexStr string) {
	b, _ := hex.DecodeString(hexStr)
	f.replies[uuid] = append(f.replies[uuid], b)
}
func (f *fakeTransport) WriteChar(uuid string, data []byte) error {
	f.writes = append(f.writes, [2]string{uuid, hex.EncodeToString(data)})
	return f.failWrites[uuid]
}
func (f *fakeTransport) ReadChar(uuid string) ([]byte, error) {
	q := f.replies[uuid]
	if len(q) == 0 {
		return nil, errors.New("no scripted reply for " + uuid)
	}
	f.replies[uuid] = q[1:]
	return q[0], nil
}
func (f *fakeTransport) Subscribe(uuid string, fn func([]byte)) error {
	f.subs[uuid] = fn
	return nil
}
func (f *fakeTransport) Disconnected() <-chan struct{} { return f.disc }

func scriptedHandshake(f *fakeTransport) {
	f.push(CharOTA, "010000000000000000000000000503") // mode 1, CID 0x0305
	f.push(CharModel, hex.EncodeToString([]byte("BP4SL3V2")))
	f.push(CharHWRev, hex.EncodeToString([]byte("V5#0305")))
	f.push(CharSWRev, hex.EncodeToString([]byte("1.4.9")))
	f.push(CharCmd, "1080002b72eb5a04dc") // DEVICE_ID reply
	f.push(CharCmd, "fe8000ff7f0000")     // FEATURES reply
	f.push(CharBattery, "010001def3def364d0f0f3a7a8c10000")
	f.push(CharDC, "0100a7e713b11bc201007f")
	f.push(CharTypeC, "0100000000000000faf0000300")
}

func TestHandshake(t *testing.T) {
	f := newFake()
	scriptedHandshake(f)
	store := state.NewStore()
	s := NewSession(f, store)
	s.settle = 0
	id, err := s.Handshake()
	if err != nil {
		t.Fatal(err)
	}
	if id.Model != "BP4SL3V2" || id.Firmware != "1.4.9" || id.CID != 0x0305 ||
		id.Features != 0x7FFF || id.MAC != "DC:04:5A:EB:72:2B" {
		t.Fatalf("identity: %+v", id)
	}
	snap := store.Snapshot()
	if snap.Battery == nil || snap.Battery.Level != 100 || snap.DC == nil || !snap.DC.Bypass {
		t.Fatalf("telemetry not stored: %+v", snap)
	}
	// Subscriptions registered on all three chars.
	for _, u := range []string{CharBattery, CharDC, CharTypeC} {
		if f.subs[u] == nil {
			t.Fatalf("no subscription on %s", u)
		}
	}
	// Notification updates flow to store.
	f.subs[CharBattery](mustHexB(t, "010001def3def332d0f0f3a7a8c10000")) // level 0x32=50
	if store.Snapshot().Battery.Level != 50 {
		t.Fatal("notification not applied")
	}
	// Current Time was written during handshake.
	found := false
	for _, w := range f.writes {
		if w[0] == CharTime {
			found = true
		}
	}
	if !found {
		t.Fatal("no time sync write")
	}
}

func mustHexB(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHandshakeBootloader(t *testing.T) {
	f := newFake()
	// f.push(CharOTA, "0200100000001083000000040005 0301000000") // mode 2 (spaces stripped below)
	f.replies[CharOTA] = [][]byte{mustHexB(t, "020010000000108300000004000503010000")}
	s := NewSession(f, state.NewStore())
	s.settle = 0
	if _, err := s.Handshake(); !errors.Is(err, ErrBootloader) {
		t.Fatalf("want ErrBootloader, got %v", err)
	}
}

func TestCommandsAndDisconnectAsSuccess(t *testing.T) {
	f := newFake()
	store := state.NewStore()
	s := NewSession(f, store)
	f.push(CharCmd, "018100")
	if err := s.DCControl(false); err != nil {
		t.Fatal(err)
	}
	// Bypass returns 0xFF result — must not error (exemption).
	f.push(CharCmd, "1481ff")
	if err := s.BypassControl(false); err != nil {
		t.Fatal(err)
	}
	// Restart: write fails with disconnect-ish error → success.
	f.failWrites[CharCmd] = errors.New("disconnected")
	if err := s.Restart(); err != nil {
		t.Fatalf("restart must treat write error as success: %v", err)
	}
	// Shutdown: same on the factory char.
	f.failWrites[CharFactory] = errors.New("disconnected")
	if err := s.Shutdown(); err != nil {
		t.Fatalf("shutdown must treat write error as success: %v", err)
	}
}
