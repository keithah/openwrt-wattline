package state

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

const (
	ConnectionDisconnected = "disconnected"
	ConnectionConnecting   = "connecting"
	ConnectionHandshaking  = "handshaking"
	ConnectionReady        = "ready"
	ConnectionBootloader   = "bootloader"

	CommandPending   = "pending"
	CommandConfirmed = "confirmed"
	CommandTimeout   = "timeout"
	CommandFailed    = "failed"
)

const recentCommandCap = 32

type Identity struct {
	Model              string           `json:"model,omitempty"`
	HWRev              string           `json:"hw_rev,omitempty"`
	AppFirmware        string           `json:"app_firmware,omitempty"`
	BootloaderFirmware string           `json:"bootloader_firmware,omitempty"`
	MAC                string           `json:"mac,omitempty"`
	CID                uint16           `json:"cid,omitempty"`
	Features           uint32           `json:"features,omitempty"`
	FeatureSet         proto.FeatureSet `json:"feature_set"`
	Mode               string           `json:"mode,omitempty"`
	Characteristics    map[string]bool  `json:"characteristics,omitempty"`
}

type Connection struct {
	Phase          string    `json:"phase"`
	ReconnectArmed bool      `json:"reconnect_armed"`
	Since          time.Time `json:"since"`
	Error          string    `json:"error,omitempty"`
}

type CommandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Command struct {
	ID        string        `json:"id"`
	Operation string        `json:"operation"`
	Phase     string        `json:"phase"`
	Requested any           `json:"requested,omitempty"`
	Observed  any           `json:"observed,omitempty"`
	StartedAt time.Time     `json:"started_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Error     *CommandError `json:"error"`
}

func (s *Store) SetIdentity(identity Identity) {
	identity = cloneIdentity(identity)
	s.mutateState(func(sn *Snapshot) { sn.Device = &identity })
}

func (s *Store) SetConnection(connection Connection) {
	s.mutateState(func(sn *Snapshot) { sn.Connection = &connection })
}

func (s *Store) BeginCommand(command Command) {
	now := s.now()
	command = cloneCommand(command)
	command.Phase = CommandPending
	if command.StartedAt.IsZero() {
		command.StartedAt = now
	}
	if command.UpdatedAt.IsZero() {
		command.UpdatedAt = now
	}
	s.mutateState(func(sn *Snapshot) {
		if sn.PendingCommands == nil {
			sn.PendingCommands = make(map[string]Command)
		}
		sn.PendingCommands[command.ID] = command
	})
}

func (s *Store) FinishCommand(id, phase string, observed any, commandErr *CommandError) (Command, bool) {
	if !isTerminalCommandPhase(phase) {
		return Command{}, false
	}
	observed = cloneValue(observed)
	commandErr = cloneCommandError(commandErr)
	s.mu.Lock()
	command, ok := s.snap.PendingCommands[id]
	if !ok {
		s.mu.Unlock()
		return Command{}, false
	}
	command.Phase = phase
	command.Observed = observed
	command.UpdatedAt = s.now()
	command.Error = commandErr
	delete(s.snap.PendingCommands, id)
	s.snap.RecentCommands = append(s.snap.RecentCommands, command)
	if len(s.snap.RecentCommands) > recentCommandCap {
		s.snap.RecentCommands = append([]Command(nil), s.snap.RecentCommands[len(s.snap.RecentCommands)-recentCommandCap:]...)
	}
	s.publishLocked(false)
	s.mu.Unlock()
	return cloneCommand(command), true
}

func isTerminalCommandPhase(phase string) bool {
	return phase == CommandConfirmed || phase == CommandTimeout || phase == CommandFailed
}

type snapshotWaiter struct {
	mu    sync.Mutex
	queue []Snapshot
	ready chan struct{}
}

func newSnapshotWaiter() *snapshotWaiter {
	return &snapshotWaiter{ready: make(chan struct{}, 1)}
}

func (w *snapshotWaiter) enqueue(snap Snapshot) {
	w.mu.Lock()
	w.queue = append(w.queue, snap)
	select {
	case w.ready <- struct{}{}:
	default:
	}
	w.mu.Unlock()
}

func (w *snapshotWaiter) next(ctx context.Context) (Snapshot, error) {
	for {
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-w.ready:
		}

		w.mu.Lock()
		if len(w.queue) == 0 {
			w.mu.Unlock()
			continue
		}
		snap := w.queue[0]
		w.queue[0] = Snapshot{}
		w.queue = w.queue[1:]
		if len(w.queue) > 0 {
			select {
			case w.ready <- struct{}{}:
			default:
			}
		}
		w.mu.Unlock()
		return snap, nil
	}
}

// Wait returns the first snapshot satisfying predicate, or the context error.
// Registering the waiter and taking its initial snapshot happen under one lock,
// so a mutation cannot be lost between those two operations.
func (s *Store) Wait(ctx context.Context, predicate func(Snapshot) bool) (Snapshot, error) {
	waiter := newSnapshotWaiter()
	s.mu.Lock()
	s.waiters[waiter] = struct{}{}
	snap := cloneSnapshot(s.snap)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, waiter)
		s.mu.Unlock()
	}()

	for {
		if predicate(snap) {
			return snap, nil
		}
		var err error
		snap, err = waiter.next(ctx)
		if err != nil {
			return Snapshot{}, err
		}
	}
}

func cloneIdentity(in Identity) Identity {
	out := in
	if in.Characteristics != nil {
		out.Characteristics = make(map[string]bool, len(in.Characteristics))
		for key, value := range in.Characteristics {
			out.Characteristics[key] = value
		}
	}
	return out
}

func cloneCommandError(in *CommandError) *CommandError {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneCommand(in Command) Command {
	out := in
	out.Requested = cloneValue(in.Requested)
	out.Observed = cloneValue(in.Observed)
	out.Error = cloneCommandError(in.Error)
	return out
}

func cloneValue(in any) any {
	if in == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(in), make(map[cloneVisit]reflect.Value)).Interface()
}

type cloneVisit struct {
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

// cloneReflect preserves concrete command-payload types while recursively
// copying exported state. Command payloads must remain JSON-encodable; channels,
// functions, and unexported struct fields are therefore treated as opaque.
func cloneReflect(in reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	if !in.IsValid() {
		return in
	}
	switch in.Kind() {
	case reflect.Interface:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		out := reflect.New(in.Type()).Elem()
		out.Set(cloneReflect(in.Elem(), seen))
		return out
	case reflect.Pointer:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		visit := cloneVisit{typeOf: in.Type(), pointer: in.Pointer()}
		if out, ok := seen[visit]; ok {
			return out
		}
		out := reflect.New(in.Type().Elem())
		seen[visit] = out
		out.Elem().Set(cloneReflect(in.Elem(), seen))
		return out
	case reflect.Map:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		out := reflect.MakeMapWithSize(in.Type(), in.Len())
		iter := in.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneReflect(iter.Value(), seen))
		}
		return out
	case reflect.Slice:
		if in.IsNil() {
			return reflect.Zero(in.Type())
		}
		visit := cloneVisit{typeOf: in.Type(), pointer: in.Pointer(), length: in.Len()}
		if out, ok := seen[visit]; ok {
			return out
		}
		out := reflect.MakeSlice(in.Type(), in.Len(), in.Len())
		seen[visit] = out
		for i := 0; i < in.Len(); i++ {
			out.Index(i).Set(cloneReflect(in.Index(i), seen))
		}
		return out
	case reflect.Array:
		out := reflect.New(in.Type()).Elem()
		for i := 0; i < in.Len(); i++ {
			out.Index(i).Set(cloneReflect(in.Index(i), seen))
		}
		return out
	case reflect.Struct:
		out := reflect.New(in.Type()).Elem()
		out.Set(in)
		for i := 0; i < in.NumField(); i++ {
			if in.Type().Field(i).PkgPath == "" {
				out.Field(i).Set(cloneReflect(in.Field(i), seen))
			}
		}
		return out
	default:
		return in
	}
}
