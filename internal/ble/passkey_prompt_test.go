package ble

import (
	"errors"
	"testing"
	"time"
)

func TestPromptBlocksUntilPINIsSubmitted(t *testing.T) {
	p := NewPasskeyPrompt(time.Second)
	result := make(chan struct {
		pin string
		err error
	}, 1)
	go func() {
		pin, err := p.Wait(func() {})
		result <- struct {
			pin string
			err error
		}{pin, err}
	}()
	select {
	case <-result:
		t.Fatal("returned early")
	case <-time.After(20 * time.Millisecond):
	}
	if err := p.Submit("020555"); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil || r.pin != "020555" {
		t.Fatalf("got %#v", r)
	}
}

func TestPromptTimeoutAndLateSubmit(t *testing.T) {
	p := NewPasskeyPrompt(5 * time.Millisecond)
	_, err := p.Wait(func() {})
	if !errors.Is(err, ErrPasskeyTimeout) {
		t.Fatal(err)
	}
	if err := p.Submit("020555"); !errors.Is(err, ErrPasskeyNotWaiting) {
		t.Fatal(err)
	}
}

func TestPromptCancelAndDuplicates(t *testing.T) {
	p := NewPasskeyPrompt(time.Second)
	done := make(chan error, 1)
	go func() { _, e := p.Wait(func() {}); done <- e }()
	p.Cancel()
	if !errors.Is(<-done, ErrPasskeyCanceled) {
		t.Fatal("not canceled")
	}
	if err := p.Submit("020555"); !errors.Is(err, ErrPasskeyNotWaiting) {
		t.Fatal(err)
	}
}

func TestPromptRejectsInvalidAndConcurrentWait(t *testing.T) {
	p := NewPasskeyPrompt(time.Second)
	ready := make(chan struct{})
	go func() { p.Wait(func() { close(ready) }) }()
	<-ready
	for _, s := range []string{"12345", "1234567", "１２３４５６", "12a456"} {
		if err := p.Submit(s); !errors.Is(err, ErrPasskeyInvalid) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := p.Wait(func() {}); !errors.Is(err, ErrPasskeyAlreadyWaiting) {
		t.Fatal(err)
	}
	p.Cancel()
}
