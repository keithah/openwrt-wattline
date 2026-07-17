// Package rules evaluates automation rules against telemetry snapshots.
package rules

import (
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type Firing struct {
	Rule config.Rule
	At   time.Time
}

type RuleStatus struct {
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Armed      bool       `json:"armed"`
	HoldingFor string     `json:"holding_for,omitempty"`
	LastFired  *time.Time `json:"last_fired,omitempty"`
}

type ruleState struct {
	rule      config.Rule
	sched     cron.Schedule // nil unless condition == schedule
	nextCron  time.Time     // zero until first tick arms it
	holdStart time.Time     // zero when not holding
	armed     bool
	lastFired time.Time
}

type Engine struct {
	mu     sync.Mutex
	states []*ruleState
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func build(rules []config.Rule) ([]*ruleState, error) {
	out := make([]*ruleState, 0, len(rules))
	for _, r := range rules {
		st := &ruleState{rule: r, armed: true}
		if r.Condition == "schedule" {
			s, err := cronParser.Parse(r.Cron)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.Name, err)
			}
			st.sched = s
		}
		out = append(out, st)
	}
	return out, nil
}

func NewEngine(rules []config.Rule) (*Engine, error) {
	states, err := build(rules)
	if err != nil {
		return nil, err
	}
	return &Engine{states: states}, nil
}

func (e *Engine) SetRules(rules []config.Rule) error {
	states, err := build(rules)
	if err != nil {
		return err
	}
	e.mu.Lock()
	old := make(map[string]*ruleState, len(e.states))
	for _, s := range e.states {
		old[s.rule.Name] = s
	}
	for _, s := range states {
		if o := old[s.rule.Name]; o != nil {
			s.armed = o.armed
			s.lastFired = o.lastFired
			s.holdStart = o.holdStart
			if s.sched != nil && o.sched != nil {
				s.nextCron = o.nextCron
			}
		}
	}
	e.states = states
	e.mu.Unlock()
	return nil
}

// conditionTrue evaluates non-schedule conditions. Snapshot must be connected.
func conditionTrue(r config.Rule, s state.Snapshot) bool {
	switch r.Condition {
	case "input_power":
		present := (s.Battery != nil && s.Battery.Status == 1) ||
			(s.TypeC != nil && s.TypeC.DCInput)
		if r.State == "present" {
			return present
		}
		return !present
	case "battery_level":
		if s.Battery == nil {
			return false
		}
		if r.Op == "below" {
			return int(s.Battery.Level) < r.Percent
		}
		return int(s.Battery.Level) > r.Percent
	case "port_power":
		var w float64
		switch {
		case r.Port == "dc" && s.DC != nil:
			w = s.DC.Watts
		case r.Port == "usbc" && s.TypeC != nil:
			w = s.TypeC.Watts
		default:
			return false
		}
		if r.Op == "below" {
			return w < r.Watts
		}
		return w > r.Watts
	case "temperature":
		if s.TypeC == nil {
			return false
		}
		if r.Op == "below" {
			return s.TypeC.TempC < r.TempC
		}
		return s.TypeC.TempC > r.TempC
	}
	return false
}

// rearmTrue reports whether the condition has receded enough to re-arm.
func rearmTrue(r config.Rule, s state.Snapshot) bool {
	if r.Condition == "battery_level" && s.Battery != nil {
		if r.Op == "below" {
			return int(s.Battery.Level) >= r.Percent+r.HysteresisMargin
		}
		return int(s.Battery.Level) <= r.Percent-r.HysteresisMargin
	}
	// Temperature re-arms with the same margin (°C) to avoid flapping near
	// the threshold — e.g. an "above 60" cutoff only re-arms below 60-margin.
	if r.Condition == "temperature" && s.TypeC != nil {
		m := float64(r.HysteresisMargin)
		if r.Op == "below" {
			return s.TypeC.TempC >= r.TempC+m
		}
		return s.TypeC.TempC <= r.TempC-m
	}
	return !conditionTrue(r, s)
}

func (e *Engine) Tick(snap state.Snapshot, now time.Time) []Firing {
	e.mu.Lock()
	defer e.mu.Unlock()
	var fires []Firing
	for _, st := range e.states {
		if !st.rule.Enabled {
			continue
		}
		if !snap.Connected { // blind means not-firing (spec §2.3)
			st.holdStart = time.Time{}
			if st.sched != nil {
				st.nextCron = st.sched.Next(now) // skip occurrences that pass while disconnected
			}
			continue
		}
		if st.sched != nil {
			if st.nextCron.IsZero() {
				st.nextCron = st.sched.Next(now)
				continue
			}
			if !now.Before(st.nextCron) {
				st.lastFired = now
				st.nextCron = st.sched.Next(now)
				fires = append(fires, Firing{Rule: st.rule, At: now})
			}
			continue
		}
		if conditionTrue(st.rule, snap) {
			if st.holdStart.IsZero() {
				st.holdStart = now
			}
			held := now.Sub(st.holdStart) >= st.rule.Hold
			canRepeat := st.rule.RepeatEvery > 0 && !st.lastFired.IsZero() &&
				now.Sub(st.lastFired) >= st.rule.RepeatEvery
			if held && (st.armed || canRepeat) {
				st.armed = false
				st.lastFired = now
				fires = append(fires, Firing{Rule: st.rule, At: now})
			}
		} else {
			st.holdStart = time.Time{}
			if !st.armed && rearmTrue(st.rule, snap) {
				st.armed = true
			}
		}
	}
	return fires
}

func (e *Engine) Status() []RuleStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]RuleStatus, 0, len(e.states))
	for _, st := range e.states {
		rs := RuleStatus{Name: st.rule.Name, Enabled: st.rule.Enabled, Armed: st.armed}
		if !st.holdStart.IsZero() {
			rs.HoldingFor = time.Since(st.holdStart).Round(time.Second).String()
		}
		if !st.lastFired.IsZero() {
			lf := st.lastFired
			rs.LastFired = &lf
		}
		out = append(out, rs)
	}
	return out
}
