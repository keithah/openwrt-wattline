package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPairingAgentRegistrationCachesOneSuccess(t *testing.T) {
	var calls atomic.Int32
	agent := newPairingAgentRegistration(func() (func(), error) {
		calls.Add(1)
		return func() {}, nil
	})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := agent.Ensure(); err != nil {
				t.Errorf("Ensure() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("register calls = %d, want 1", got)
	}
}

func TestPairingAgentRegistrationCachesSuccessWithoutCancel(t *testing.T) {
	var calls int
	agent := newPairingAgentRegistration(func() (func(), error) {
		calls++
		return nil, nil
	})
	if err := agent.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Ensure(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("register calls = %d, want 1", calls)
	}
}

func TestPairingAgentRegistrationRetriesFailure(t *testing.T) {
	var calls int
	agent := newPairingAgentRegistration(func() (func(), error) {
		calls++
		if calls == 1 {
			return nil, errors.New("not ready")
		}
		return func() {}, nil
	})
	if err := agent.Ensure(); err == nil {
		t.Fatal("first Ensure() error = nil")
	}
	if err := agent.Ensure(); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("register calls = %d, want 2", calls)
	}
}

func TestPairingAgentRegistrationInvalidateCancelsAndAllowsRegistration(t *testing.T) {
	var calls, cancels int
	agent := newPairingAgentRegistration(func() (func(), error) {
		calls++
		return func() { cancels++ }, nil
	})
	if err := agent.Ensure(); err != nil {
		t.Fatal(err)
	}
	agent.Invalidate()
	agent.Invalidate()
	if cancels != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancels)
	}
	if err := agent.Ensure(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("register calls = %d, want 2", calls)
	}
}
