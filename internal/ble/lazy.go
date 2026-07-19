package ble

import (
	"sync"
	"time"
)

// lazyPairOps defers BlueZ setup to first use, so a dongle or bluetoothd that
// comes up after the daemon still gets pairing without a restart. A failed
// resolve is retried on the next call, and a failed operation drops the
// cached ops so a dead bus connection or re-enumerated adapter heals on the
// next attempt instead of erroring forever.
type lazyPairOps struct {
	mu  sync.Mutex
	ops PairOps
}

func NewLazyPairOps() PairOps { return &lazyPairOps{} }

func (l *lazyPairOps) resolve() (PairOps, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ops == nil {
		ops, err := NewBlueZPairOps()
		if err != nil {
			return nil, err
		}
		l.ops = ops
	}
	return l.ops, nil
}

func (l *lazyPairOps) invalidate() {
	l.mu.Lock()
	l.ops = nil
	l.mu.Unlock()
}

func (l *lazyPairOps) do(op func(PairOps) error) error {
	ops, err := l.resolve()
	if err != nil {
		return err
	}
	if err := op(ops); err != nil {
		l.invalidate()
		return err
	}
	return nil
}

func (l *lazyPairOps) Scan(dur time.Duration) (found []Found, err error) {
	err = l.do(func(ops PairOps) error { found, err = ops.Scan(dur); return err })
	return found, err
}

func (l *lazyPairOps) Pair(mac string, recover bool, report PairProgress) error {
	return l.do(func(ops PairOps) error { return ops.Pair(mac, recover, report) })
}

func (l *lazyPairOps) Trust(mac string) error {
	return l.do(func(ops PairOps) error { return ops.Trust(mac) })
}

func (l *lazyPairOps) Unpair(mac string) error {
	return l.do(func(ops PairOps) error { return ops.Unpair(mac) })
}
