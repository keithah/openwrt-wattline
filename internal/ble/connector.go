package ble

import (
	"log"
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type Dialer func() (Transport, error)

type Connector struct {
	dial       Dialer
	store      *state.Store
	onSession  func(*Session, Identity)
	retryDelay time.Duration
	settle     time.Duration

	mu     sync.Mutex
	sess   *Session
	fails  int
	paused bool
	resume chan struct{} // non-nil while paused; closed by Resume

	reconnectArmed  bool
	reconnectDelay  time.Duration
	reconnectResume chan struct{} // non-nil while disarmed
}

// logFailure reports whether the nth consecutive connect failure should be
// logged: the first, then once per ~30 attempts (~1/min at the 2s retry).
func logFailure(n int) bool { return n == 1 || n%30 == 0 }

func NewConnector(dial Dialer, store *state.Store, onSession func(*Session, Identity)) *Connector {
	return &Connector{dial: dial, store: store, onSession: onSession,
		retryDelay: 2 * time.Second, settle: 2 * time.Second, reconnectArmed: true}
}

func (c *Connector) Session() *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *Connector) sleep(d time.Duration, stop <-chan struct{}) bool {
	select {
	case <-stop:
		return false
	case <-time.After(d):
		return true
	}
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
	if c.reconnectResume != nil {
		close(c.reconnectResume)
		c.reconnectResume = nil
	}
	c.mu.Unlock()
	c.setConnectionReconnect(true)
}

// DisarmReconnect prevents an expected disconnect (shutdown) from causing a
// new scan. ResumeReconnect explicitly returns to normal reconnect behavior.
func (c *Connector) DisarmReconnect() {
	c.mu.Lock()
	c.reconnectArmed = false
	c.reconnectDelay = 0
	if c.reconnectResume == nil {
		c.reconnectResume = make(chan struct{})
	}
	c.mu.Unlock()
	c.setConnectionReconnect(false)
}

func (c *Connector) ResumeReconnect() {
	c.ArmReconnect(0)
}

func (c *Connector) setConnectionReconnect(armed bool) {
	snap := c.store.Snapshot()
	connection := state.Connection{Phase: state.ConnectionDisconnected, ReconnectArmed: armed, Since: time.Now()}
	if snap.Connection != nil {
		connection = *snap.Connection
		connection.ReconnectArmed = armed
	}
	c.store.SetConnection(connection)
}

func (c *Connector) reconnectPolicy() (bool, time.Duration, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reconnectArmed {
		return false, 0, c.reconnectResume
	}
	delay := c.reconnectDelay
	c.reconnectDelay = 0
	if delay == 0 {
		delay = c.retryDelay
	}
	return true, delay, nil
}

func (c *Connector) waitForReconnect(stop <-chan struct{}) bool {
	for {
		armed, delay, resume := c.reconnectPolicy()
		if armed {
			return c.sleep(delay, stop)
		}
		select {
		case <-stop:
			return false
		case <-resume:
		}
	}
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

func (c *Connector) Run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		if paused, resume := c.pauseState(); paused {
			select {
			case <-stop:
				return
			case <-resume:
			}
			continue
		}
		t, err := c.dial()
		if err != nil {
			c.fails++
			if logFailure(c.fails) {
				log.Printf("wattline: dial failed: %v", err)
			}
			if !c.sleep(c.retryDelay, stop) {
				return
			}
			continue
		}
		c.store.SetConnection(state.Connection{Phase: state.ConnectionHandshaking,
			ReconnectArmed: true, Since: time.Now()})
		// A pause may have landed while dial was in flight; the Link-Power
		// accepts one central, so drop the fresh connection immediately.
		if c.isPaused() {
			t.Close()
			continue
		}
		sess := NewSession(t, c.store)
		sess.settle = c.settle
		id, err := sess.Handshake()
		if err != nil {
			// Close, or the device stays occupied (it stops advertising while
			// connected) and every retry — and any pairing scan — finds nothing.
			t.Close()
			c.fails++
			if logFailure(c.fails) {
				log.Printf("wattline: handshake failed: %v", err)
			}
			if !c.sleep(c.retryDelay, stop) {
				return
			}
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
		c.mu.Lock()
		c.sess = sess
		c.mu.Unlock()
		c.store.SetConnected(true)
		phase := state.ConnectionReady
		if sess.Mode() == "ota" {
			phase = state.ConnectionBootloader
		}
		c.store.SetConnection(state.Connection{Phase: phase, ReconnectArmed: true, Since: time.Now()})
		select {
		case <-stop:
			return
		case <-t.Disconnected():
			c.store.SetConnected(false)
			c.mu.Lock()
			c.sess = nil
			armed := c.reconnectArmed
			c.mu.Unlock()
			c.store.SetConnection(state.Connection{Phase: state.ConnectionDisconnected,
				ReconnectArmed: armed, Since: time.Now()})
			log.Printf("wattline: device disconnected")
			if !c.waitForReconnect(stop) {
				return
			}
		}
	}
}
