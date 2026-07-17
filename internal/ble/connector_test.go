package ble

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/state"
)

func TestConnectorReconnects(t *testing.T) {
	var dials int32
	makeFake := func() (Transport, error) {
		f := newFake()
		scriptedHandshake(f)
		// close disconnect channel shortly after connect to force a reconnect
		go func() {
			time.Sleep(20 * time.Millisecond)
			close(f.disc)
		}()
		return f, nil
	}
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		return makeFake()
	}
	store := state.NewStore()
	var sessions int32
	c := NewConnector(dial, store, func(*Session, Identity) { atomic.AddInt32(&sessions, 1) })
	c.retryDelay = 5 * time.Millisecond
	c.settle = 0
	stop := make(chan struct{})
	go c.Run(stop)
	time.Sleep(200 * time.Millisecond)
	close(stop)
	if atomic.LoadInt32(&dials) < 2 {
		t.Fatalf("expected reconnects, got %d dials", dials)
	}
	if atomic.LoadInt32(&sessions) < 2 {
		t.Fatalf("expected multiple sessions, got %d", sessions)
	}
}

func TestLogFailureThrottles(t *testing.T) {
	if !logFailure(1) {
		t.Fatal("first failure must log")
	}
	for n := 2; n <= 29; n++ {
		if logFailure(n) {
			t.Fatalf("failure %d must not log", n)
		}
	}
	if !logFailure(30) {
		t.Fatal("30th failure must log")
	}
	if !logFailure(60) {
		t.Fatal("60th failure must log")
	}
}

// TestConnectedSetAfterSessionPublished covers the post-handshake race: a
// tick observing Connected==true must also observe the published session.
// onSession runs before store.SetConnected(true), so at the moment onSession
// fires the store must not yet report connected.
func TestConnectedSetAfterSessionPublished(t *testing.T) {
	dial := func() (Transport, error) {
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	store := state.NewStore()
	var sawUnconnectedInCallback int32
	c := NewConnector(dial, store, func(*Session, Identity) {
		if !store.Snapshot().Connected {
			atomic.StoreInt32(&sawUnconnectedInCallback, 1)
		}
	})
	c.settle = 0
	stop := make(chan struct{})
	go c.Run(stop)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	if atomic.LoadInt32(&sawUnconnectedInCallback) == 0 {
		t.Fatal("expected store not yet Connected when onSession runs")
	}
	if !store.Snapshot().Connected {
		t.Fatal("expected store Connected after session published")
	}
}

func TestConnectorBackoffOnDialError(t *testing.T) {
	var dials int32
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		return nil, errors.New("no adapter")
	}
	c := NewConnector(dial, state.NewStore(), func(*Session, Identity) {})
	c.retryDelay = 5 * time.Millisecond
	stop := make(chan struct{})
	go c.Run(stop)
	time.Sleep(60 * time.Millisecond)
	close(stop)
	if atomic.LoadInt32(&dials) < 2 {
		t.Fatalf("expected retries on dial error, got %d", dials)
	}
}

func TestConnectorPauseClosesSessionAndBlocksRedial(t *testing.T) {
	var dials int32
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	store := state.NewStore()
	c := NewConnector(dial, store, nil)
	c.retryDelay = 5 * time.Millisecond
	c.settle = 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)

	deadline := time.Now().Add(2 * time.Second)
	for c.Session() == nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if c.Session() == nil {
		t.Fatal("never connected")
	}

	c.Pause()
	deadline = time.Now().Add(2 * time.Second)
	for c.Session() != nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if c.Session() != nil {
		t.Fatal("session not closed on Pause")
	}
	if store.Snapshot().Connected {
		t.Fatal("store still reports connected after Pause")
	}
	before := atomic.LoadInt32(&dials)
	time.Sleep(100 * time.Millisecond)
	if after := atomic.LoadInt32(&dials); after != before {
		t.Fatalf("connector redialed while paused (%d -> %d)", before, after)
	}

	c.Resume()
	deadline = time.Now().Add(2 * time.Second)
	for c.Session() == nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if c.Session() == nil {
		t.Fatal("did not reconnect after Resume")
	}
}

func TestPauseDuringDialDropsFreshConnection(t *testing.T) {
	release := make(chan struct{})
	var closed int32
	dial := func() (Transport, error) {
		<-release // simulate a long BLE scan in flight
		f := newFake()
		scriptedHandshake(f)
		go func() {
			<-f.disc
			atomic.AddInt32(&closed, 1)
		}()
		return f, nil
	}
	store := state.NewStore()
	c := NewConnector(dial, store, nil)
	c.retryDelay = 5 * time.Millisecond
	c.settle = 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)

	time.Sleep(20 * time.Millisecond) // let Run enter dial()
	c.Pause()                         // pause lands while dial is in flight
	close(release)                    // dial now returns a live transport

	time.Sleep(100 * time.Millisecond)
	if c.Session() != nil {
		t.Fatal("paused connector committed a session from an in-flight dial")
	}
	if atomic.LoadInt32(&closed) == 0 {
		t.Fatal("the freshly dialed transport was not closed")
	}
	if store.Snapshot().Connected {
		t.Fatal("store reports connected while paused")
	}
}

func waitForConnector(t *testing.T, message string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !fn() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !fn() {
		t.Fatal(message)
	}
}

func TestConnectorPublishesAppAndBootloaderSessions(t *testing.T) {
	var dials int32
	dial := func() (Transport, error) {
		n := atomic.AddInt32(&dials, 1)
		f := newFake()
		if n == 1 {
			scriptedHandshake(f)
			go func() {
				time.Sleep(15 * time.Millisecond)
				f.Close()
			}()
		} else {
			f.available(CharOTA, CharModel, CharHWRev, CharFWRev, CharSWRev)
			f.push(CharOTA, "0200100000001083000000040005030100000000")
			f.push(CharModel, "425034534c335632")
			f.push(CharHWRev, "56352330333035")
			f.push(CharFWRev, "322e302e32")
			f.push(CharSWRev, "312e342e39")
		}
		return f, nil
	}
	modes := make(chan string, 2)
	store := state.NewStore()
	c := NewConnector(dial, store, func(s *Session, _ Identity) { modes <- s.Mode() })
	c.retryDelay, c.settle = time.Millisecond, 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)

	for _, want := range []string{"app", "ota"} {
		select {
		case got := <-modes:
			if got != want {
				t.Fatalf("session mode = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s session", want)
		}
	}
	waitForConnector(t, "bootloader connection state", func() bool {
		snap := store.Snapshot()
		return snap.Connection != nil && snap.Connection.Phase == state.ConnectionBootloader &&
			snap.Device != nil && snap.Device.Mode == "ota" && snap.Device.BootloaderFirmware == "2.0.2"
	})
}

func TestConnectorArmReconnectDelaysRestartRedial(t *testing.T) {
	var dials int32
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	c := NewConnector(dial, state.NewStore(), nil)
	c.retryDelay, c.settle = time.Millisecond, 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })

	c.ArmReconnect(80 * time.Millisecond)
	if err := c.Session().Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("restart redialed before armed delay: %d dials", got)
	}
	waitForConnector(t, "restart reconnect", func() bool { return atomic.LoadInt32(&dials) >= 2 })
}

func TestConnectorDisarmReconnectBlocksShutdownUntilResume(t *testing.T) {
	var dials int32
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	c := NewConnector(dial, state.NewStore(), nil)
	c.retryDelay, c.settle = time.Millisecond, 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })

	c.DisarmReconnect()
	if err := c.Session().Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("shutdown redialed while disarmed: %d dials", got)
	}
	c.ResumeReconnect()
	waitForConnector(t, "reconnect after resume", func() bool { return atomic.LoadInt32(&dials) >= 2 })
}
