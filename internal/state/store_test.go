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
	for i := 0; i < 66; i++ {
		select {
		case snap := <-ch:
			if len(snap.RecentCommands) == 1 && snap.RecentCommands[0].Phase == CommandConfirmed {
				foundTerminal = true
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out after %d saturated publications", i)
		}
	}
	if !foundTerminal {
		t.Fatal("final terminal command state was dropped from saturated subscriber")
	}
}

func TestSubscribeOverflowEvictsSlowSubscriberWithoutBlockingMutations(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()

	const queueCapacity = snapshotSubscriberQueueCapacity
	mutationsDone := make(chan struct{})
	go func() {
		for i := 0; i <= queueCapacity; i++ {
			s.SetIdentity(Identity{Model: fmt.Sprintf("model-%03d", i)})
		}
		close(mutationsDone)
	}()
	select {
	case <-mutationsDone:
	case <-time.After(time.Second):
		t.Fatal("store mutations blocked on a slow subscriber")
	}

	for i := 0; i < queueCapacity; i++ {
		select {
		case snap, ok := <-ch:
			if !ok {
				t.Fatalf("slow subscriber closed before queued snapshot %d", i)
			}
			want := fmt.Sprintf("model-%03d", i)
			if snap.Device == nil || snap.Device.Model != want {
				t.Fatalf("snapshot %d out of order: %+v", i, snap.Device)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out reading queued snapshot %d", i)
		}
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("overflowing snapshot was delivered instead of terminating the slow subscriber")
		}
	case <-time.After(time.Second):
		t.Fatal("overflow did not close the slow subscriber")
	}

	postOverflow := make(chan struct{})
	go func() {
		s.SetConnected(true)
		close(postOverflow)
	}()
	select {
	case <-postOverflow:
	case <-time.After(time.Second):
		t.Fatal("mutation blocked after slow-subscriber eviction")
	}
}

func TestSubscribeWithSnapshotOrdersInitialBeforeFutureUpdates(t *testing.T) {
	s := NewStore()
	s.SetIdentity(Identity{Model: "initial"})

	ch, initial, cancel := s.SubscribeWithSnapshot()
	defer cancel()
	s.SetIdentity(Identity{Model: "future"})

	if initial.Device == nil || initial.Device.Model != "initial" {
		t.Fatalf("atomic initial snapshot = %+v", initial.Device)
	}
	select {
	case update := <-ch:
		if update.Device == nil || update.Device.Model != "future" {
			t.Fatalf("first queued update = %+v, want future state", update.Device)
		}
	case <-time.After(time.Second):
		t.Fatal("future update was not delivered")
	}
}

func TestSubscribeDeliversEveryCommandTransitionInOrderWhenConsumerBlocked(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()
	const commandCount = 20
	for i := 0; i < commandCount; i++ {
		id := fmt.Sprintf("cmd-%02d", i)
		s.BeginCommand(Command{ID: id, Operation: "dc", Phase: CommandPending})
		s.FinishCommand(id, CommandConfirmed, map[string]any{"on": true}, nil)
	}

	for transition := 0; transition < commandCount*2; transition++ {
		id := fmt.Sprintf("cmd-%02d", transition/2)
		select {
		case snap := <-ch:
			if transition%2 == 0 {
				command, ok := snap.PendingCommands[id]
				if !ok || command.Phase != CommandPending {
					t.Fatalf("transition %d: want pending %s, got pending=%+v recent=%+v", transition, id, snap.PendingCommands, snap.RecentCommands)
				}
				continue
			}
			if _, ok := snap.PendingCommands[id]; ok {
				t.Fatalf("transition %d: terminal %s remained pending", transition, id)
			}
			if len(snap.RecentCommands) == 0 || snap.RecentCommands[len(snap.RecentCommands)-1].ID != id {
				t.Fatalf("transition %d: want terminal %s, got recent=%+v", transition, id, snap.RecentCommands)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for command transition %d", transition)
		}
	}
}

func TestSubscribeCancelInterruptsBlockedDelivery(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	s.SetConnected(true) // queued until a consumer receives or cancellation wins
	done := make(chan struct{})
	go func() {
		cancel()
		cancel() // cancellation remains safe and idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop a subscriber blocked on delivery")
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("canceled subscriber delivered a snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close the subscriber channel")
	}

	mutationDone := make(chan struct{})
	go func() {
		s.SetConnected(false)
		close(mutationDone)
	}()
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("store mutation blocked after subscriber cancellation")
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
