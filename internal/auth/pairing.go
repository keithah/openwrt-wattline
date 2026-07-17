package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	defaultPairingTTL          = 5 * time.Minute
	defaultPairingRateWindow   = time.Minute
	defaultPairingSourceLimit  = 5
	defaultPairingGlobalLimit  = 20
	pairingPINPossibilities    = 1_000_000
	pairingPINRejectionCeiling = 16_000_000
)

var ErrInvalidOrExpiredPIN = errors.New("pairing PIN is invalid or expired")

type PairingOption func(*Pairing)

type PairingStatus struct {
	Open      bool      `json:"open"`
	ExpiresAt time.Time `json:"expires_at"`
	PIN       string    `json:"pin,omitempty"`
}

type Pairing struct {
	mu sync.Mutex

	tokens   *Store
	ttl      time.Duration
	alwaysOn bool
	now      func() time.Time
	random   io.Reader

	open      bool
	pin       string
	expiresAt time.Time
	lastNow   time.Time

	sourceLimit int
	globalLimit int
	rateWindow  time.Duration
	sources     map[string]pairingFailureWindow
	global      pairingFailureWindow
}

type pairingFailureWindow struct {
	started time.Time
	count   int
}

func withPairingClock(clock func() time.Time) PairingOption {
	return func(pairing *Pairing) {
		if clock != nil {
			pairing.now = clock
		}
	}
}

func withPairingRandom(random io.Reader) PairingOption {
	return func(pairing *Pairing) {
		if random != nil {
			pairing.random = random
		}
	}
}

func withPairingRateLimits(sourceLimit, globalLimit int, window time.Duration) PairingOption {
	return func(pairing *Pairing) {
		if sourceLimit > 0 {
			pairing.sourceLimit = sourceLimit
		}
		if globalLimit > 0 {
			pairing.globalLimit = globalLimit
		}
		if window > 0 {
			pairing.rateWindow = window
		}
	}
}

func NewPairing(tokens *Store, ttl time.Duration, alwaysOn bool, opts ...PairingOption) *Pairing {
	if ttl <= 0 {
		ttl = defaultPairingTTL
	}
	pairing := &Pairing{
		tokens:      tokens,
		ttl:         ttl,
		alwaysOn:    alwaysOn,
		now:         time.Now,
		random:      rand.Reader,
		sourceLimit: defaultPairingSourceLimit,
		globalLimit: defaultPairingGlobalLimit,
		rateWindow:  defaultPairingRateWindow,
		sources:     make(map[string]pairingFailureWindow),
	}
	for _, option := range opts {
		if option != nil {
			option(pairing)
		}
	}
	if alwaysOn {
		pairing.openLocked(pairing.observeNowLocked())
	}
	return pairing
}

func (p *Pairing) Open() PairingStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.observeNowLocked()
	p.openLocked(now)
	return p.statusLocked(true)
}

func (p *Pairing) Status(admin bool) PairingStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked(p.observeNowLocked())
	return p.statusLocked(admin)
}

func (p *Pairing) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeLocked()
}

func (p *Pairing) Exchange(source, pin, label string) (secret string, meta TokenMeta, err error) {
	if err := validateLabel(label); err != nil {
		return "", TokenMeta{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.observeNowLocked()
	p.refreshLocked(now)
	if p.rateLimitedLocked(source, now) {
		return "", TokenMeta{}, ErrInvalidOrExpiredPIN
	}
	if !p.open || subtle.ConstantTimeCompare([]byte(pin), []byte(p.pin)) != 1 {
		p.recordFailureLocked(source, now)
		return "", TokenMeta{}, ErrInvalidOrExpiredPIN
	}
	if p.tokens == nil {
		return "", TokenMeta{}, errors.New("pairing token store is unavailable")
	}
	return p.tokens.Issue(label)
}

func (p *Pairing) observeNowLocked() time.Time {
	now := p.now().UTC()
	if !p.lastNow.IsZero() && now.Before(p.lastNow) {
		p.sources = make(map[string]pairingFailureWindow)
		p.global = pairingFailureWindow{}
		if p.alwaysOn && p.open {
			p.openLocked(now)
		} else {
			p.closeLocked()
		}
	}
	p.lastNow = now
	return now
}

func (p *Pairing) refreshLocked(now time.Time) {
	if !p.open || now.Before(p.expiresAt) {
		return
	}
	if p.alwaysOn {
		p.openLocked(now)
		return
	}
	p.closeLocked()
}

func (p *Pairing) openLocked(now time.Time) {
	pin, err := generatePairingPIN(p.random)
	if err != nil {
		p.closeLocked()
		return
	}
	p.open = true
	p.pin = pin
	p.expiresAt = now.Add(p.ttl)
}

func (p *Pairing) closeLocked() {
	p.open = false
	p.pin = ""
	p.expiresAt = time.Time{}
}

func (p *Pairing) statusLocked(admin bool) PairingStatus {
	if !p.open {
		return PairingStatus{}
	}
	status := PairingStatus{Open: true, ExpiresAt: p.expiresAt}
	if admin {
		status.PIN = p.pin
	}
	return status
}

func (p *Pairing) rateLimitedLocked(source string, now time.Time) bool {
	p.expireFailureWindowsLocked(now)
	p.expireSourceWindowLocked(source, now)
	return p.global.count >= p.globalLimit || p.sources[source].count >= p.sourceLimit || !p.sourceCapacityAvailableLocked(source, now)
}

func (p *Pairing) recordFailureLocked(source string, now time.Time) {
	p.expireFailureWindowsLocked(now)
	p.expireSourceWindowLocked(source, now)
	p.global = incrementFailureWindow(p.global, now)
	if !p.sourceCapacityAvailableLocked(source, now) {
		return
	}
	p.sources[source] = incrementFailureWindow(p.sources[source], now)
}

func (p *Pairing) expireFailureWindowsLocked(now time.Time) {
	if windowExpired(p.global, now, p.rateWindow) {
		p.global = pairingFailureWindow{}
	}
}

func (p *Pairing) expireSourceWindowLocked(source string, now time.Time) {
	if windowExpired(p.sources[source], now, p.rateWindow) {
		delete(p.sources, source)
	}
}

func (p *Pairing) evictExpiredSourcesLocked(now time.Time) {
	for source, failures := range p.sources {
		if windowExpired(failures, now, p.rateWindow) {
			delete(p.sources, source)
		}
	}
}

func (p *Pairing) sourceCapacityAvailableLocked(source string, now time.Time) bool {
	if _, exists := p.sources[source]; exists || len(p.sources) < p.globalLimit {
		return true
	}
	p.evictExpiredSourcesLocked(now)
	return len(p.sources) < p.globalLimit
}

func incrementFailureWindow(window pairingFailureWindow, now time.Time) pairingFailureWindow {
	if window.count == 0 {
		window.started = now
	}
	window.count++
	return window
}

func windowExpired(window pairingFailureWindow, now time.Time, duration time.Duration) bool {
	return window.count > 0 && !now.Before(window.started.Add(duration))
}

func generatePairingPIN(random io.Reader) (string, error) {
	var sample [3]byte
	for {
		if _, err := io.ReadFull(random, sample[:]); err != nil {
			return "", fmt.Errorf("generate pairing PIN: %w", err)
		}
		value := int(sample[0])<<16 | int(sample[1])<<8 | int(sample[2])
		if value < pairingPINRejectionCeiling {
			return fmt.Sprintf("%06d", value%pairingPINPossibilities), nil
		}
	}
}
