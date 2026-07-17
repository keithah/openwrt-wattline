package control

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func TestLimitsReturnAtomicSessionReadback(t *testing.T) {
	sess := &fakeSession{getLimit: -1, putLimit: 4, deleteLimit: 0}
	svc := testService(fullyCapableStore(), sess)
	if got, err := svc.GetUSBCLimit(context.Background(), proto.LimitRuntime); err != nil || got != -1 {
		t.Fatalf("get=(%d,%v)", got, err)
	}
	if got, err := svc.PutUSBCLimit(context.Background(), proto.LimitOutput, 3); err != nil || got != 4 {
		t.Fatalf("put=(%d,%v)", got, err)
	}
	if got, err := svc.DeleteUSBCLimit(context.Background(), proto.LimitOutput); err != nil || got != 0 {
		t.Fatalf("delete=(%d,%v)", got, err)
	}
}

func TestPutUSBCLimitRejectsInvalidIntTypeBeforeResolution(t *testing.T) {
	for _, typ := range []int{257, -255, 0, proto.LimitRuntime, 5, -1} {
		t.Run(fmt.Sprintf("type_%d", typ), func(t *testing.T) {
			sess := &fakeSession{}
			resolveCalls := 0
			svc := testService(fullyCapableStore(), sess)
			svc.resolve = func() Session { resolveCalls++; return sess }
			if _, err := svc.PutUSBCLimit(context.Background(), typ, 3); err == nil {
				t.Fatalf("type %d accepted", typ)
			}
			if resolveCalls != 0 {
				t.Fatalf("type %d resolved session %d times", typ, resolveCalls)
			}
		})
	}
}

func TestTimerMutationsReturnAuthoritativeRelist(t *testing.T) {
	want := []proto.Timer{{ID: 7, Status: 1, Type: proto.TimerDaily, Hour: 6, Action: 1}}
	sess := &fakeSession{timers: want, assigned: 7}
	svc := testService(fullyCapableStore(), sess)
	timer := proto.Timer{Status: 1, Type: proto.TimerDaily, Hour: 6, Action: 1}
	got, id, err := svc.AddTimer(context.Background(), timer)
	if err != nil || id != 7 || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("add=(%+v,%d,%v)", got, id, err)
	}
	got, err = svc.PutTimer(context.Background(), 7, timer)
	if err != nil || got[0] != want[0] {
		t.Fatalf("put=(%+v,%v)", got, err)
	}
	got, err = svc.DeleteTimer(context.Background(), 7)
	if err != nil || got[0] != want[0] {
		t.Fatalf("delete=(%+v,%v)", got, err)
	}
}

func TestGetTimerDelegatesDirectlyAndMapsOnlyTimerNotFound(t *testing.T) {
	want := proto.Timer{ID: 7, Status: 1, Type: proto.TimerDaily}
	sess := &fakeSession{timers: []proto.Timer{want}}
	svc := testService(fullyCapableStore(), sess)
	got, err := svc.GetTimer(context.Background(), 7)
	if err != nil || got != want || sess.getTimerCalls != 1 || sess.listTimerCalls != 0 {
		t.Fatalf("got=%+v err=%v get=%d list=%d", got, err, sess.getTimerCalls, sess.listTimerCalls)
	}

	sess.err = proto.ErrTimerNotFound
	if _, err := svc.GetTimer(context.Background(), 8); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrBLE) {
		t.Fatalf("not-found err=%v", err)
	}
	sess.err = errors.New("transport")
	if _, err := svc.GetTimer(context.Background(), 8); !errors.Is(err, ErrBLE) || errors.Is(err, ErrNotFound) {
		t.Fatalf("transport err=%v", err)
	}
}

func TestTimerMutationMapsAtomicNotFound(t *testing.T) {
	timer := proto.Timer{Status: 1, Type: proto.TimerDaily}
	for _, call := range []func(*Service) error{
		func(s *Service) error { _, err := s.PutTimer(context.Background(), 7, timer); return err },
		func(s *Service) error { _, err := s.DeleteTimer(context.Background(), 7); return err },
	} {
		sess := &fakeSession{err: proto.ErrTimerNotFound}
		svc := testService(fullyCapableStore(), sess)
		if err := call(svc); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrBLE) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestTimerValidationHappensBeforeBLE(t *testing.T) {
	sess := &fakeSession{}
	svc := testService(fullyCapableStore(), sess)
	_, _, err := svc.AddTimer(context.Background(), proto.Timer{Status: -2, Type: proto.TimerDaily})
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("validation err=%v", err)
	}
}

func TestTimerIDFFRejectedBeforeResolution(t *testing.T) {
	valid := proto.Timer{Status: 1, Type: proto.TimerDaily}
	for _, tc := range []struct {
		name string
		call func(*Service) error
	}{
		{"get", func(s *Service) error { _, err := s.GetTimer(context.Background(), 0xff); return err }},
		{"put", func(s *Service) error { _, err := s.PutTimer(context.Background(), 0xff, valid); return err }},
		{"delete", func(s *Service) error { _, err := s.DeleteTimer(context.Background(), 0xff); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{}
			resolveCalls := 0
			svc := testService(fullyCapableStore(), sess)
			svc.resolve = func() Session { resolveCalls++; return sess }
			if err := tc.call(svc); err == nil {
				t.Fatal("ID 0xff accepted")
			}
			if resolveCalls != 0 || sess.listTimerCalls != 0 || sess.putTimerCalls != 0 || sess.deleteTimerCalls != 0 {
				t.Fatalf("BLE touched: resolve=%d list=%d put=%d delete=%d", resolveCalls, sess.listTimerCalls, sess.putTimerCalls, sess.deleteTimerCalls)
			}
		})
	}
}

func TestOrdinaryCapabilitiesAreDistinctFromDisconnect(t *testing.T) {
	st := fullyCapableStore()
	st.SetIdentity(stateIdentityWithoutFeatures())
	svc := testService(st, &fakeSession{})
	if _, err := svc.GetUSBCLimit(context.Background(), proto.LimitOutput); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err=%v", err)
	}
	svc.resolve = func() Session { return nil }
	st = fullyCapableStore()
	svc.store = st
	if _, err := svc.GetUSBCLimit(context.Background(), proto.LimitOutput); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("err=%v", err)
	}
}

func stateIdentityWithoutFeatures() state.Identity {
	return state.Identity{Mode: "app", Characteristics: map[string]bool{"command": true}}
}
