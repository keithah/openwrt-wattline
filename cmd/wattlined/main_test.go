package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/discovery"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/rules"
	serverpkg "github.com/keithah/openwrt-wattline/internal/server"
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

func TestDiscoveryOptionsUseDynamicMDNSPolicyButServedListeners(t *testing.T) {
	cfg := &config.Config{HTTPEnabled: true, HTTPPort: 8377, MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}}
	live := &liveConfig{cfg: cloneConfig(cfg)}
	tlsState := &tlsIdentity{served: serverpkg.Certificate{SHA256: strings.Repeat("a", 64)}}
	store := state.NewStore()
	options := discoveryOptions("1.3.0", "router", store, live, tlsState, listenerConfig(cfg))
	if options.Version != "1.3.0" || options.Hostname != "router" || options.Store != store || options.Config().HTTPPort != 8377 || options.TLSFingerprint() != strings.Repeat("a", 64) {
		t.Fatalf("options = %+v", options)
	}
	next := cloneConfig(cfg)
	next.HTTPPort = 9000 // pending until restart: must not be advertised.
	next.HTTPSEnabled = true
	next.HTTPSPort = 9443
	next.MDNSInterfaces = []string{"lan2"}
	live.mu.Lock()
	live.cfg = next
	live.mu.Unlock()
	discovery.NewService(options).Refresh()
	got := options.Config()
	if got.HTTPPort != 8377 || got.HTTPSEnabled || got.HTTPSPort == 9443 || discovery.PreferredPort(*got) != 8377 || !reflect.DeepEqual(got.MDNSInterfaces, []string{"lan2"}) {
		t.Fatalf("projected discovery config = %+v", got)
	}
}

func TestPreferredLANHostMatchesAdvertisedLocalName(t *testing.T) {
	for input, want := range map[string]string{"router": "router.local", "router.local.": "router.local", "192.168.8.1": "192.168.8.1.local", "": "wattline.local"} {
		if got := preferredLANHost(input); got != want {
			t.Fatalf("preferredLANHost(%q) = %q, want %q", input, got, want)
		}
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
	_, managed, err := oldStore.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	invalidated, cancel, active := oldStore.SubscribeRevocation(managed.ID)
	defer cancel()
	if !active {
		t.Fatal("managed token subscription was inactive")
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
	select {
	case <-invalidated:
		t.Fatal("rollback invalidated old-store subscribers")
	default:
	}
}

func TestLiveConfigCommitInvalidatesOldStoreSubscribersWithoutRevokingTokens(t *testing.T) {
	dir := t.TempDir()
	oldPath, newPath := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	oldStore, err := auth.OpenStore(oldPath, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	secret, managed, err := oldStore.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	invalidated, cancel, active := oldStore.SubscribeRevocation(managed.ID)
	defer cancel()
	if !active {
		t.Fatal("managed token subscription was inactive")
	}
	pairing := auth.NewPairing(oldStore, 5*time.Minute, false)
	before := &config.Config{Token: "bootstrap", TokenStore: oldPath, PairingTTL: 5 * time.Minute}
	after := cloneConfig(before)
	after.TokenStore = newPath
	live := &liveConfig{cfg: cloneConfig(before), store: oldStore, pairing: pairing}
	if _, err := live.apply(before, after); err != nil {
		t.Fatal(err)
	}
	select {
	case <-invalidated:
		t.Fatal("apply invalidated subscribers before persistence commit")
	default:
	}
	live.commit(before, after)
	select {
	case <-invalidated:
	default:
		t.Fatal("commit did not invalidate old-store subscribers")
	}
	if _, ok := oldStore.Authenticate(secret); !ok {
		t.Fatal("subscriber invalidation revoked the old on-disk token")
	}
}

func TestListenerConfigPreservesIndependentEndpoints(t *testing.T) {
	cfg := &config.Config{HTTPEnabled: true, HTTPAddr4: "127.0.0.1", HTTPAddr6: "::1", HTTPPort: 8000, HTTPSEnabled: false, HTTPSAddr4: "0.0.0.0", HTTPSAddr6: "::", HTTPSPort: 9000, TLSCert: "cert", TLSKey: "key"}
	got := listenerConfig(cfg)
	if !got.HTTP.Enabled || got.HTTP.Port != 8000 || got.HTTPS.Enabled || got.HTTPS.Port != 9000 || got.CertFile != "cert" || got.KeyFile != "key" {
		t.Fatalf("listener config = %+v", got)
	}
}

func TestTLSIdentityRotationDoesNotChangeServedFingerprint(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert"), filepath.Join(dir, "key")
	cert, err := serverpkg.EnsureCertificate(certFile, keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := newTLSIdentity(cert, nil)
	rotated, err := identity.rotate()
	if err != nil {
		t.Fatal(err)
	}
	if rotated == cert.SHA256 {
		t.Fatal("rotation did not create a new fingerprint")
	}
	if identity.fingerprint() != cert.SHA256 {
		t.Fatalf("served fingerprint changed before restart: %s", identity.fingerprint())
	}
}

func TestTLSIdentityRotatesConfiguredPathsButKeepsServedPin(t *testing.T) {
	dir := t.TempDir()
	served, err := serverpkg.EnsureCertificate(filepath.Join(dir, "served.crt"), filepath.Join(dir, "served.key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	newCert, newKey := filepath.Join(dir, "next", "server.crt"), filepath.Join(dir, "next", "server.key")
	identity := newTLSIdentity(served, nil)
	identity.paths = func() (string, string) { return newCert, newKey }
	staged, err := identity.rotate()
	if err != nil {
		t.Fatal(err)
	}
	if staged == served.SHA256 || identity.fingerprint() != served.SHA256 {
		t.Fatalf("staged=%s served=%s", staged, identity.fingerprint())
	}
	if _, err := os.Stat(newCert); err != nil {
		t.Fatalf("configured certificate not created: %v", err)
	}
	if _, err := os.Stat(newKey); err != nil {
		t.Fatalf("configured key not created: %v", err)
	}
}

func TestLiveConfigSavePublishesCommittedNextWithoutReloadSplit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wattline")
	if err := os.WriteFile(path, []byte("config wattline 'main'\n\toption token 'fresh-secret'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Advanced = true
	current := cloneConfig(next)
	current.Token = "runtime-bootstrap"
	next.Token = "stale-request-copy"
	live := &liveConfig{cfg: current}
	if err := live.save(path, next); err != nil {
		t.Fatal(err)
	}
	got := live.current()
	if !got.Advanced || got.Token != "runtime-bootstrap" {
		t.Fatalf("published config = %+v", got)
	}
}
