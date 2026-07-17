package ble

import (
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/state"
)

// fakeTransport scripts read replies per char and records writes.
type fakeTransport struct {
	writes     [][2]string // (uuid, hex)
	reads      int
	readCalls  map[string]int
	writeCalls map[string]int
	subCalls   map[string]int
	replies    map[string][][]byte // uuid -> FIFO of read replies
	subs       map[string]func([]byte)
	chars      map[string]bool
	failWrites map[string]error // uuid -> error to return on write
	disc       chan struct{}
	closeOnce  sync.Once
}

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.disc) })
	return nil
}

func newFake() *fakeTransport {
	return &fakeTransport{replies: map[string][][]byte{},
		readCalls: map[string]int{}, writeCalls: map[string]int{}, subCalls: map[string]int{},
		subs: map[string]func([]byte){}, chars: map[string]bool{}, failWrites: map[string]error{},
		disc: make(chan struct{})}
}
func (f *fakeTransport) available(uuids ...string) {
	for _, uuid := range uuids {
		f.chars[uuid] = true
	}
}
func (f *fakeTransport) HasChar(uuid string) bool { return f.chars[uuid] }
func (f *fakeTransport) push(uuid, hexStr string) {
	b, _ := hex.DecodeString(hexStr)
	f.replies[uuid] = append(f.replies[uuid], b)
}
func (f *fakeTransport) WriteChar(uuid string, data []byte) error {
	f.writeCalls[uuid]++
	f.writes = append(f.writes, [2]string{uuid, hex.EncodeToString(data)})
	return f.failWrites[uuid]
}
func (f *fakeTransport) ReadChar(uuid string) ([]byte, error) {
	f.reads++
	f.readCalls[uuid]++
	q := f.replies[uuid]
	if len(q) == 0 {
		return nil, errors.New("no scripted reply for " + uuid)
	}
	f.replies[uuid] = q[1:]
	return q[0], nil
}

func TestCommandRejectsEmptyFrameWithoutTransportIO(t *testing.T) {
	f := newFake()
	s := NewSession(f, state.NewStore())

	_, _, err := s.command(nil)
	if err == nil || !strings.Contains(err.Error(), "invalid command frame") {
		t.Fatalf("command(nil) error = %v, want invalid command frame", err)
	}
	if len(f.writes) != 0 || f.reads != 0 {
		t.Fatalf("command(nil) transport calls: writes=%d reads=%d, want zero", len(f.writes), f.reads)
	}
}
func (f *fakeTransport) Subscribe(uuid string, fn func([]byte)) error {
	f.subCalls[uuid]++
	f.subs[uuid] = fn
	return nil
}
func (f *fakeTransport) Disconnected() <-chan struct{} { return f.disc }

func scriptedHandshake(f *fakeTransport) {
	f.available(CharOTA, CharModel, CharHWRev, CharFWRev, CharSWRev, CharCmd,
		CharBattery, CharDC, CharTypeC, CharTime)
	f.push(CharOTA, "010000000000000000000000000503") // mode 1, CID 0x0305
	f.push(CharModel, hex.EncodeToString([]byte("BP4SL3V2")))
	f.push(CharHWRev, hex.EncodeToString([]byte("V5#0305")))
	f.push(CharFWRev, hex.EncodeToString([]byte("2.0.2")))
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
	if id.Model != "BP4SL3V2" || id.Firmware != "1.4.9" || id.BootloaderFirmware != "2.0.2" ||
		id.Mode != "app" || id.CID != 0x0305 ||
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
			if got := mustHexB(t, w[1]); len(got) != 10 || got[9] != 1 {
				t.Fatalf("handshake Current Time = % x, want adjustment reason 1", got)
			}
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
	f.available(CharOTA, CharModel, CharHWRev, CharFWRev, CharSWRev)
	f.push(CharOTA, "0200100000001083000000040005030100000000")
	f.push(CharModel, hex.EncodeToString([]byte("BP4SL3V2")))
	f.push(CharHWRev, hex.EncodeToString([]byte("V5#0305")))
	f.push(CharFWRev, hex.EncodeToString([]byte("2.0.2")))
	f.push(CharSWRev, hex.EncodeToString([]byte("1.4.9")))
	s := NewSession(f, state.NewStore())
	s.settle = 0
	id, err := s.Handshake()
	if err != nil {
		t.Fatalf("bootloader handshake: %v", err)
	}
	if id.Mode != "ota" || s.Mode() != "ota" || id.CID != 0x0305 || id.BootloaderFirmware != "2.0.2" {
		t.Fatalf("bootloader identity: %+v, session mode %q", id, s.Mode())
	}
	for _, uuid := range []string{CharCmd, CharBattery, CharDC, CharTypeC, CharTime} {
		if f.readCalls[uuid] != 0 || f.writeCalls[uuid] != 0 || f.subCalls[uuid] != 0 {
			t.Fatalf("bootloader touched app characteristic %s: reads=%d writes=%d subs=%d",
				uuid, f.readCalls[uuid], f.writeCalls[uuid], f.subCalls[uuid])
		}
	}
}

func TestHandshakeSkipsMissingCharacteristics(t *testing.T) {
	f := newFake()
	f.available(CharOTA, CharModel, CharHWRev, CharFWRev, CharSWRev, CharCmd, CharDC)
	f.push(CharOTA, "010000000000000000000000000503")
	f.push(CharModel, hex.EncodeToString([]byte("BP4SL3V2")))
	f.push(CharHWRev, hex.EncodeToString([]byte("V5#0305")))
	f.push(CharFWRev, hex.EncodeToString([]byte("2.0.2")))
	f.push(CharSWRev, hex.EncodeToString([]byte("1.4.9")))
	f.push(CharCmd, "1080002b72eb5a04dc")
	f.push(CharCmd, "fe8000ff7f0000")
	f.push(CharDC, "0100a7e713b11bc201007f")

	s := NewSession(f, state.NewStore())
	s.settle = 0
	if _, err := s.Handshake(); err != nil {
		t.Fatal(err)
	}
	if f.subCalls[CharDC] != 1 {
		t.Fatalf("DC subscriptions = %d, want 1", f.subCalls[CharDC])
	}
	for _, uuid := range []string{CharBattery, CharTypeC, CharTime} {
		if f.readCalls[uuid] != 0 || f.writeCalls[uuid] != 0 || f.subCalls[uuid] != 0 {
			t.Fatalf("missing characteristic %s was touched", uuid)
		}
	}
}

func TestCharacteristicLookupAndAdvertisementPrefixMatching(t *testing.T) {
	f := newFake()
	f.available(CharOTA, CharTime)
	s := NewSession(f, state.NewStore())
	if !s.HasChar(CharOTA) || !s.HasChar(strings.ToUpper(CharTime)) || s.HasChar(CharCmd) {
		t.Fatalf("unexpected characteristic inventory: ota=%v time=%v cmd=%v",
			s.HasChar(CharOTA), s.HasChar(strings.ToUpper(CharTime)), s.HasChar(CharCmd))
	}
	prefixes := []string{"Link-Power", "PeakDo-OTA"}
	for _, localName := range []string{"Link-Power-2", "PeakDo-OTA"} {
		if !matchesAdvertisementLocalName(localName, prefixes) {
			t.Fatalf("fresh advertisement local name %q did not match", localName)
		}
	}
	if matchesAdvertisementLocalName("Cached-Link-Power", prefixes) {
		t.Fatal("non-matching fresh advertisement local name was accepted")
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
