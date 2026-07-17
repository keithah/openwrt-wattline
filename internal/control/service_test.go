package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type fakeSession struct {
	mu                                                 sync.Mutex
	dcErr, typeCErr, bypassErr                         error
	dcCalls, typeCCalls, bypassCalls                   []bool
	getLimit                                           int
	putLimit                                           int
	deleteLimit                                        int
	threshold                                          float64
	timers                                             []proto.Timer
	assigned                                           byte
	barrier                                            bool
	firmware                                           []byte
	clock                                              time.Time
	clockAvailable                                     bool
	ota                                                proto.OTAInfo
	err                                                error
	restartCalls, shutdownCalls, enterCalls, exitCalls int
}

func (f *fakeSession) DCControl(v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dcCalls = append(f.dcCalls, v)
	return f.dcErr
}
func (f *fakeSession) TypeCOutput(v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typeCCalls = append(f.typeCCalls, v)
	return f.typeCErr
}
func (f *fakeSession) BypassControl(v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bypassCalls = append(f.bypassCalls, v)
	return f.bypassErr
}
func (f *fakeSession) GetUSBCLimit(int) (int, error)      { return f.getLimit, f.err }
func (f *fakeSession) PutUSBCLimit(int, int) (int, error) { return f.putLimit, f.err }
func (f *fakeSession) DeleteUSBCLimit(int) (int, error)   { return f.deleteLimit, f.err }
func (f *fakeSession) BypassThreshold() (float64, error)  { return f.threshold, f.err }
func (f *fakeSession) SetBypassThreshold(float64) error   { return f.err }
func (f *fakeSession) ListTimers() ([]proto.Timer, error) {
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) AddTimer(proto.Timer) ([]proto.Timer, byte, error) {
	return append([]proto.Timer(nil), f.timers...), f.assigned, f.err
}
func (f *fakeSession) PutTimer(byte, proto.Timer) ([]proto.Timer, error) {
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) DeleteTimer(byte) ([]proto.Timer, error) {
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) BarrierFree() (bool, error)        { return f.barrier, f.err }
func (f *fakeSession) SetBarrierFree(bool) (bool, error) { return f.barrier, f.err }
func (f *fakeSession) SetRunningMode(byte) error         { return f.err }
func (f *fakeSession) USBFirmwareVersion() ([]byte, error) {
	return append([]byte(nil), f.firmware...), f.err
}
func (f *fakeSession) SetBLEPIN(uint32) error              { return f.err }
func (f *fakeSession) ReadClock() (time.Time, bool, error) { return f.clock, f.clockAvailable, f.err }
func (f *fakeSession) SyncClock(time.Time, byte) error     { return f.err }
func (f *fakeSession) OTAInfo() (proto.OTAInfo, error)     { return f.ota, f.err }
func (f *fakeSession) EnterOTA(context.Context) error      { f.enterCalls++; return f.err }
func (f *fakeSession) ExitOTA(context.Context) error       { f.exitCalls++; return f.err }
func (f *fakeSession) Restart() error                      { f.restartCalls++; return f.err }
func (f *fakeSession) Shutdown() error                     { f.shutdownCalls++; return f.err }

type fakeConnector struct{ arm, disarm, resume int }

func (f *fakeConnector) ArmReconnect(time.Duration) { f.arm++ }
func (f *fakeConnector) DisarmReconnect()           { f.disarm++ }
func (f *fakeConnector) ResumeReconnect()           { f.resume++ }

func fullyCapableStore() *state.Store {
	s := state.NewStore()
	s.SetConnected(true)
	s.SetIdentity(state.Identity{
		Mode:            "app",
		FeatureSet:      proto.FeatureSet{FactoryMode: true, Shutdown: true, DCOutControl: true, DCOutScheduler: true, USBPort: true, USBPowerLimit: true, USBOutputControl: true, DCBypass: true, DCBypassControl: true},
		Characteristics: map[string]bool{"command": true, "dc": true, "typec": true, "ota": true, "factory": true, "current_time": true},
	})
	return s
}

func testService(s *state.Store, session Session) *Service {
	return &Service{resolve: func() Session { return session }, store: s, advanced: func() bool { return true }, confirmTimeout: 60 * time.Millisecond, bypassTimeout: 60 * time.Millisecond, now: func() time.Time { return time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC) }, newID: func() string { return "cmd-test" }}
}

func terminal(t *testing.T, s *state.Store) state.Command {
	t.Helper()
	snap := s.Snapshot()
	if len(snap.PendingCommands) != 0 || len(snap.RecentCommands) != 1 {
		t.Fatalf("pending=%d recent=%d", len(snap.PendingCommands), len(snap.RecentCommands))
	}
	return snap.RecentCommands[0]
}

func TestSetDCConfirmsOnlyFromEnabled(t *testing.T) {
	st := fullyCapableStore()
	st.SetDC(proto.DCPort{Enabled: false})
	svc := testService(st, &fakeSession{})
	done := make(chan error, 1)
	go func() { _, err := svc.SetDC(context.Background(), true); done <- err }()
	waitPending(t, st)
	st.SetDC(proto.DCPort{Enabled: false, Bypass: true, Status: 1})
	select {
	case err := <-done:
		t.Fatalf("confirmed from unrelated DC fields: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	want := proto.DCPort{Enabled: true, Bypass: true, Status: -1}
	st.SetDC(want)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cmd := terminal(t, st)
	if cmd.Phase != state.CommandConfirmed || cmd.Observed != want {
		t.Fatalf("terminal=%+v", cmd)
	}
}

func TestSetTypeCOutputConfirmsOnlyFromMode(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		on                   bool
		initial, wrong, want proto.TypeCPort
	}{
		{"on", true, proto.TypeCPort{Mode: 1}, proto.TypeCPort{Mode: 2, Enabled: true}, proto.TypeCPort{Mode: 3, Enabled: false}},
		{"off", false, proto.TypeCPort{Mode: 3}, proto.TypeCPort{Mode: 2, Enabled: false}, proto.TypeCPort{Mode: 1, Enabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := fullyCapableStore()
			st.SetTypeC(tc.initial)
			svc := testService(st, &fakeSession{})
			done := make(chan error, 1)
			go func() { _, err := svc.SetTypeCOutput(context.Background(), tc.on); done <- err }()
			waitPending(t, st)
			st.SetTypeC(tc.wrong)
			select {
			case err := <-done:
				t.Fatalf("confirmed from Enabled/wrong mode: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			st.SetTypeC(tc.want)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			cmd := terminal(t, st)
			if cmd.Phase != state.CommandConfirmed || cmd.Observed != tc.want {
				t.Fatalf("terminal=%+v", cmd)
			}
		})
	}
}

func TestSetBypassConfirmsFromBypassNotCommandCompletion(t *testing.T) {
	st := fullyCapableStore()
	st.SetDC(proto.DCPort{Bypass: false})
	sess := &fakeSession{}
	svc := testService(st, sess)
	done := make(chan error, 1)
	go func() { _, err := svc.SetBypass(context.Background(), true); done <- err }()
	waitPending(t, st)
	st.SetDC(proto.DCPort{Bypass: false, Enabled: true})
	select {
	case err := <-done:
		t.Fatalf("confirmed from Enabled: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	want := proto.DCPort{Bypass: true, Enabled: false}
	st.SetDC(want)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if cmd := terminal(t, st); cmd.Phase != state.CommandConfirmed || cmd.Observed != want {
		t.Fatalf("terminal=%+v", cmd)
	}
}

func TestReconciledTimeoutAndCancellationAreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ctx   func() context.Context
		want  error
		phase string
	}{
		{"timeout", context.Background, ErrTimeout, state.CommandTimeout},
		{"cancel", func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }, context.Canceled, state.CommandFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := fullyCapableStore()
			st.SetDC(proto.DCPort{})
			svc := testService(st, &fakeSession{})
			_, err := svc.SetDC(tc.ctx(), true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			cmd := terminal(t, st)
			if cmd.Phase != tc.phase || cmd.Observed != (proto.DCPort{}) {
				t.Fatalf("terminal=%+v", cmd)
			}
		})
	}
}

func TestReconciledFailureAndDisconnectedAreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session Session
		want    error
	}{
		{"write", &fakeSession{dcErr: errors.New("write failed")}, errors.New("write failed")},
		{"disconnected", nil, ErrDisconnected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := fullyCapableStore()
			st.SetDC(proto.DCPort{})
			svc := testService(st, tc.session)
			_, err := svc.SetDC(context.Background(), true)
			if tc.name == "write" {
				if err == nil || err.Error() != tc.want.Error() {
					t.Fatalf("err=%v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v", err)
			}
			if cmd := terminal(t, st); cmd.Phase != state.CommandFailed {
				t.Fatalf("terminal=%+v", cmd)
			}
		})
	}
}

func waitPending(t *testing.T, st *state.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := st.Wait(ctx, func(s state.Snapshot) bool { return len(s.PendingCommands) == 1 })
	if err != nil {
		t.Fatal(err)
	}
}

func TestAdvancedSupportPrecedesPolicy(t *testing.T) {
	st := fullyCapableStore()
	sess := &fakeSession{barrier: true}
	svc := testService(st, sess)
	svc.advanced = func() bool { return false }
	st.SetIdentity(state.Identity{Mode: "app", Characteristics: map[string]bool{}})
	if _, err := svc.BarrierFree(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported err=%v", err)
	}
	st = fullyCapableStore()
	svc = testService(st, sess)
	svc.advanced = func() bool { return false }
	if _, err := svc.BarrierFree(context.Background()); !errors.Is(err, ErrAdvancedDisabled) {
		t.Fatalf("policy err=%v", err)
	}
}

func TestLifecycleDelegatesWithoutDuplicatingConnectorPolicy(t *testing.T) {
	st := fullyCapableStore()
	sess := &fakeSession{}
	connector := &fakeConnector{}
	svc := testService(st, sess)
	svc.connector = connector
	if err := svc.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnterOTA(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.SetIdentity(state.Identity{Mode: "ota", Characteristics: map[string]bool{"ota": true}})
	if err := svc.ExitOTA(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sess.restartCalls != 1 || sess.shutdownCalls != 1 || sess.enterCalls != 1 || sess.exitCalls != 1 {
		t.Fatalf("session lifecycle calls: %+v", sess)
	}
	if connector.arm != 0 || connector.disarm != 0 || connector.resume != 0 {
		t.Fatalf("duplicated connector policy: %+v", connector)
	}
}
