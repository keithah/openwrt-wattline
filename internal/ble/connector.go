package ble

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type Dialer func() (Transport, error)

type connectorTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realConnectorTimer struct{ timer *time.Timer }

func (t realConnectorTimer) C() <-chan time.Time { return t.timer.C }
func (t realConnectorTimer) Stop() bool          { return t.timer.Stop() }

type Connector struct {
	dial       Dialer
	store      *state.Store
	onSession  func(*Session, Identity)
	retryDelay time.Duration
	settle     time.Duration
	newTimer   func(time.Duration) connectorTimer

	mu     sync.Mutex
	sess   *Session
	fails  int
	paused bool
	resume chan struct{} // non-nil while paused; closed by Resume

	reconnectArmed        bool
	reconnectDelay        time.Duration
	reconnectDelayPending bool
	policyChanged         chan struct{}
}

// logFailure reports whether the nth consecutive connect failure should be
// logged: the first, then once per ~30 attempts (~1/min at the 2s retry).
func logFailure(n int) bool { return n == 1 || n%30 == 0 }

func NewConnector(dial Dialer, store *state.Store, onSession func(*Session, Identity)) *Connector {
	return &Connector{dial: dial, store: store, onSession: onSession,
		retryDelay: 2 * time.Second, settle: 2 * time.Second,
		newTimer:       func(delay time.Duration) connectorTimer { return realConnectorTimer{time.NewTimer(delay)} },
		reconnectArmed: true, policyChanged: make(chan struct{})}
}

func (c *Connector) Session() *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

// Pause stops the connector from dialing and closes any live session,
// releasing the single-central Link-Power for pairing. A dial already in
// flight cannot be interrupted, but Run re-checks the paused flag after the
// dial and after the handshake and drops the fresh connection immediately.
// Resume re-enables the connector.
func (c *Connector) Pause() {
	c.mu.Lock()
	if !c.paused {
		c.paused = true
		c.resume = make(chan struct{})
	}
	sess := c.sess
	c.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

func (c *Connector) Resume() {
	c.mu.Lock()
	if c.paused {
		c.paused = false
		close(c.resume)
	}
	c.mu.Unlock()
}

// ArmReconnect enables reconnect and applies delay to the next disconnect.
// Lifecycle operations use this to allow the peripheral time to restart or
// switch firmware before scanning again.
func (c *Connector) ArmReconnect(delay time.Duration) {
	c.mu.Lock()
	c.reconnectArmed = true
	c.reconnectDelay = delay
	c.reconnectDelayPending = true
	c.signalPolicyChangedLocked()
	c.setConnectionReconnectLocked(true)
	c.mu.Unlock()
}

// DisarmReconnect prevents an expected disconnect (shutdown) from causing a
// new scan. ResumeReconnect explicitly returns to normal reconnect behavior.
func (c *Connector) DisarmReconnect() {
	c.mu.Lock()
	c.reconnectArmed = false
	c.reconnectDelay = 0
	c.reconnectDelayPending = false
	c.signalPolicyChangedLocked()
	c.setConnectionReconnectLocked(false)
	c.mu.Unlock()
}

func (c *Connector) ResumeReconnect() {
	c.ArmReconnect(0)
}

func (c *Connector) signalPolicyChangedLocked() {
	close(c.policyChanged)
	c.policyChanged = make(chan struct{})
}

func (c *Connector) setConnectionReconnectLocked(armed bool) {
	snap := c.store.Snapshot()
	connection := state.Connection{Phase: state.ConnectionDisconnected, ReconnectArmed: armed, Since: time.Now()}
	if snap.Connection != nil {
		connection = *snap.Connection
		connection.ReconnectArmed = armed
	}
	c.store.SetConnection(connection)
}

func (c *Connector) publishConnection(phase string) {
	c.mu.Lock()
	c.store.SetConnection(state.Connection{Phase: phase, ReconnectArmed: c.reconnectArmed, Since: time.Now()})
	c.mu.Unlock()
}

func (c *Connector) reconnectPolicy() (bool, time.Duration, bool, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delay, pending := c.retryDelay, c.reconnectDelayPending
	if pending {
		delay = c.reconnectDelay
	}
	return c.reconnectArmed, delay, pending, c.policyChanged
}

func (c *Connector) claimReconnect(policy <-chan struct{}, pending bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if policy != c.policyChanged || !c.reconnectArmed {
		return false
	}
	if pending {
		c.reconnectDelayPending = false
	}
	return true
}

// waitForDial returns the policy generation authorizing the next dial. A
// reconnect attempt observes its delay; the first attempt only waits for an
// armed policy. Any policy change interrupts the wait and starts it over.
func (c *Connector) waitForDial(stop <-chan struct{}, reconnect bool) (<-chan struct{}, bool) {
	for {
		armed, delay, pending, changed := c.reconnectPolicy()
		if !armed {
			select {
			case <-stop:
				return nil, false
			case <-changed:
				continue
			}
		}
		if !reconnect || delay <= 0 {
			if c.claimReconnect(changed, reconnect && pending) {
				return changed, true
			}
			continue
		}
		timer := c.newTimer(delay)
		select {
		case <-stop:
			timer.Stop()
			return nil, false
		case <-changed:
			timer.Stop()
			continue
		case <-timer.C():
			timer.Stop()
			if c.claimReconnect(changed, pending) {
				return changed, true
			}
		}
	}
}

func (c *Connector) policyCurrent(policy <-chan struct{}) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return policy == c.policyChanged && c.reconnectArmed
}

func (c *Connector) publishIdentity(id Identity, sess *Session) {
	chars := make(map[string]bool)
	for name, uuid := range map[string]string{
		"ota": CharOTA, "command": CharCmd, "battery": CharBattery, "dc": CharDC,
		"typec": CharTypeC, "factory": CharFactory, "model": CharModel,
		"firmware_revision": CharFWRev, "hardware_revision": CharHWRev,
		"software_revision": CharSWRev, "current_time": CharTime,
	} {
		chars[name] = sess.HasChar(uuid)
	}
	c.store.SetIdentity(state.Identity{
		Model: id.Model, HWRev: id.HWRev, AppFirmware: id.Firmware,
		BootloaderFirmware: id.BootloaderFirmware, MAC: id.MAC, CID: id.CID,
		Features: id.Features, FeatureSet: proto.DecodeFeatures(id.Features), Mode: id.Mode,
		Characteristics: chars,
	})
}

// pauseState returns the paused flag and the channel that unblocks waiters
// when Resume is called.
func (c *Connector) pauseState() (bool, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused, c.resume
}

func (c *Connector) isPaused() bool { p, _ := c.pauseState(); return p }

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func (c *Connector) closeSession(sess *Session) {
	if sess != nil {
		sess.Close()
	}
	c.clearSession(sess)
}

func (c *Connector) clearSession(sess *Session) {
	c.mu.Lock()
	if c.sess == sess {
		c.sess = nil
	}
	c.mu.Unlock()
	c.store.SetConnected(false)
	c.publishConnection(state.ConnectionDisconnected)
}

type handshakeResult struct {
	id  Identity
	err error
}

// handshake watches stop while synchronous GATT calls are in flight. Closing
// the transport aborts a blocked operation; joining done ensures no handshake
// goroutine or watcher outlives the connection attempt.
func (c *Connector) handshake(sess *Session, stop <-chan struct{}) (Identity, error, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	sess.setCancel(cancel)
	done := make(chan handshakeResult, 1)
	go func() {
		id, err := sess.HandshakeContext(ctx)
		done <- handshakeResult{id: id, err: err}
	}()
	select {
	case result := <-done:
		return result.id, result.err, false
	case <-stop:
		cancel()
		sess.Close()
		result := <-done
		return result.id, result.err, true
	}
}

func (c *Connector) Run(stop <-chan struct{}) {
	reconnect := false
	for {
		policy, ok := c.waitForDial(stop, reconnect)
		if !ok {
			c.closeSession(c.Session())
			return
		}
		reconnect = false
		if paused, resume := c.pauseState(); paused {
			select {
			case <-stop:
				c.closeSession(c.Session())
				return
			case <-resume:
			}
			continue
		}
		if !c.policyCurrent(policy) {
			reconnect = true
			continue
		}
		// stop may become ready after waitForDial selected a simultaneous timer.
		// Check at the final boundary so a stopped connector never enters dial.
		if stopped(stop) {
			c.closeSession(c.Session())
			return
		}
		t, err := c.dial()
		if stopped(stop) {
			if t != nil {
				t.Close()
			}
			c.closeSession(nil)
			return
		}
		// A reconnect policy change while dialing invalidates this attempt.
		// Close any returned transport and honor the replacement policy.
		if !c.policyCurrent(policy) {
			if t != nil {
				t.Close()
			}
			reconnect = true
			continue
		}
		if err != nil {
			c.fails++
			if logFailure(c.fails) {
				log.Printf("wattline: dial failed: %v", err)
			}
			reconnect = true
			continue
		}
		c.publishConnection(state.ConnectionHandshaking)
		// A pause may have landed while dial was in flight; the Link-Power
		// accepts one central, so drop the fresh connection immediately.
		if c.isPaused() {
			t.Close()
			continue
		}
		sess := NewSession(t, c.store)
		sess.lifecycle = c
		sess.settle = c.settle
		id, err, stoppedDuringHandshake := c.handshake(sess, stop)
		if stoppedDuringHandshake {
			c.clearSession(sess)
			return
		}
		if stopped(stop) {
			c.closeSession(sess)
			return
		}
		if err != nil {
			// Close, or the device stays occupied (it stops advertising while
			// connected) and every retry — and any pairing scan — finds nothing.
			sess.Close()
			c.fails++
			if logFailure(c.fails) {
				log.Printf("wattline: handshake failed: %v", err)
			}
			reconnect = true
			continue
		}
		if c.isPaused() {
			sess.Close()
			continue
		}
		c.fails = 0
		c.publishIdentity(id, sess)
		if c.onSession != nil {
			c.onSession(sess, id)
		}
		if stopped(stop) {
			c.closeSession(sess)
			return
		}
		c.mu.Lock()
		c.sess = sess
		c.mu.Unlock()
		c.store.SetConnected(true)
		phase := state.ConnectionReady
		if sess.Mode() == "ota" {
			phase = state.ConnectionBootloader
		}
		c.publishConnection(phase)
		select {
		case <-stop:
			c.closeSession(sess)
			return
		case <-t.Disconnected():
			sess.cancelContext()
			c.store.SetConnected(false)
			c.mu.Lock()
			c.sess = nil
			c.mu.Unlock()
			c.publishConnection(state.ConnectionDisconnected)
			log.Printf("wattline: device disconnected")
			reconnect = true
		}
	}
}
