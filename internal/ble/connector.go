package ble

import (
	"errors"
	"log"
	"sync"
	"time"

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
}

// logFailure reports whether the nth consecutive connect failure should be
// logged: the first, then once per ~30 attempts (~1/min at the 2s retry).
func logFailure(n int) bool { return n == 1 || n%30 == 0 }

func NewConnector(dial Dialer, store *state.Store, onSession func(*Session, Identity)) *Connector {
	return &Connector{dial: dial, store: store, onSession: onSession,
		retryDelay: 2 * time.Second, settle: 2 * time.Second}
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
			if errors.Is(err, ErrBootloader) {
				log.Printf("wattline: device in bootloader mode; leaving it alone")
				if !c.sleep(30*time.Second, stop) {
					return
				}
				continue
			}
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
		if c.onSession != nil {
			c.onSession(sess, id)
		}
		c.mu.Lock()
		c.sess = sess
		c.mu.Unlock()
		c.store.SetConnected(true)
		select {
		case <-stop:
			return
		case <-t.Disconnected():
			c.store.SetConnected(false)
			c.mu.Lock()
			c.sess = nil
			c.mu.Unlock()
			log.Printf("wattline: device disconnected; reconnecting")
			if !c.sleep(c.retryDelay, stop) {
				return
			}
		}
	}
}
