package state

import (
	"fmt"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

func TestSnapshotCopyAndConnected(t *testing.T) {
	s := NewStore()
	if snap := s.Snapshot(); snap.Connected || snap.Battery != nil {
		t.Fatalf("zero store: %+v", snap)
	}
	s.SetConnected(true)
	s.SetBattery(proto.Battery{Level: 88, Status: 1})
	snap := s.Snapshot()
	if !snap.Connected || snap.Battery.Level != 88 || snap.UpdatedAt.IsZero() {
		t.Fatalf("%+v", snap)
	}
}

func TestHistoryOnePerMinuteAndCap(t *testing.T) {
	s := NewStore()
	base := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.SetBattery(proto.Battery{Level: 50})
	s.SetBattery(proto.Battery{Level: 51}) // same minute → no new point
	if got := len(s.History()); got != 1 {
		t.Fatalf("want 1 point, got %d", got)
	}
	for i := 1; i <= 1500; i++ {
		s.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		s.SetBattery(proto.Battery{Level: 50})
	}
	if got := len(s.History()); got != 1440 {
		t.Fatalf("want cap 1440, got %d", got)
	}
}

func TestSubscribe(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()
	s.SetConnected(true)
	select {
	case snap := <-ch:
		if !snap.Connected {
			t.Fatal("expected connected snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot published")
	}
}

func TestSubscribeSaturationRetainsFinalTerminalState(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()
	for i := 0; i < 64; i++ {
		s.SetIdentity(Identity{Model: fmt.Sprintf("model-%d", i)})
	}
	s.BeginCommand(Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending})
	s.FinishCommand("cmd-1", CommandConfirmed, map[string]any{"on": true}, nil)

	foundTerminal := false
	for {
		select {
		case snap := <-ch:
			if len(snap.RecentCommands) == 1 && snap.RecentCommands[0].Phase == CommandConfirmed {
				foundTerminal = true
			}
		default:
			if !foundTerminal {
				t.Fatal("final terminal command state was dropped from saturated subscriber")
			}
			return
		}
	}
}

func TestIdentityMutationDoesNotConsumeTelemetryHistoryMinute(t *testing.T) {
	s := NewStore()
	base := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.SetIdentity(Identity{Model: "BP4SL3V2"})
	s.SetBattery(proto.Battery{Level: 88, Status: 1})
	history := s.History()
	if len(history) != 1 || history[0].Level != 88 {
		t.Fatalf("history after metadata then telemetry: %+v", history)
	}
}
