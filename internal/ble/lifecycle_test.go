package ble

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type lifecyclePolicyCall struct {
	name  string
	delay time.Duration
}

type fakeLifecyclePolicy struct{ calls []lifecyclePolicyCall }

func (p *fakeLifecyclePolicy) ArmReconnect(delay time.Duration) {
	p.calls = append(p.calls, lifecyclePolicyCall{name: "arm", delay: delay})
}
func (p *fakeLifecyclePolicy) DisarmReconnect() {
	p.calls = append(p.calls, lifecyclePolicyCall{name: "disarm"})
}
func (p *fakeLifecyclePolicy) ResumeReconnect() {
	p.calls = append(p.calls, lifecyclePolicyCall{name: "resume"})
}

func TestDisconnectDefaultGraceExceedsDocumentedOTAExit(t *testing.T) {
	s, _ := newCtlSession()
	if s.disconnectGrace <= 2*time.Second {
		t.Fatalf("default disconnect grace = %v, want > 2s", s.disconnectGrace)
	}
}

func TestClockAbsentDoesZeroIO(t *testing.T) {
	s, f := newCtlSession()
	if got, available, err := s.ReadClock(); err != nil || available || !got.IsZero() {
		t.Fatalf("ReadClock = %v, %v, %v", got, available, err)
	}
	if err := s.SyncClock(time.Now(), 0); err == nil {
		t.Fatal("SyncClock on absent characteristic succeeded")
	}
	if len(f.writes) != 0 || f.reads != 0 {
		t.Fatalf("absent clock performed I/O: writes=%d reads=%d", len(f.writes), f.reads)
	}
}

func TestWriteOnlyClockReadDoesZeroEndpointIOButSyncRemainsAvailable(t *testing.T) {
	s, f := newCtlSession()
	f.writeOnly(CharTime)

	if got, available, err := s.ReadClock(); err != nil || available || !got.IsZero() {
		t.Fatalf("ReadClock = %v, %v, %v", got, available, err)
	}
	if f.readCalls[CharTime] != 0 {
		t.Fatalf("write-only clock endpoint performed %d GATT reads", f.readCalls[CharTime])
	}
	if err := s.SyncClock(time.Date(2026, 7, 18, 10, 11, 12, 0, time.Local), 0); err != nil {
		t.Fatalf("SyncClock on present write-only characteristic: %v", err)
	}
	if f.writeCalls[CharTime] != 1 {
		t.Fatalf("SyncClock writes = %d, want 1", f.writeCalls[CharTime])
	}
}

func TestClockReadAndCallerSuppliedReason(t *testing.T) {
	s, f := newCtlSession()
	f.available(CharTime)
	f.push(CharTime, "ea0707120a0b0c040000")
	got, available, err := s.ReadClock()
	if err != nil || !available || !got.Equal(time.Date(2026, 7, 18, 10, 11, 12, 0, time.Local)) {
		t.Fatalf("ReadClock = %v, %v, %v", got, available, err)
	}
	ts := time.Date(2026, 7, 18, 10, 11, 12, 0, time.Local)
	if err := s.SyncClock(ts, 7); err != nil {
		t.Fatal(err)
	}
	if got := f.writes[len(f.writes)-1][1]; got != "ea0707120a0b0c060007" {
		t.Fatalf("clock frame = %s", got)
	}
}

func TestOTAInfoEnterAndExit(t *testing.T) {
	s, f := newCtlSession()
	p := &fakeLifecyclePolicy{}
	s.lifecycle = p
	s.disconnectGrace = 50 * time.Millisecond
	f.push(CharOTA, "010000000000000000000000000503")
	info, err := s.OTAInfo()
	if err != nil || info.Mode != 1 || info.CID != 0x0305 {
		t.Fatalf("OTAInfo = %+v, %v", info, err)
	}
	go func() { time.Sleep(time.Millisecond); f.Close() }()
	if err := s.EnterOTA(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := writeFrames(f); !reflect.DeepEqual(got, []string{"84", "504b"}) {
		t.Fatalf("OTA frames = %v", got)
	}
	if len(p.calls) != 1 || p.calls[0].name != "arm" || p.calls[0].delay != 0 {
		t.Fatalf("OTA enter policy = %+v", p.calls)
	}

	s2, f2 := newCtlSession()
	p2 := &fakeLifecyclePolicy{}
	s2.lifecycle, s2.disconnectGrace = p2, 50*time.Millisecond
	go func() { time.Sleep(time.Millisecond); f2.Close() }()
	if err := s2.ExitOTA(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := writeFrames(f2); !reflect.DeepEqual(got, []string{"83"}) {
		t.Fatalf("OTA exit frames = %v", got)
	}
}

func TestDisconnectAsSuccessRequiresObservedDisconnect(t *testing.T) {
	tests := []struct {
		name          string
		writeError    error
		disconnect    bool
		preDisconnect bool
		wantError     bool
	}{
		{name: "clean write and disconnect", disconnect: true},
		{name: "write error and disconnect", writeError: errors.New("write failed"), disconnect: true},
		{name: "clean write without disconnect", wantError: true},
		{name: "write error without disconnect", writeError: errors.New("write failed"), wantError: true},
		{name: "already disconnected", preDisconnect: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, f := newCtlSession()
			p := &fakeLifecyclePolicy{}
			s.lifecycle, s.disconnectGrace = p, 10*time.Millisecond
			f.failWrites[CharCmd] = tt.writeError
			if tt.preDisconnect {
				f.Close()
			}
			if tt.disconnect {
				go func() { time.Sleep(time.Millisecond); f.Close() }()
			}
			err := s.Restart()
			if (err != nil) != tt.wantError {
				t.Fatalf("Restart error = %v, wantError %v", err, tt.wantError)
			}
			if p.calls[0] != (lifecyclePolicyCall{name: "arm", delay: 15 * time.Second}) {
				t.Fatalf("restart policy = %+v", p.calls)
			}
			if tt.wantError && (len(p.calls) != 2 || p.calls[1].name != "resume") {
				t.Fatalf("failed restart policy = %+v", p.calls)
			}
		})
	}
}

func TestDisconnectLifecyclePolicies(t *testing.T) {
	s, f := newCtlSession()
	p := &fakeLifecyclePolicy{}
	s.lifecycle, s.disconnectGrace = p, 50*time.Millisecond
	go func() { time.Sleep(time.Millisecond); f.Close() }()
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if got := writeFrames(f); !reflect.DeepEqual(got, []string{"464d"}) {
		t.Fatalf("shutdown frames = %v", got)
	}
	if !reflect.DeepEqual(p.calls, []lifecyclePolicyCall{{name: "disarm"}}) {
		t.Fatalf("shutdown policy = %+v", p.calls)
	}
}

func TestOTAContextCancellationIsNotDisconnectSuccess(t *testing.T) {
	s, _ := newCtlSession()
	p := &fakeLifecyclePolicy{}
	s.lifecycle, s.disconnectGrace = p, time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.EnterOTA(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnterOTA error = %v", err)
	}
}
