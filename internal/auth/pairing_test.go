package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPairingOpenUsesSixDigitPINAndDefaultExpiry(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), 0, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
	)

	status := pairing.Open()
	if !status.Open || status.PIN != "000042" {
		t.Fatalf("Open() = %+v, want open with zero-padded PIN 000042", status)
	}
	if want := now.Add(5 * time.Minute); !status.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", status.ExpiresAt, want)
	}
	if got := pairing.Status(false); got.PIN != "" {
		t.Fatalf("non-admin Status PIN = %q, want omitted", got.PIN)
	}
	encoded, err := json.Marshal(pairing.Status(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "pin") || strings.Contains(string(encoded), "wlt_") {
		t.Fatalf("non-admin status leaks a secret: %s", encoded)
	}
	if got := pairing.Status(true).PIN; got != "000042" {
		t.Fatalf("admin Status PIN = %q, want 000042", got)
	}
}

func TestPairingWrongExpiredAndClosedAreIndistinguishable(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 7})),
	)
	pairing.Open()

	_, _, wrong := pairing.Exchange("192.0.2.1", "000008", "phone")
	now = now.Add(time.Minute)
	_, _, expired := pairing.Exchange("192.0.2.2", "000007", "phone")
	pairing.Close()
	_, _, closed := pairing.Exchange("192.0.2.3", "000007", "phone")

	for name, err := range map[string]error{"wrong": wrong, "expired": expired, "closed": closed} {
		if !errors.Is(err, ErrInvalidOrExpiredPIN) {
			t.Errorf("%s error = %v, want ErrInvalidOrExpiredPIN", name, err)
		}
	}
	if wrong != expired || expired != closed {
		t.Fatalf("errors differ: wrong=%v expired=%v closed=%v", wrong, expired, closed)
	}
	if got := pairing.Status(true); got.Open || got.PIN != "" || !got.ExpiresAt.IsZero() {
		t.Fatalf("closed Status = %+v", got)
	}
}

func TestPairingExchangeIssuesManagedTokenWithLabel(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	store := pairingTestStore(t, &now)
	pairing := NewPairing(store, time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 9})),
	)
	pairing.Open()

	secret, meta, err := pairing.Exchange("192.0.2.1", "000009", "Keith's phone")
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || meta.Label != "Keith's phone" || meta.Bootstrap {
		t.Fatalf("Exchange() = secret %q, meta %+v", secret, meta)
	}
	if principal, ok := store.Authenticate(secret); !ok || principal.Role != RoleClient || principal.TokenID != meta.ID {
		t.Fatalf("issued secret did not authenticate as managed client: %+v, %v", principal, ok)
	}
	encoded, err := json.Marshal(pairing.Status(true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("status leaks managed-token secret: %s", encoded)
	}
}

func TestPairingAlwaysOnRotatesAtEveryTTL(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), 0, true,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 1, 0, 0, 2, 0, 0, 3})),
	)

	first := pairing.Status(true)
	if !first.Open || first.PIN != "000001" {
		t.Fatalf("initial always-on status = %+v", first)
	}
	now = now.Add(5*time.Minute - time.Nanosecond)
	if got := pairing.Status(true).PIN; got != first.PIN {
		t.Fatalf("PIN rotated early: %q", got)
	}
	now = now.Add(time.Nanosecond)
	second := pairing.Status(true)
	if second.PIN != "000002" || !second.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("rotated status = %+v", second)
	}
	if _, _, err := pairing.Exchange("192.0.2.1", first.PIN, "stale"); !errors.Is(err, ErrInvalidOrExpiredPIN) {
		t.Fatalf("stale PIN error = %v", err)
	}
	now = now.Add(5 * time.Minute)
	if got := pairing.Status(true).PIN; got != "000003" {
		t.Fatalf("second rotation PIN = %q, want 000003", got)
	}
}

func TestPairingPerSourceAndGlobalLockoutsRecover(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), 10*time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
		withPairingRateLimits(2, 3, time.Minute),
	)
	pairing.Open()

	for range 2 {
		_, _, _ = pairing.Exchange("source-a", "999999", "bad")
	}
	if _, _, err := pairing.Exchange("source-a", "000042", "blocked-a"); !errors.Is(err, ErrInvalidOrExpiredPIN) {
		t.Fatalf("per-source lockout error = %v", err)
	}
	_, _, _ = pairing.Exchange("source-b", "999999", "bad")
	if _, _, err := pairing.Exchange("source-c", "000042", "blocked-global"); !errors.Is(err, ErrInvalidOrExpiredPIN) {
		t.Fatalf("global lockout error = %v", err)
	}

	now = now.Add(time.Minute)
	secret, _, err := pairing.Exchange("source-a", "000042", "recovered")
	if err != nil || secret == "" {
		t.Fatalf("exchange after rate window = secret %q, err %v", secret, err)
	}
}

func TestPairingBackwardClockDoesNotKeepStalePINOpen(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
	)
	pairing.Open()
	now = now.Add(-time.Hour)
	if got := pairing.Status(true); got.Open || got.PIN != "" {
		t.Fatalf("status after backward clock jump = %+v, want securely closed", got)
	}
}

func TestPairingConcurrentStatusAndExchange(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
	)
	pairing.Open()
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = pairing.Status(true)
			_, _, _ = pairing.Exchange("shared", "999999", "bad")
		}()
	}
	wait.Wait()
}

func pairingTestStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir()+"/tokens.json", "bootstrap-secret", withClock(func() time.Time { return *now }))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
