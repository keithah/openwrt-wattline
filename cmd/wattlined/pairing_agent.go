package main

import "sync"

type pairingAgentRegistration struct {
	mu       sync.Mutex
	register func() (func(), error)
	active   bool
	cancel   func()
}

func newPairingAgentRegistration(register func() (func(), error)) *pairingAgentRegistration {
	return &pairingAgentRegistration{register: register}
}

func (a *pairingAgentRegistration) Ensure() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active {
		return nil
	}
	cancel, err := a.register()
	if err != nil {
		return err
	}
	a.active = true
	a.cancel = cancel
	return nil
}

func (a *pairingAgentRegistration) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.active = false
	a.cancel = nil
}
