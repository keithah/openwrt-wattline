package ble

import (
	"errors"
	"sync"
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
			f.Close()
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

type connectorTimerRequest struct {
	delay time.Duration
	fire  chan time.Time
}

type controlledTimer struct{ fire chan time.Time }

func (t controlledTimer) C() <-chan time.Time { return t.fire }
func (controlledTimer) Stop() bool            { return true }

func controlledConnectorTimer(c *Connector) <-chan connectorTimerRequest {
	requests := make(chan connectorTimerRequest, 8)
	c.newTimer = func(delay time.Duration) connectorTimer {
		fire := make(chan time.Time, 1)
		requests <- connectorTimerRequest{delay: delay, fire: fire}
		return controlledTimer{fire: fire}
	}
	return requests
}

func TestConnectorDisarmInterruptsActiveReconnectDelay(t *testing.T) {
	var dials int32
	dialed := make(chan struct{}, 4)
	dial := func() (Transport, error) {
		atomic.AddInt32(&dials, 1)
		dialed <- struct{}{}
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	c := NewConnector(dial, state.NewStore(), nil)
	c.retryDelay, c.settle = time.Minute, 0
	timers := controlledConnectorTimer(c)
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	<-dialed
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })
	if err := c.Session().Close(); err != nil {
		t.Fatal(err)
	}
	active := <-timers
	if active.delay != time.Minute {
		t.Fatalf("active delay = %v, want %v", active.delay, time.Minute)
	}

	c.DisarmReconnect()
	active.fire <- time.Now() // stale timer firing must not authorize a dial
	select {
	case <-dialed:
		t.Fatal("connector dialed after disarming an active reconnect delay")
	case <-time.After(50 * time.Millisecond):
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("dials after disarm = %d, want 1", got)
	}
}

func TestConnectorArmReplacesActiveDefaultReconnectDelay(t *testing.T) {
	dialed := make(chan struct{}, 4)
	dial := func() (Transport, error) {
		dialed <- struct{}{}
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	c := NewConnector(dial, state.NewStore(), nil)
	c.retryDelay, c.settle = time.Minute, 0
	timers := controlledConnectorTimer(c)
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	<-dialed
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })
	if err := c.Session().Close(); err != nil {
		t.Fatal(err)
	}
	stale := <-timers
	if stale.delay != time.Minute {
		t.Fatalf("default delay = %v, want %v", stale.delay, time.Minute)
	}

	c.ArmReconnect(23 * time.Millisecond)
	var replacement connectorTimerRequest
	select {
	case replacement = <-timers:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ArmReconnect did not replace the active default delay")
	}
	if replacement.delay != 23*time.Millisecond {
		t.Fatalf("replacement delay = %v, want 23ms", replacement.delay)
	}
	replacement.fire <- time.Now()
	select {
	case <-dialed:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("replacement delay did not authorize reconnect")
	}
}

type trackedConnectorTimer struct {
	fire    chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func (t *trackedConnectorTimer) C() <-chan time.Time { return t.fire }
func (t *trackedConnectorTimer) Stop() bool {
	t.once.Do(func() { close(t.stopped) })
	return true
}

func TestConnectorPolicyChangeStopsAbandonedTimer(t *testing.T) {
	timers := make(chan *trackedConnectorTimer, 1)
	dial := func() (Transport, error) {
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	c := NewConnector(dial, state.NewStore(), nil)
	c.retryDelay, c.settle = time.Minute, 0
	c.newTimer = func(time.Duration) connectorTimer {
		timer := &trackedConnectorTimer{fire: make(chan time.Time, 1), stopped: make(chan struct{})}
		timers <- timer
		return timer
	}
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })
	if err := c.Session().Close(); err != nil {
		t.Fatal(err)
	}
	active := <-timers
	c.DisarmReconnect()
	select {
	case <-active.stopped:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("policy change abandoned the active reconnect timer without stopping it")
	}
}

type blockingHandshakeTransport struct {
	*fakeTransport
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func (t *blockingHandshakeTransport) ReadChar(uuid string) ([]byte, error) {
	if uuid == CharOTA {
		t.enterOnce.Do(func() { close(t.entered) })
		<-t.release
	}
	return t.fakeTransport.ReadChar(uuid)
}

func (t *blockingHandshakeTransport) releaseHandshake() {
	t.releaseOnce.Do(func() { close(t.release) })
}

func (t *blockingHandshakeTransport) Close() error {
	// A real transport close aborts the blocked GATT operation.
	t.releaseHandshake()
	return t.fakeTransport.Close()
}

func newBlockingHandshakeTransport() *blockingHandshakeTransport {
	f := newFake()
	scriptedHandshake(f)
	return &blockingHandshakeTransport{fakeTransport: f, entered: make(chan struct{}), release: make(chan struct{})}
}

func TestConnectorDisarmDuringHandshakeRemainsAuthoritative(t *testing.T) {
	var dials int32
	bt := newBlockingHandshakeTransport()
	dial := func() (Transport, error) {
		if atomic.AddInt32(&dials, 1) == 1 {
			return bt, nil
		}
		f := newFake()
		scriptedHandshake(f)
		return f, nil
	}
	store := state.NewStore()
	c := NewConnector(dial, store, nil)
	c.settle = 0
	stop := make(chan struct{})
	defer close(stop)
	go c.Run(stop)
	<-bt.entered
	c.DisarmReconnect()
	bt.releaseHandshake()
	waitForConnector(t, "handshake did not publish session", func() bool { return c.Session() != nil })
	if conn := store.Snapshot().Connection; conn == nil || conn.ReconnectArmed {
		t.Fatalf("ready state overwrote disarm: %+v", conn)
	}
	if err := bt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(50 * time.Millisecond):
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("disarmed handshake reconnected: %d dials", got)
	}
}

func TestConnectorStopClosesLiveSessionAndPublishesDisconnected(t *testing.T) {
	var f *fakeTransport
	dial := func() (Transport, error) {
		f = newFake()
		scriptedHandshake(f)
		return f, nil
	}
	store := state.NewStore()
	c := NewConnector(dial, store, nil)
	c.settle = 0
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { c.Run(stop); close(done) }()
	waitForConnector(t, "initial connection", func() bool { return c.Session() != nil })
	close(stop)
	<-done
	if c.Session() != nil {
		t.Fatal("stop left a published live session")
	}
	select {
	case <-f.Disconnected():
	default:
		t.Fatal("stop did not close live transport")
	}
	snap := store.Snapshot()
	if snap.Connected || snap.Connection == nil || snap.Connection.Phase != state.ConnectionDisconnected {
		t.Fatalf("stop state = connected %v, connection %+v", snap.Connected, snap.Connection)
	}
}

func TestConnectorStopDuringDialClosesReturnedTransport(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFake()
	scriptedHandshake(f)
	dial := func() (Transport, error) {
		close(entered)
		<-release
		return f, nil
	}
	store := state.NewStore()
	c := NewConnector(dial, store, nil)
	c.settle = 0
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { c.Run(stop); close(done) }()
	<-entered
	close(stop)
	close(release)
	<-done
	select {
	case <-f.Disconnected():
	default:
		t.Fatal("transport returned after stop was not closed")
	}
	if f.reads != 0 || c.Session() != nil || store.Snapshot().Connected {
		t.Fatalf("stop-during-dial leaked live state: reads=%d session=%v connected=%v",
			f.reads, c.Session(), store.Snapshot().Connected)
	}
}

func TestConnectorStopDuringHandshakeClosesFreshTransport(t *testing.T) {
	bt := newBlockingHandshakeTransport()
	store := state.NewStore()
	c := NewConnector(func() (Transport, error) { return bt, nil }, store, nil)
	c.settle = 0
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { c.Run(stop); close(done) }()
	<-bt.entered
	close(stop)
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		bt.Close() // prevent a leaked test goroutine after recording failure
		<-done
		t.Fatal("stop did not promptly abort the blocked handshake")
	}
	select {
	case <-bt.Disconnected():
	default:
		t.Fatal("stop during handshake did not close transport")
	}
	if c.Session() != nil || store.Snapshot().Connected {
		t.Fatalf("stop-during-handshake leaked live state: session=%v connected=%v",
			c.Session(), store.Snapshot().Connected)
	}
}
