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

	mu    sync.Mutex
	sess  *Session
	fails int
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

func (c *Connector) Run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
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
		sess := NewSession(t, c.store)
		sess.settle = c.settle
		id, err := sess.Handshake()
		if err != nil {
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
