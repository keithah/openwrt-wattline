package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/auth"
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

func TestInitGeneratesBootstrapTokenCertificateAndTokenStoreIdempotently(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "wattline")
	certPath, keyPath, storePath := filepath.Join(dir, "tls", "server.crt"), filepath.Join(dir, "tls", "server.key"), filepath.Join(dir, "tokens.json")
	raw := "config wattline 'main'\n\toption tls_cert '" + certPath + "'\n\toption tls_key '" + keyPath + "'\n\toption token_store '" + storePath + "'\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := initialize(cfgPath, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" || strings.Contains(cfg.Token, " ") {
		t.Fatalf("token = %q", cfg.Token)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("token store: %v", err)
	}
	second, err := initialize(cfgPath, []string{"router.lan"})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _ := config.Load(cfgPath)
	if reloaded.Token != cfg.Token {
		t.Fatal("init rotated bootstrap token")
	}
	if second.SHA256 != first.SHA256 {
		t.Fatal("init rotated certificate")
	}
}

func TestInitRejectsCorruptCertificateWithoutReplacingIt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "wattline")
	certPath, keyPath := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	if err := os.WriteFile(certPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := "config wattline 'main'\n\toption token 'existing'\n\toption tls_cert '" + certPath + "'\n\toption tls_key '" + keyPath + "'\n\toption token_store '" + filepath.Join(dir, "tokens.json") + "'\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := initialize(cfgPath, nil); err == nil {
		t.Fatal("corrupt one-sided certificate accepted")
	}
	got, _ := os.ReadFile(certPath)
	if string(got) != "corrupt" {
		t.Fatalf("corrupt cert was overwritten: %q", got)
	}
}

func TestLiveConfigSwitchesTokenStoreTransactionally(t *testing.T) {
	dir := t.TempDir()
	oldPath, newPath := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	oldStore, err := auth.OpenStore(oldPath, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	pairing := auth.NewPairing(oldStore, 5*time.Minute, false)
	before := &config.Config{Token: "bootstrap", TokenStore: oldPath, PairingTTL: 5 * time.Minute, BLEPIN: "020555"}
	after := cloneConfig(before)
	after.TokenStore = newPath
	after.PairingTTL = 10 * time.Minute
	after.PairingAlwaysOn = true
	after.BLEPIN = "123456"
	live := &liveConfig{cfg: cloneConfig(before), store: oldStore, pairing: pairing}
	rollback, err := live.apply(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if live.authStore() == oldStore {
		t.Fatal("token store did not switch")
	}
	if principal, ok := live.authStore().Authenticate("bootstrap"); !ok || principal.Role != auth.RoleAdmin {
		t.Fatal("new store lacks bootstrap")
	}
	if status := pairing.Status(true); !status.Open || status.PIN == "" {
		t.Fatalf("pairing policy not applied: %+v", status)
	}
	rollback()
	if live.authStore() != oldStore {
		t.Fatal("rollback did not restore token store")
	}
	if status := pairing.Status(true); status.Open {
		t.Fatalf("rollback did not restore pairing policy: %+v", status)
	}
}

func TestListenerConfigPreservesIndependentEndpoints(t *testing.T) {
	cfg := &config.Config{HTTPEnabled: true, HTTPAddr4: "127.0.0.1", HTTPAddr6: "::1", HTTPPort: 8000, HTTPSEnabled: false, HTTPSAddr4: "0.0.0.0", HTTPSAddr6: "::", HTTPSPort: 9000, TLSCert: "cert", TLSKey: "key"}
	got := listenerConfig(cfg)
	if !got.HTTP.Enabled || got.HTTP.Port != 8000 || got.HTTPS.Enabled || got.HTTPS.Port != 9000 || got.CertFile != "cert" || got.KeyFile != "key" {
		t.Fatalf("listener config = %+v", got)
	}
}
