package rules

import (
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func snap(connected bool, status int8, level uint8, dcInput bool) state.Snapshot {
	return state.Snapshot{
		Connected: connected,
		Battery:   &proto.Battery{Status: status, Level: level},
		TypeC:     &proto.TypeCPort{DCInput: dcInput},
		DC:        &proto.DCPort{},
	}
}

var t0 = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

func inputRule(hold time.Duration) config.Rule {
	return config.Rule{Name: "r", Enabled: true, Condition: "input_power",
		State: "absent", Hold: hold, HysteresisMargin: 5,
		Actions: []string{"dc_off"}}
}

func TestHoldFiresAfterWindow(t *testing.T) {
	e, err := NewEngine([]config.Rule{inputRule(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ { // 0..9 min: holding, not fired
		if f := e.Tick(snap(true, 0, 50, false), t0.Add(time.Duration(i)*time.Minute)); len(f) != 0 {
			t.Fatalf("fired early at minute %d", i)
		}
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(10*time.Minute)); len(f) != 1 {
		t.Fatal("expected firing at 10m")
	}
	// Edge re-arm: still true → no repeat fire.
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(11*time.Minute)); len(f) != 0 {
		t.Fatal("re-fired without re-arm")
	}
	// Condition false (charging) re-arms; true again restarts the full hold.
	e.Tick(snap(true, 1, 50, false), t0.Add(12*time.Minute))
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(13*time.Minute)); len(f) != 0 {
		t.Fatal("must wait the full hold again")
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(23*time.Minute)); len(f) != 1 {
		t.Fatal("expected second firing after re-arm + hold")
	}
}

func TestChargingOrDCInputMeansPresent(t *testing.T) {
	e, _ := NewEngine([]config.Rule{inputRule(time.Minute)})
	// TypeC DCInput counts as input present even when not charging.
	e.Tick(snap(true, 0, 50, true), t0)
	if f := e.Tick(snap(true, 0, 50, true), t0.Add(2*time.Minute)); len(f) != 0 {
		t.Fatal("input present must not fire absent rule")
	}
}

func TestBlindResetsHold(t *testing.T) {
	e, _ := NewEngine([]config.Rule{inputRule(10 * time.Minute)})
	e.Tick(snap(true, 0, 50, false), t0)
	e.Tick(state.Snapshot{Connected: false}, t0.Add(5*time.Minute)) // blind
	// Reconnected at 15m: hold restarts from here. A no-reset engine would have
	// counted the elapsed 15m and fired now — asserting no fire catches that.
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(15*time.Minute)); len(f) != 0 {
		t.Fatal("blind gap must reset hold")
	}
	// 10m after reconnect (t0+25m) the fresh hold completes and fires.
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(25*time.Minute)); len(f) != 1 {
		t.Fatal("expected firing 10m after reconnect")
	}
}

func TestPowerLossShutdownPresetSemantics(t *testing.T) {
	rule := config.Rule{
		Name: "no_input_shutdown", Enabled: true, Condition: "input_power",
		State: "absent", Hold: 10 * time.Minute, HysteresisMargin: 5,
		Actions: []string{"shutdown"}, ConfirmShutdown: true,
	}
	e, err := NewEngine([]config.Rule{rule})
	if err != nil {
		t.Fatal(err)
	}

	absent := snap(true, 0, 50, false)
	present := snap(true, 1, 50, false)
	if f := e.Tick(absent, t0); len(f) != 0 {
		t.Fatalf("power loss fired at the start of the hold: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(9*time.Minute+59*time.Second)); len(f) != 0 {
		t.Fatalf("power loss fired before ten continuous minutes: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(10*time.Minute)); len(f) != 1 {
		t.Fatalf("expected one firing after ten continuous minutes, got %+v", f)
	} else if len(f[0].Rule.Actions) != 1 || f[0].Rule.Actions[0] != "shutdown" {
		t.Fatalf("preset fired unexpected actions: %+v", f[0].Rule.Actions)
	}
	if f := e.Tick(absent, t0.Add(10*time.Minute+time.Second)); len(f) != 0 {
		t.Fatalf("preset fired more than once without re-arm: %+v", f)
	}
	if f := e.Tick(present, t0.Add(11*time.Minute)); len(f) != 0 {
		t.Fatalf("restored input fired instead of re-arming the rule: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(12*time.Minute)); len(f) != 0 {
		t.Fatalf("re-armed power loss fired without a fresh hold: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(21*time.Minute+59*time.Second)); len(f) != 0 {
		t.Fatalf("re-armed power loss fired before the full hold: %+v", f)
	}
	if f := e.Tick(present, t0.Add(22*time.Minute)); len(f) != 0 {
		t.Fatalf("restored input fired instead of cancelling the hold: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(23*time.Minute)); len(f) != 0 {
		t.Fatalf("cancelled hold was not restarted: %+v", f)
	}
	if f := e.Tick(state.Snapshot{Connected: false}, t0.Add(28*time.Minute)); len(f) != 0 {
		t.Fatalf("disconnected tick fired while telemetry was blind: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(35*time.Minute)); len(f) != 0 {
		t.Fatalf("reconnect counted disconnected time toward the hold: %+v", f)
	}
	if f := e.Tick(absent, t0.Add(45*time.Minute)); len(f) != 1 {
		t.Fatalf("expected one shutdown firing after ten fresh minutes, got %+v", f)
	} else if len(f[0].Rule.Actions) != 1 || f[0].Rule.Actions[0] != "shutdown" {
		t.Fatalf("preset fired unexpected actions: %+v", f[0].Rule.Actions)
	}
	if f := e.Tick(absent, t0.Add(46*time.Minute)); len(f) != 0 {
		t.Fatalf("preset fired more than once without re-arm: %+v", f)
	}
}

func TestBatteryHysteresis(t *testing.T) {
	r := config.Rule{Name: "low", Enabled: true, Condition: "battery_level",
		Op: "below", Percent: 15, HysteresisMargin: 5, Actions: []string{"dc_off"}}
	e, _ := NewEngine([]config.Rule{r})
	if f := e.Tick(snap(true, 0, 14, false), t0); len(f) != 1 {
		t.Fatal("expected fire below threshold (no hold)")
	}
	// Bounce to 16 (< 15+5): must NOT re-arm.
	e.Tick(snap(true, 0, 16, false), t0.Add(time.Minute))
	if f := e.Tick(snap(true, 0, 14, false), t0.Add(2*time.Minute)); len(f) != 0 {
		t.Fatal("hysteresis violated")
	}
	// Recover to 20 (>= 15+5): re-arms.
	e.Tick(snap(true, 0, 20, false), t0.Add(3*time.Minute))
	if f := e.Tick(snap(true, 0, 14, false), t0.Add(4*time.Minute)); len(f) != 1 {
		t.Fatal("expected re-fire after hysteresis recovery")
	}
}

func TestRepeatEvery(t *testing.T) {
	r := inputRule(0)
	r.RepeatEvery = 30 * time.Minute
	e, _ := NewEngine([]config.Rule{r})
	if f := e.Tick(snap(true, 0, 50, false), t0); len(f) != 1 {
		t.Fatal("first fire")
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(29*time.Minute)); len(f) != 0 {
		t.Fatal("too early to repeat")
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(30*time.Minute)); len(f) != 1 {
		t.Fatal("expected repeat at 30m")
	}
}

func TestCronFiresOnceAtBoundary(t *testing.T) {
	r := config.Rule{Name: "night", Enabled: true, Condition: "schedule",
		Cron: "0 22 * * *", HysteresisMargin: 5, Actions: []string{"dc_off"}}
	e, err := NewEngine([]config.Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	e.Tick(snap(true, 0, 50, false), t0) // 12:00 — arms schedule, next = 22:00
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(9*time.Hour+59*time.Minute)); len(f) != 0 {
		t.Fatal("before boundary")
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(10*time.Hour+time.Second)); len(f) != 1 {
		t.Fatal("expected cron firing after 22:00")
	}
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(10*time.Hour+2*time.Second)); len(f) != 0 {
		t.Fatal("cron must not re-fire until next day")
	}
}

// TestCronDoesNotFireMissedOccurrenceOnReconnect covers the "missed cron
// fires on reconnect" bug: while disconnected, nextCron must be rebaselined
// past occurrences that pass during the outage, so reconnecting after a
// boundary was crossed does not fire it late.
func TestCronDoesNotFireMissedOccurrenceOnReconnect(t *testing.T) {
	r := config.Rule{Name: "night", Enabled: true, Condition: "schedule",
		Cron: "0 22 * * *", HysteresisMargin: 5, Actions: []string{"dc_off"}}
	e, err := NewEngine([]config.Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	// Arm connected at 12:00 -> next = 22:00 same day.
	if f := e.Tick(snap(true, 0, 50, false), t0); len(f) != 0 {
		t.Fatal("arming tick must not fire")
	}
	// Go disconnected before 22:00 and stay disconnected across the boundary.
	if f := e.Tick(state.Snapshot{Connected: false}, t0.Add(9*time.Hour+59*time.Minute)); len(f) != 0 {
		t.Fatal("disconnected tick before boundary must not fire")
	}
	if f := e.Tick(state.Snapshot{Connected: false}, t0.Add(10*time.Hour+30*time.Minute)); len(f) != 0 {
		t.Fatal("disconnected tick after boundary must not fire")
	}
	// Reconnect just after the missed boundary: must NOT fire the missed occurrence.
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(10*time.Hour+31*time.Minute)); len(f) != 0 {
		t.Fatal("reconnect must not fire a cron boundary missed while disconnected")
	}
	// Advance to the next day's 22:00 while connected: must fire normally.
	nextDay22 := t0.Add(24 * time.Hour) // t0 is 12:00, so +24h lands back at 12:00 next day
	// step forward to 22:00 the next day (10h after nextDay22's 12:00)
	if f := e.Tick(snap(true, 0, 50, false), nextDay22.Add(10*time.Hour+time.Second)); len(f) != 1 {
		t.Fatal("expected cron firing at the next day's 22:00 while connected")
	}
}

// TestSetRulesPreservesRuntimeState covers the "editing one rule re-arms and
// refires unrelated already-fired rules" bug: SetRules must carry over the
// armed/lastFired/holdStart state for rules that still exist by name.
func TestSetRulesPreservesRuntimeState(t *testing.T) {
	r := inputRule(0) // hold 0: fires immediately on an edge
	e, err := NewEngine([]config.Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	if f := e.Tick(snap(true, 0, 50, false), t0); len(f) != 1 {
		t.Fatalf("expected initial fire, got %d", len(f))
	}
	// Reload with the SAME rule plus a new unrelated one — as if the user
	// added another rule via the config UI.
	other := config.Rule{Name: "other", Enabled: true, Condition: "battery_level",
		Op: "below", Percent: 15, HysteresisMargin: 5, Actions: []string{"dc_off"}}
	if err := e.SetRules([]config.Rule{r, other}); err != nil {
		t.Fatal(err)
	}
	// Condition is still true; the original rule must NOT refire because its
	// armed=false state should have carried over.
	if f := e.Tick(snap(true, 0, 50, false), t0.Add(time.Minute)); len(f) != 0 {
		t.Fatalf("edited reload refired unrelated already-fired rule: %+v", f)
	}
	st := e.Status()
	for _, s := range st {
		if s.Name == "r" && s.Armed {
			t.Fatal("armed state was not preserved across SetRules")
		}
	}
}

func TestDisabledAndBadCron(t *testing.T) {
	r := inputRule(0)
	r.Enabled = false
	e, _ := NewEngine([]config.Rule{r})
	if f := e.Tick(snap(true, 0, 50, false), t0); len(f) != 0 {
		t.Fatal("disabled rule fired")
	}
	if _, err := NewEngine([]config.Rule{{Name: "x", Enabled: true,
		Condition: "schedule", Cron: "not a cron", Actions: []string{"dc_off"}}}); err == nil {
		t.Fatal("bad cron must error")
	}
}

func tempSnap(tempC float64) state.Snapshot {
	return state.Snapshot{
		Connected: true,
		Battery:   &proto.Battery{Status: 0, Level: 50},
		TypeC:     &proto.TypeCPort{TempC: tempC},
		DC:        &proto.DCPort{},
	}
}

func TestTemperatureCondition(t *testing.T) {
	r := config.Rule{Name: "hot", Enabled: true, Condition: "temperature",
		Op: "above", TempC: 60, Hold: 0, HysteresisMargin: 5,
		Actions: []string{"usbc_off"}}
	e, err := NewEngine([]config.Rule{r})
	if err != nil {
		t.Fatal(err)
	}
	// below threshold: no fire
	if f := e.Tick(tempSnap(55), t0); len(f) != 0 {
		t.Fatalf("fired at 55°C: %v", f)
	}
	// above threshold: fires
	if f := e.Tick(tempSnap(62), t0.Add(time.Minute)); len(f) != 1 {
		t.Fatalf("did not fire at 62°C: %v", f)
	}
	// still hot but already armed-and-fired: no repeat
	if f := e.Tick(tempSnap(63), t0.Add(2*time.Minute)); len(f) != 0 {
		t.Fatalf("re-fired without re-arm: %v", f)
	}
	// drops but within hysteresis band (>= 60-... no, above-op re-arms below 60-5=55): 58 should NOT re-arm
	e.Tick(tempSnap(58), t0.Add(3*time.Minute))
	if f := e.Tick(tempSnap(62), t0.Add(4*time.Minute)); len(f) != 0 {
		t.Fatalf("re-armed inside hysteresis band at 58°C: %v", f)
	}
	// drops below 55: re-arms, then fires again above 60
	e.Tick(tempSnap(54), t0.Add(5*time.Minute))
	if f := e.Tick(tempSnap(62), t0.Add(6*time.Minute)); len(f) != 1 {
		t.Fatalf("did not re-fire after re-arm: %v", f)
	}
}
