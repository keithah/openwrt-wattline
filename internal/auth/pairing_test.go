package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	if global, source := pairingFailureCounts(pairing, "source-a"); global != 2 || source != 2 {
		t.Fatalf("per-source-limited attempt changed failures: global=%d source=%d", global, source)
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

func TestPairingInvalidLabelPrecedesPINPolicyAndDoesNotCount(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Pairing, *time.Time)
		pin   string
	}{
		{name: "correct PIN", pin: "000042"},
		{name: "wrong PIN", pin: "999999"},
		{name: "closed mode", pin: "000042", setup: func(pairing *Pairing, _ *time.Time) { pairing.Close() }},
		{name: "expired PIN", pin: "000042", setup: func(_ *Pairing, now *time.Time) { *now = now.Add(time.Minute) }},
		{name: "rate limited", pin: "000042", setup: func(pairing *Pairing, _ *time.Time) {
			_, _, _ = pairing.Exchange("source", "999999", "valid")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
			pairing := NewPairing(pairingTestStore(t, &now), time.Minute, false,
				withPairingClock(func() time.Time { return now }),
				withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
				withPairingRateLimits(1, 2, time.Minute),
			)
			pairing.Open()
			if test.setup != nil {
				test.setup(pairing, &now)
			}
			beforeGlobal, beforeSource := pairingFailureCounts(pairing, "source")

			_, _, err := pairing.Exchange("source", test.pin, " invalid ")
			if !errors.Is(err, ErrInvalidLabel) {
				t.Fatalf("Exchange() error = %v, want ErrInvalidLabel", err)
			}
			afterGlobal, afterSource := pairingFailureCounts(pairing, "source")
			if afterGlobal != beforeGlobal || afterSource != beforeSource {
				t.Fatalf("invalid label changed failures: global %d -> %d, source %d -> %d",
					beforeGlobal, afterGlobal, beforeSource, afterSource)
			}
		})
	}
}

func TestPairingLimitedAttemptsDoNotGrowCountersOrSources(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	pairing := NewPairing(pairingTestStore(t, &now), 10*time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
		withPairingRateLimits(2, 3, time.Minute),
	)
	pairing.Open()
	for i := 0; i < 3; i++ {
		_, _, _ = pairing.Exchange(fmt.Sprintf("initial-%d", i), "999999", "valid")
	}
	for i := 0; i < 100; i++ {
		_, _, err := pairing.Exchange(fmt.Sprintf("rotating-%d", i), "999999", "valid")
		if !errors.Is(err, ErrInvalidOrExpiredPIN) {
			t.Fatalf("limited rotating source %d error = %v", i, err)
		}
	}

	pairing.mu.Lock()
	globalCount, sourceCount := pairing.global.count, len(pairing.sources)
	pairing.mu.Unlock()
	if globalCount != 3 {
		t.Fatalf("global count = %d, want bounded at limit 3", globalCount)
	}
	if sourceCount > 3 {
		t.Fatalf("source map size = %d, want at most global limit 3", sourceCount)
	}

	now = now.Add(time.Minute)
	secret, _, err := pairing.Exchange("recovered", "000042", "valid")
	if err != nil || secret == "" {
		t.Fatalf("exchange after rate window = secret %q, err %v", secret, err)
	}
	pairing.mu.Lock()
	defer pairing.mu.Unlock()
	if pairing.global.count != 0 || len(pairing.sources) > pairing.globalLimit {
		t.Fatalf("recovery state is not bounded: global=%d sources=%d", pairing.global.count, len(pairing.sources))
	}
}

func TestPairingGlobalExpiryPreservesLaterSourceLockout(t *testing.T) {
	start := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	now := start
	pairing := NewPairing(pairingTestStore(t, &now), 10*time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
		withPairingRateLimits(2, 10, time.Minute),
	)
	pairing.Open()
	_, _, _ = pairing.Exchange("source-a", "999999", "valid")

	now = start.Add(59 * time.Second)
	for range 2 {
		_, _, _ = pairing.Exchange("source-b", "999999", "valid")
	}
	now = start.Add(time.Minute)
	if _, _, err := pairing.Exchange("source-b", "000042", "blocked"); !errors.Is(err, ErrInvalidOrExpiredPIN) {
		t.Fatalf("source B after global reset error = %v, want still locked", err)
	}
	if global, source := pairingFailureCounts(pairing, "source-b"); global != 0 || source != 2 {
		t.Fatalf("staggered counts after global reset: global=%d source-b=%d", global, source)
	}

	now = start.Add(119 * time.Second)
	secret, _, err := pairing.Exchange("source-b", "000042", "recovered")
	if err != nil || secret == "" {
		t.Fatalf("source B after its own window = secret %q, err %v", secret, err)
	}
}

func TestPairingCapacityEvictsOnlyExpiredSources(t *testing.T) {
	start := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	now := start
	pairing := NewPairing(pairingTestStore(t, &now), 10*time.Minute, false,
		withPairingClock(func() time.Time { return now }),
		withPairingRandom(bytes.NewReader([]byte{0, 0, 42})),
		withPairingRateLimits(2, 3, time.Minute),
	)
	pairing.Open()
	_, _, _ = pairing.Exchange("expired-a", "999999", "valid")
	now = start.Add(59 * time.Second)
	_, _, _ = pairing.Exchange("live-b", "999999", "valid")
	_, _, _ = pairing.Exchange("live-c", "999999", "valid")

	now = start.Add(time.Minute)
	_, _, _ = pairing.Exchange("new-d", "999999", "valid")
	pairing.mu.Lock()
	_, hasExpired := pairing.sources["expired-a"]
	_, hasB := pairing.sources["live-b"]
	_, hasC := pairing.sources["live-c"]
	_, hasD := pairing.sources["new-d"]
	sourceCount := len(pairing.sources)
	pairing.mu.Unlock()
	if hasExpired || !hasB || !hasC || !hasD || sourceCount != 3 {
		t.Fatalf("capacity eviction: expired=%v live-b=%v live-c=%v new-d=%v count=%d",
			hasExpired, hasB, hasC, hasD, sourceCount)
	}
	if secret, _, err := pairing.Exchange("live-b", "000042", "tracked client"); err != nil || secret == "" {
		t.Fatalf("tracked source at capacity = secret %q, err %v", secret, err)
	}
	if _, _, err := pairing.Exchange("untracked-e", "000042", "new client"); !errors.Is(err, ErrInvalidOrExpiredPIN) {
		t.Fatalf("untracked source at capacity error = %v, want fail-closed sentinel", err)
	}

	now = start.Add(119 * time.Second)
	if secret, _, err := pairing.Exchange("untracked-e", "000042", "new client"); err != nil || secret == "" {
		t.Fatalf("untracked source after capacity recovery = secret %q, err %v", secret, err)
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

func pairingFailureCounts(pairing *Pairing, source string) (global, perSource int) {
	pairing.mu.Lock()
	defer pairing.mu.Unlock()
	return pairing.global.count, pairing.sources[source].count
}
