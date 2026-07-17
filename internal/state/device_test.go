package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

func TestIdentitySnapshotJSONCompatibilityAndCopy(t *testing.T) {
	s := NewStore()
	s.SetBattery(proto.Battery{Level: 88, Status: 1})
	s.SetDC(proto.DCPort{})
	s.SetTypeC(proto.TypeCPort{})
	s.SetIdentity(Identity{
		Model:           "BP4SL3V2",
		MAC:             "DC:04:5A:EB:72:2B",
		CID:             773,
		Features:        3,
		FeatureSet:      proto.DecodeFeatures(3),
		Mode:            "app",
		Characteristics: map[string]bool{"current_time": true},
	})

	snap := s.Snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"battery", "dc", "typec", "connected", "updated_at", "device"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("snapshot JSON lost top-level %q field: %s", field, b)
		}
	}
	if got := snap.Device.Characteristics["current_time"]; !got {
		t.Fatal("identity mutation was not retained")
	}

	snap.Battery.Level = 1
	snap.Device.Characteristics["current_time"] = false
	fresh := s.Snapshot()
	if fresh.Battery.Level != 88 || !fresh.Device.Characteristics["current_time"] {
		t.Fatalf("Snapshot returned aliases into store: %+v", fresh)
	}
}

func TestConnectionMutation(t *testing.T) {
	s := NewStore()
	since := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.SetConnection(Connection{Phase: ConnectionConnecting, ReconnectArmed: true, Since: since})
	if got := s.Snapshot().Connection; got == nil || got.Phase != ConnectionConnecting || !got.ReconnectArmed || !got.Since.Equal(since) {
		t.Fatalf("connecting snapshot: %+v", got)
	}

	s.SetConnection(Connection{Phase: ConnectionReady, ReconnectArmed: true, Since: since.Add(time.Second)})
	if got := s.Snapshot().Connection; got == nil || got.Phase != ConnectionReady {
		t.Fatalf("ready snapshot: %+v", got)
	}
}

func TestCommandPendingToConfirmed(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	cmd := Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending,
		Requested: map[string]any{"on": true}, StartedAt: now, UpdatedAt: now}

	s.BeginCommand(cmd)
	pending, ok := s.Snapshot().PendingCommands[cmd.ID]
	if !ok || pending.Phase != CommandPending || !reflect.DeepEqual(pending.Requested, cmd.Requested) {
		t.Fatalf("pending command: %+v, present=%v", pending, ok)
	}

	now = now.Add(2 * time.Second)
	observed := map[string]any{"on": true, "watts": 12.5}
	s.FinishCommand(cmd.ID, CommandConfirmed, observed, nil)
	snap := s.Snapshot()
	if _, ok := snap.PendingCommands[cmd.ID]; ok {
		t.Fatalf("terminal command remained pending: %+v", snap.PendingCommands)
	}
	if len(snap.RecentCommands) != 1 {
		t.Fatalf("recent commands: %+v", snap.RecentCommands)
	}
	finished := snap.RecentCommands[0]
	if finished.ID != cmd.ID || finished.Phase != CommandConfirmed || !finished.UpdatedAt.Equal(now) || !reflect.DeepEqual(finished.Observed, observed) || finished.Error != nil {
		t.Fatalf("finished command: %+v", finished)
	}

	finished.Requested.(map[string]any)["on"] = false
	finished.Observed.(map[string]any)["on"] = false
	fresh := s.Snapshot().RecentCommands[0]
	if fresh.Requested.(map[string]any)["on"] != true || fresh.Observed.(map[string]any)["on"] != true {
		t.Fatalf("command payload aliases into store: %+v", fresh)
	}
}

func TestCommandTimeoutAndFailure(t *testing.T) {
	for _, tc := range []struct {
		phase string
		err   *CommandError
	}{
		{phase: CommandTimeout, err: &CommandError{Code: "command_timeout", Message: "telemetry did not converge"}},
		{phase: CommandFailed, err: &CommandError{Code: "ble_failure", Message: "write failed"}},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			s := NewStore()
			now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			s.BeginCommand(Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending, StartedAt: now, UpdatedAt: now})
			s.FinishCommand("cmd-1", tc.phase, nil, tc.err)
			got := s.Snapshot().RecentCommands[0]
			if got.Phase != tc.phase || got.Error == tc.err || !reflect.DeepEqual(got.Error, tc.err) {
				t.Fatalf("terminal command: %+v", got)
			}
		})
	}
}

func TestCommandTransitionsNotifySubscribers(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	s.SetIdentity(Identity{Model: "BP4SL3V2"})
	s.SetConnection(Connection{Phase: ConnectionConnecting})
	s.BeginCommand(Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending, StartedAt: now, UpdatedAt: now})
	s.FinishCommand("cmd-1", CommandConfirmed, map[string]any{"on": true}, nil)

	want := []func(Snapshot) bool{
		func(sn Snapshot) bool { return sn.Device != nil && sn.Device.Model == "BP4SL3V2" },
		func(sn Snapshot) bool { return sn.Connection != nil && sn.Connection.Phase == ConnectionConnecting },
		func(sn Snapshot) bool { _, ok := sn.PendingCommands["cmd-1"]; return ok },
		func(sn Snapshot) bool {
			return len(sn.RecentCommands) == 1 && sn.RecentCommands[0].Phase == CommandConfirmed
		},
	}
	for i, predicate := range want {
		select {
		case snap := <-ch:
			if !predicate(snap) {
				t.Fatalf("notification %d did not contain its transition: %+v", i, snap)
			}
		case <-time.After(time.Second):
			t.Fatalf("notification %d was not published", i)
		}
	}
}

func TestWaitPredicateAndContextCancellation(t *testing.T) {
	s := NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan Snapshot, 1)
	errCh := make(chan error, 1)
	go func() {
		snap, err := s.Wait(ctx, func(sn Snapshot) bool {
			return sn.Connection != nil && sn.Connection.Phase == ConnectionReady
		})
		done <- snap
		errCh <- err
	}()
	s.SetConnection(Connection{Phase: ConnectionReady})
	if err := <-errCh; err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if snap := <-done; snap.Connection == nil || snap.Connection.Phase != ConnectionReady {
		t.Fatalf("Wait snapshot: %+v", snap)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	_, err := s.Wait(cancelled, func(Snapshot) bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
}

func TestWaitPredicateRunsWithoutStoreLock(t *testing.T) {
	s := NewStore()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.Wait(ctx, func(Snapshot) bool {
		return s.Snapshot().UpdatedAt.IsZero()
	})
	if err != nil {
		t.Fatalf("reentrant predicate deadlocked: %v", err)
	}
}

func TestCommandSubscriberSnapshotIsIndependent(t *testing.T) {
	s := NewStore()
	ch, cancel := s.Subscribe()
	defer cancel()
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.BeginCommand(Command{
		ID:        "cmd-1",
		Operation: "dc",
		Phase:     CommandPending,
		Requested: map[string]any{"on": true},
		StartedAt: now,
		UpdatedAt: now,
	})
	notification := <-ch
	command := notification.PendingCommands["cmd-1"]
	command.Requested.(map[string]any)["on"] = false
	delete(notification.PendingCommands, "cmd-1")

	fresh := s.Snapshot()
	if fresh.PendingCommands["cmd-1"].Requested.(map[string]any)["on"] != true {
		t.Fatalf("subscriber snapshot aliases store state: %+v", fresh.PendingCommands)
	}
}

func TestCommandTypedPayloadIsCopied(t *testing.T) {
	type payload struct {
		Levels []int
		Flags  map[string]bool
	}
	s := NewStore()
	requested := &payload{Levels: []int{1, 2}, Flags: map[string]bool{"on": true}}
	s.BeginCommand(Command{ID: "cmd-1", Operation: "limit", Requested: requested})
	requested.Levels[0] = 9
	requested.Flags["on"] = false

	got := s.Snapshot().PendingCommands["cmd-1"].Requested.(*payload)
	if got.Levels[0] != 1 || !got.Flags["on"] {
		t.Fatalf("caller payload aliases store state: %+v", got)
	}
	got.Levels[0] = 8
	got.Flags["on"] = false
	fresh := s.Snapshot().PendingCommands["cmd-1"].Requested.(*payload)
	if fresh.Levels[0] != 1 || !fresh.Flags["on"] {
		t.Fatalf("snapshot payload aliases store state: %+v", fresh)
	}
}

func TestCommandConcurrentFinishTransitionsOnce(t *testing.T) {
	s := NewStore()
	s.BeginCommand(Command{ID: "cmd-1", Operation: "dc", Phase: CommandPending})
	start := make(chan struct{})
	done := make(chan struct{}, 128)
	for i := 0; i < cap(done); i++ {
		go func() {
			<-start
			s.FinishCommand("cmd-1", CommandConfirmed, nil, nil)
			done <- struct{}{}
		}()
	}
	close(start)
	for i := 0; i < cap(done); i++ {
		<-done
	}
	if recent := s.Snapshot().RecentCommands; len(recent) != 1 {
		t.Fatalf("one pending command produced %d terminal records", len(recent))
	}
}

func TestCommandEvictsOldestAfter32TerminalCommands(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < 35; i++ {
		id := fmt.Sprintf("cmd-%02d", i)
		s.BeginCommand(Command{ID: id, Operation: "dc", Phase: CommandPending, StartedAt: now, UpdatedAt: now})
		s.FinishCommand(id, CommandConfirmed, nil, nil)
	}
	recent := s.Snapshot().RecentCommands
	if len(recent) != 32 || recent[0].ID != "cmd-03" || recent[31].ID != "cmd-34" {
		t.Fatalf("recent ring = %d [%s..%s]", len(recent), recent[0].ID, recent[len(recent)-1].ID)
	}
}
