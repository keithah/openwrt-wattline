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
