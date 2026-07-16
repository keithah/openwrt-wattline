package ble

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeOps scripts PairOps for the manager tests.
type fakeOps struct {
	mu      sync.Mutex
	scanRes []Found
	scanErr error
	pairErr error
	calls   []string
	block   chan struct{} // if non-nil, Scan blocks until closed
}

func (f *fakeOps) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}
func (f *fakeOps) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *fakeOps) Scan(time.Duration) ([]Found, error) {
	f.log("scan")
	if f.block != nil {
		<-f.block
	}
	return f.scanRes, f.scanErr
}
func (f *fakeOps) Pair(mac string) error {
	f.log("pair " + mac)
	return f.pairErr
}
func (f *fakeOps) Trust(mac string) error {
	f.log("trust " + mac)
	return nil
}
func (f *fakeOps) Unpair(mac string) error {
	f.log("unpair " + mac)
	return nil
}

type pairingHarness struct {
	p       *Pairing
	ops     *fakeOps
	mu      sync.Mutex
	paused  int
	resumed int
	pins    []string
	saved   [][2]string
	waits   int
	waitOK  bool
	prepErr error
}

func newHarness(ops *fakeOps) *pairingHarness {
	h := &pairingHarness{ops: ops, waitOK: true}
	h.p = NewPairing(PairingDeps{
		Ops:     ops,
		ScanFor: time.Millisecond,
		Prepare: func() error { return h.prepErr },
		Pause:   func() { h.mu.Lock(); h.paused++; h.mu.Unlock() },
		Resume:  func() { h.mu.Lock(); h.resumed++; h.mu.Unlock() },
		SetPIN:  func(pin string) { h.mu.Lock(); h.pins = append(h.pins, pin); h.mu.Unlock() },
		WaitConnected: func() bool {
			h.mu.Lock()
			h.waits++
			ok := h.waitOK
			h.mu.Unlock()
			return ok
		},
		Persist: func(mac, pin string) error {
			h.mu.Lock()
			h.saved = append(h.saved, [2]string{mac, pin})
			h.mu.Unlock()
			return nil
		},
	})
	return h
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestScanPopulatesDevicesAndPausesConnector(t *testing.T) {
	ops := &fakeOps{scanRes: []Found{
		{MAC: "DC:04:5A:EB:72:2B", Name: "Link-Power-2", RSSI: -60},
	}}
	h := newHarness(ops)
	if err := h.p.StartScan(); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	waitFor(t, "scan to finish", func() bool { return h.p.Status().Stage == StageIdle })
	st := h.p.Status()
	if len(st.Devices) != 1 || st.Devices[0].Name != "Link-Power-2" {
		t.Fatalf("devices = %+v", st.Devices)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.paused != 1 || h.resumed != 1 {
		t.Fatalf("paused=%d resumed=%d, want 1/1", h.paused, h.resumed)
	}
}

func TestScanWhileBusyRejected(t *testing.T) {
	ops := &fakeOps{block: make(chan struct{})}
	h := newHarness(ops)
	if err := h.p.StartScan(); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	waitFor(t, "scanning stage", func() bool { return h.p.Status().Stage == StageScanning })
	if err := h.p.StartScan(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second StartScan err = %v, want ErrBusy", err)
	}
	if err := h.p.StartPair("AA:BB:CC:DD:EE:FF", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("StartPair during scan err = %v, want ErrBusy", err)
	}
	close(ops.block)
	waitFor(t, "idle", func() bool { return h.p.Status().Stage == StageIdle })
}

func TestScanErrorReported(t *testing.T) {
	ops := &fakeOps{scanErr: errors.New("hci down")}
	h := newHarness(ops)
	if err := h.p.StartScan(); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
	if st := h.p.Status(); st.Error != "hci down" {
		t.Fatalf("error = %q", st.Error)
	}
	// a new scan is allowed after an error
	if err := h.p.StartScan(); err != nil {
		t.Fatalf("StartScan after error: %v", err)
	}
}

func TestPairSuccessTrustsPersistsAndSetsPIN(t *testing.T) {
	ops := &fakeOps{}
	h := newHarness(ops)
	mac := "DC:04:5A:EB:72:2B"
	if err := h.p.StartPair(mac, "020555"); err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	waitFor(t, "paired stage", func() bool { return h.p.Status().Stage == StagePaired })
	calls := ops.got()
	want := []string{"pair " + mac, "trust " + mac}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("ops calls = %v, want %v", calls, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pins) != 1 || h.pins[0] != "020555" {
		t.Fatalf("pins = %v", h.pins)
	}
	if len(h.saved) != 1 || h.saved[0] != [2]string{mac, "020555"} {
		t.Fatalf("saved = %v", h.saved)
	}
	if h.paused != 1 || h.resumed != 1 {
		t.Fatalf("paused=%d resumed=%d, want 1/1", h.paused, h.resumed)
	}
	if h.waits != 1 {
		t.Fatalf("WaitConnected called %d times, want 1", h.waits)
	}
	if got := h.p.Status().Target; got != mac {
		t.Fatalf("target = %q", got)
	}
}

func TestPairEmptyPINKeepsConfiguredPIN(t *testing.T) {
	ops := &fakeOps{}
	h := newHarness(ops)
	if err := h.p.StartPair("AA:BB:CC:DD:EE:FF", ""); err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	waitFor(t, "paired stage", func() bool { return h.p.Status().Stage == StagePaired })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pins) != 0 {
		t.Fatalf("SetPIN called with %v, want no calls", h.pins)
	}
	if len(h.saved) != 1 || h.saved[0] != [2]string{"AA:BB:CC:DD:EE:FF", ""} {
		t.Fatalf("saved = %v", h.saved)
	}
}

func TestPairFailureSetsErrorResumesAndRestoresPIN(t *testing.T) {
	ops := &fakeOps{pairErr: errors.New("authentication failed")}
	h := newHarness(ops)
	if err := h.p.StartPair("AA:BB:CC:DD:EE:FF", "1234"); err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
	if st := h.p.Status(); st.Error != "authentication failed" {
		t.Fatalf("error = %q", st.Error)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resumed != 1 {
		t.Fatalf("resumed=%d, want 1", h.resumed)
	}
	if len(h.saved) != 0 {
		t.Fatalf("persist called on failure: %v", h.saved)
	}
	// The PIN override must be rolled back (empty = restore configured PIN)
	// so a later empty-pin pair or agent callback uses the configured value.
	if len(h.pins) != 2 || h.pins[0] != "1234" || h.pins[1] != "" {
		t.Fatalf("pins = %v, want [1234 \"\"]", h.pins)
	}
	for _, c := range ops.got() {
		if c == "trust AA:BB:CC:DD:EE:FF" {
			t.Fatal("trust called after failed pair")
		}
	}
}

func TestPairVerificationFailureDoesNotPersist(t *testing.T) {
	ops := &fakeOps{}
	h := newHarness(ops)
	h.mu.Lock()
	h.waitOK = false
	h.mu.Unlock()
	if err := h.p.StartPair("AA:BB:CC:DD:EE:FF", "1234"); err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.saved) != 0 {
		t.Fatalf("persisted despite failed verification: %v", h.saved)
	}
	if len(h.pins) != 2 || h.pins[1] != "" {
		t.Fatalf("PIN not restored: %v", h.pins)
	}
	if h.resumed != 1 {
		t.Fatalf("resumed=%d, want 1", h.resumed)
	}
}

func TestPairPrepareErrorFailsFast(t *testing.T) {
	ops := &fakeOps{}
	h := newHarness(ops)
	h.prepErr = errors.New("agent unavailable")
	if err := h.p.StartPair("AA:BB:CC:DD:EE:FF", ""); err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	waitFor(t, "error stage", func() bool { return h.p.Status().Stage == StageError })
	if len(ops.got()) != 0 {
		t.Fatalf("ops called despite Prepare failure: %v", ops.got())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.paused != 0 {
		t.Fatalf("paused connector despite Prepare failure")
	}
}

func TestUnpairSyncAndBusy(t *testing.T) {
	ops := &fakeOps{block: make(chan struct{})}
	h := newHarness(ops)
	if err := h.p.Unpair("AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatalf("Unpair: %v", err)
	}
	if calls := ops.got(); len(calls) != 1 || calls[0] != "unpair AA:BB:CC:DD:EE:FF" {
		t.Fatalf("calls = %v", calls)
	}
	if err := h.p.StartScan(); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	waitFor(t, "scanning", func() bool { return h.p.Status().Stage == StageScanning })
	if err := h.p.Unpair("AA:BB:CC:DD:EE:FF"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Unpair during scan err = %v, want ErrBusy", err)
	}
	close(ops.block)
}

func TestLazyPairOpsSurfacesResolveError(t *testing.T) {
	// On non-Linux dev hosts NewBlueZPairOps always errors; the lazy wrapper
	// must surface that per call instead of panicking or caching a nil.
	ops := NewLazyPairOps()
	if _, err := ops.Scan(time.Millisecond); err == nil {
		t.Skip("BlueZ available; nothing to assert on this host")
	}
	if err := ops.Pair("AA:BB:CC:DD:EE:FF"); err == nil {
		t.Fatal("Pair should surface resolve error")
	}
}
