package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/rules"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type countDev struct{ dc int32 }

func (c *countDev) DCControl(bool) error     { atomic.AddInt32(&c.dc, 1); return nil }
func (c *countDev) TypeCOutput(bool) error   { return nil }
func (c *countDev) BypassControl(bool) error { return nil }
func (c *countDev) Restart() error           { return nil }
func (c *countDev) Shutdown() error          { return nil }

func protoBatteryLevel(l uint8) proto.Battery { return proto.Battery{Level: l} }

func TestTickDispatchesFirings(t *testing.T) {
	store := state.NewStore()
	store.SetConnected(true)
	r := config.Rule{Name: "r", Enabled: true, Condition: "battery_level",
		Op: "below", Percent: 15, HysteresisMargin: 5, Actions: []string{"dc_off"}}
	eng, _ := rules.NewEngine([]config.Rule{r})
	dev := &countDev{}
	exec := actions.NewExecutor(dev, "d")
	// One tick with battery below threshold must fire dc_off once.
	tickOnce(eng, store, func() actions.Device { return dev }, exec, time.Now())
	// helper needs battery set:
	if atomic.LoadInt32(&dev.dc) != 0 {
		t.Fatal("fired without battery data")
	}
	store.SetBattery(protoBatteryLevel(10))
	tickOnce(eng, store, func() actions.Device { return dev }, exec, time.Now())
	if atomic.LoadInt32(&dev.dc) != 1 {
		t.Fatalf("expected 1 dc call, got %d", dev.dc)
	}
}
