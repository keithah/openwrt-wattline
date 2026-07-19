package ble

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrPasskeyTimeout        = errors.New("passkey prompt timed out")
	ErrPasskeyCanceled       = errors.New("passkey prompt canceled")
	ErrPasskeyNotWaiting     = errors.New("passkey prompt is not waiting")
	ErrPasskeyInvalid        = errors.New("passkey must be exactly six ASCII digits")
	ErrPasskeyAlreadyWaiting = errors.New("passkey prompt already waiting")
)

type PasskeyPrompt struct {
	mu       sync.Mutex
	duration time.Duration
	waiting  bool
	terminal bool
	canceled bool
	active   bool
	consumed bool
	result   chan promptOutcome
	deadline time.Time
}

func (p *PasskeyPrompt) Activate(onWaiting func()) {
	p.mu.Lock()
	p.active = true
	p.waiting = true
	p.consumed = true
	p.consumed = false
	p.terminal = false
	p.deadline = time.Now().Add(p.duration)
	p.result = make(chan promptOutcome, 1)
	p.mu.Unlock()
	if onWaiting != nil {
		onWaiting()
	}
}
func (p *PasskeyPrompt) Deactivate()  { p.mu.Lock(); p.active = false; p.mu.Unlock(); p.Cancel() }
func (p *PasskeyPrompt) Active() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.active }

type promptOutcome struct {
	pin string
	err error
}

func NewPasskeyPrompt(timeout time.Duration) *PasskeyPrompt {
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	return &PasskeyPrompt{duration: timeout}
}

func (p *PasskeyPrompt) Wait(onWaiting func()) (string, error) {
	p.mu.Lock()
	if p.waiting {
		if p.consumed {
			p.mu.Unlock()
			return "", ErrPasskeyAlreadyWaiting
		}
		p.consumed = true
		ch, deadline := p.result, p.deadline
		p.mu.Unlock()
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		var out promptOutcome
		select {
		case out = <-ch:
		case <-t.C:
			out.err = ErrPasskeyTimeout
		}
		p.mu.Lock()
		p.waiting = false
		p.terminal = true
		p.deadline = time.Time{}
		p.result = nil
		p.mu.Unlock()
		return out.pin, out.err
	}
	if p.canceled {
		p.canceled = false
		p.mu.Unlock()
		return "", ErrPasskeyCanceled
	}
	p.waiting = true
	p.terminal = false
	p.deadline = time.Now().Add(p.duration)
	p.result = make(chan promptOutcome, 1)
	ch := p.result
	p.mu.Unlock()
	if onWaiting != nil {
		onWaiting()
	}
	t := time.NewTimer(time.Until(p.deadline))
	defer t.Stop()
	var out promptOutcome
	select {
	case out = <-ch:
	case <-t.C:
		out.err = ErrPasskeyTimeout
	}
	p.mu.Lock()
	p.waiting = false
	p.terminal = true
	p.deadline = time.Time{}
	p.result = nil
	p.mu.Unlock()
	return out.pin, out.err
}

func (p *PasskeyPrompt) Waiting() bool       { p.mu.Lock(); defer p.mu.Unlock(); return p.waiting }
func (p *PasskeyPrompt) Deadline() time.Time { p.mu.Lock(); defer p.mu.Unlock(); return p.deadline }

func (p *PasskeyPrompt) Submit(pin string) error {
	if len(pin) != 6 {
		return ErrPasskeyInvalid
	}
	for i := 0; i < 6; i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return ErrPasskeyInvalid
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.waiting || p.result == nil || p.terminal {
		return ErrPasskeyNotWaiting
	}
	p.terminal = true
	p.result <- promptOutcome{pin: pin}
	return nil
}

func (p *PasskeyPrompt) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting && p.result != nil && !p.terminal {
		p.terminal = true
		p.result <- promptOutcome{err: ErrPasskeyCanceled}
	} else {
		if !p.terminal {
			p.canceled = true
		}
	}
}
