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
	mu                                                      sync.Mutex
	dcErr, typeCErr, bypassErr                              error
	dcCalls, typeCCalls, bypassCalls                        []bool
	getLimit                                                int
	putLimit                                                int
	deleteLimit                                             int
	threshold                                               float64
	getThresholdCalls, setThresholdCalls, putThresholdCalls int
	timers                                                  []proto.Timer
	listTimerCalls, putTimerCalls, deleteTimerCalls         int
	assigned                                                byte
	barrier                                                 bool
	firmware                                                []byte
	clock                                                   time.Time
	clockAvailable                                          bool
	readClockCalls, runningModeCalls                        int
	ota                                                     proto.OTAInfo
	err                                                     error
	restartCalls, shutdownCalls, enterCalls, exitCalls      int
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
func (f *fakeSession) BypassThreshold() (float64, error) {
	f.getThresholdCalls++
	return f.threshold, f.err
}
func (f *fakeSession) SetBypassThreshold(float64) error { f.setThresholdCalls++; return f.err }
func (f *fakeSession) PutBypassThreshold(float64) (float64, error) {
	f.putThresholdCalls++
	return f.threshold, f.err
}
func (f *fakeSession) ListTimers() ([]proto.Timer, error) {
	f.listTimerCalls++
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) AddTimer(proto.Timer) ([]proto.Timer, byte, error) {
	return append([]proto.Timer(nil), f.timers...), f.assigned, f.err
}
func (f *fakeSession) PutTimer(byte, proto.Timer) ([]proto.Timer, error) {
	f.putTimerCalls++
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) DeleteTimer(byte) ([]proto.Timer, error) {
	f.deleteTimerCalls++
	return append([]proto.Timer(nil), f.timers...), f.err
}
func (f *fakeSession) BarrierFree() (bool, error)        { return f.barrier, f.err }
func (f *fakeSession) SetBarrierFree(bool) (bool, error) { return f.barrier, f.err }
func (f *fakeSession) SetRunningMode(byte) error         { f.runningModeCalls++; return f.err }
func (f *fakeSession) USBFirmwareVersion() ([]byte, error) {
	return append([]byte(nil), f.firmware...), f.err
}
func (f *fakeSession) SetBLEPIN(uint32) error { return f.err }
func (f *fakeSession) ReadClock() (time.Time, bool, error) {
	f.readClockCalls++
	return f.clock, f.clockAvailable, f.err
}
func (f *fakeSession) SyncClock(time.Time, byte) error { return f.err }
func (f *fakeSession) OTAInfo() (proto.OTAInfo, error) { return f.ota, f.err }
func (f *fakeSession) EnterOTA(context.Context) error  { f.enterCalls++; return f.err }
func (f *fakeSession) ExitOTA(context.Context) error   { f.exitCalls++; return f.err }
func (f *fakeSession) Restart() error                  { f.restartCalls++; return f.err }
func (f *fakeSession) Shutdown() error                 { f.shutdownCalls++; return f.err }

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
	return &Service{resolve: func() Session { return session }, store: s, advanced: func() bool { return true }, confirmTimeout: 60 * time.Millisecond, bypassTimeout: 60 * time.Millisecond, now: func() time.Time { return time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC) }, newID: func() (string, error) { return "cmd-test", nil }}
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
	st := fullyCapableStore()
	st.SetDC(proto.DCPort{})
	svc := testService(st, &fakeSession{})
	_, err := svc.SetDC(context.Background(), true)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout err=%v", err)
	}
	cmd := terminal(t, st)
	if cmd.Phase != state.CommandTimeout || cmd.Observed != (proto.DCPort{}) {
		t.Fatalf("timeout terminal=%+v", cmd)
	}

	st = fullyCapableStore()
	st.SetDC(proto.DCPort{})
	svc = testService(st, &fakeSession{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.SetDC(ctx, true); done <- err }()
	waitPending(t, st)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	cmd = terminal(t, st)
	if cmd.Phase != state.CommandFailed || cmd.Observed != (proto.DCPort{}) {
		t.Fatalf("cancel terminal=%+v", cmd)
	}
}

func TestPreCanceledCommandStopsBeforeResolutionAndID(t *testing.T) {
	st := fullyCapableStore()
	sess := &fakeSession{}
	svc := testService(st, sess)
	resolveCalls, idCalls := 0, 0
	svc.resolve = func() Session { resolveCalls++; return sess }
	svc.newID = func() (string, error) { idCalls++; return "cmd-test", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.SetDC(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	snap := st.Snapshot()
	if resolveCalls != 0 || idCalls != 0 || len(snap.PendingCommands) != 0 || len(snap.RecentCommands) != 0 {
		t.Fatalf("resolve=%d id=%d pending=%d recent=%d", resolveCalls, idCalls, len(snap.PendingCommands), len(snap.RecentCommands))
	}
}

func TestReconciledResolvesBeforeIDAndCapability(t *testing.T) {
	st := fullyCapableStore()
	st.SetIdentity(stateIdentityWithoutFeatures())
	sess := &fakeSession{}
	svc := testService(st, sess)
	resolved := false
	svc.resolve = func() Session { resolved = true; return sess }
	svc.newID = func() (string, error) {
		if !resolved {
			return "", errors.New("ID generated before session resolution")
		}
		return "cmd-test", nil
	}
	if _, err := svc.SetDC(context.Background(), true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err=%v", err)
	}
	if cmd := terminal(t, st); cmd.Error == nil || cmd.Error.Code != "capability_unsupported" {
		t.Fatalf("terminal=%+v", cmd)
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

func TestReadClockDelegatesWhenCharacteristicIsAbsent(t *testing.T) {
	st := fullyCapableStore()
	id := *st.Snapshot().Device
	id.Characteristics["current_time"] = false
	st.SetIdentity(id)
	sess := &fakeSession{clockAvailable: false}
	svc := testService(st, sess)
	got, available, err := svc.ReadClock(context.Background())
	if err != nil || available || !got.IsZero() {
		t.Fatalf("ReadClock=(%v,%v,%v)", got, available, err)
	}
	if sess.readClockCalls != 1 {
		t.Fatalf("ReadClock calls=%d want 1", sess.readClockCalls)
	}
}

func TestDisconnectedPrecedesUnsupported(t *testing.T) {
	st := fullyCapableStore()
	st.SetIdentity(stateIdentityWithoutFeatures())
	svc := testService(st, nil)
	if _, err := svc.GetUSBCLimit(context.Background(), proto.LimitOutput); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("pass-through err=%v", err)
	}
	svc.advanced = func() bool { return false }
	if _, err := svc.BarrierFree(context.Background()); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("advanced err=%v", err)
	}
	st.SetDC(proto.DCPort{})
	if _, err := svc.SetDC(context.Background(), true); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("reconciled err=%v", err)
	}
	if cmd := terminal(t, st); cmd.Phase != state.CommandFailed || cmd.Error == nil || cmd.Error.Code != "device_disconnected" {
		t.Fatalf("terminal=%+v", cmd)
	}
}

func TestPutBypassThresholdUsesAtomicSessionReadback(t *testing.T) {
	sess := &fakeSession{threshold: 19.75}
	svc := testService(fullyCapableStore(), sess)
	got, err := svc.PutBypassThreshold(context.Background(), 19.6)
	if err != nil || got != 19.75 {
		t.Fatalf("PutBypassThreshold=(%v,%v)", got, err)
	}
	if sess.putThresholdCalls != 1 || sess.setThresholdCalls != 0 || sess.getThresholdCalls != 0 {
		t.Fatalf("threshold calls put=%d set=%d get=%d", sess.putThresholdCalls, sess.setThresholdCalls, sess.getThresholdCalls)
	}
}

func TestSetRunningModeRejectsInvalidBeforeResolution(t *testing.T) {
	for _, mode := range []byte{2, 0xff} {
		st := fullyCapableStore()
		sess := &fakeSession{}
		resolveCalls := 0
		svc := testService(st, sess)
		svc.resolve = func() Session { resolveCalls++; return sess }
		if err := svc.SetRunningMode(context.Background(), mode); err == nil {
			t.Fatalf("mode %d accepted", mode)
		}
		if resolveCalls != 0 || sess.runningModeCalls != 0 {
			t.Fatalf("mode %d I/O: resolve=%d calls=%d", mode, resolveCalls, sess.runningModeCalls)
		}
	}
}

func TestCommandIDEntropyFailureCreatesNoCommand(t *testing.T) {
	st := fullyCapableStore()
	st.SetDC(proto.DCPort{})
	sess := &fakeSession{}
	svc := testService(st, sess)
	want := errors.New("entropy unavailable")
	svc.newID = func() (string, error) { return "", want }
	if _, err := svc.SetDC(context.Background(), true); !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
	snap := st.Snapshot()
	if len(snap.PendingCommands) != 0 || len(snap.RecentCommands) != 0 {
		t.Fatalf("command state pending=%v recent=%v", snap.PendingCommands, snap.RecentCommands)
	}
	if len(sess.dcCalls) != 0 {
		t.Fatalf("DC calls=%v", sess.dcCalls)
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
