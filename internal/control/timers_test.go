package control

import (
	"context"
	"errors"
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

func TestTimerValidationHappensBeforeBLE(t *testing.T) {
	sess := &fakeSession{}
	svc := testService(fullyCapableStore(), sess)
	_, _, err := svc.AddTimer(context.Background(), proto.Timer{Status: -2, Type: proto.TimerDaily})
	if err == nil || errors.Is(err, ErrUnsupported) {
		t.Fatalf("validation err=%v", err)
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
