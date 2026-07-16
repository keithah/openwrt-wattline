// Package state holds the latest telemetry snapshot and a 24h ring buffer.
package state

import (
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

type Snapshot struct {
	Battery   *proto.Battery   `json:"battery,omitempty"`
	DC        *proto.DCPort    `json:"dc,omitempty"`
	TypeC     *proto.TypeCPort `json:"typec,omitempty"`
	Connected bool             `json:"connected"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type HistoryPoint struct {
	At     time.Time `json:"at"`
	Level  uint8     `json:"level"`
	Status int8      `json:"status"`
	DCW    float64   `json:"dc_w"`
	TypeCW float64   `json:"typec_w"`
}

const historyCap = 1440

type Store struct {
	mu      sync.Mutex
	snap    Snapshot
	history []HistoryPoint
	lastMin time.Time
	subs    map[chan Snapshot]struct{}
	now     func() time.Time // test hook
}

func NewStore() *Store {
	return &Store{subs: make(map[chan Snapshot]struct{}), now: time.Now}
}

func (s *Store) mutate(f func(*Snapshot)) {
	s.mu.Lock()
	f(&s.snap)
	s.snap.UpdatedAt = s.now()
	s.record()
	snap := s.snap
	for ch := range s.subs {
		select {
		case ch <- snap:
		default: // slow subscriber: drop
		}
	}
	s.mu.Unlock()
}

func (s *Store) record() {
	min := s.now().Truncate(time.Minute)
	if min.Equal(s.lastMin) {
		return
	}
	s.lastMin = min
	p := HistoryPoint{At: min}
	if s.snap.Battery != nil {
		p.Level, p.Status = s.snap.Battery.Level, s.snap.Battery.Status
	}
	if s.snap.DC != nil {
		p.DCW = s.snap.DC.Watts
	}
	if s.snap.TypeC != nil {
		p.TypeCW = s.snap.TypeC.Watts
	}
	s.history = append(s.history, p)
	if len(s.history) > historyCap {
		s.history = s.history[len(s.history)-historyCap:]
	}
}

func (s *Store) SetBattery(b proto.Battery) { s.mutate(func(sn *Snapshot) { sn.Battery = &b }) }
func (s *Store) SetDC(d proto.DCPort)       { s.mutate(func(sn *Snapshot) { sn.DC = &d }) }
func (s *Store) SetTypeC(c proto.TypeCPort) { s.mutate(func(sn *Snapshot) { sn.TypeC = &c }) }
func (s *Store) SetConnected(v bool)        { s.mutate(func(sn *Snapshot) { sn.Connected = v }) }

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

func (s *Store) History() []HistoryPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HistoryPoint, len(s.history))
	copy(out, s.history)
	return out
}

func (s *Store) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}
