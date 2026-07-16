package state

import (
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
