package discovery

import (
	"context"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type fakeInterfaces struct {
	interfaces []net.Interface
	addresses  map[int][]net.Addr
}

func (f fakeInterfaces) Interfaces() ([]net.Interface, error) {
	return append([]net.Interface(nil), f.interfaces...), nil
}
func (f fakeInterfaces) Addrs(i net.Interface) ([]net.Addr, error) {
	return append([]net.Addr(nil), f.addresses[i.Index]...), nil
}

type fakeAddr string

func (a fakeAddr) Network() string { return "ip" }
func (a fakeAddr) String() string  { return string(a) }

func TestResolveInterfacesMatchesOnlyConfiguredNamesOrAddresses(t *testing.T) {
	source := fakeInterfaces{
		interfaces: []net.Interface{{Index: 1, Name: "lo"}, {Index: 2, Name: "br-lan"}, {Index: 3, Name: "tailscale0"}},
		addresses: map[int][]net.Addr{
			2: {fakeAddr("192.168.8.1/24"), fakeAddr("fd00::1/64")},
			3: {fakeAddr("100.64.0.1/32")},
		},
	}
	source.addresses[2] = append(source.addresses[2], fakeAddr("fe80::1%br-lan/64"))
	for _, configured := range [][]string{{"br-lan"}, {"192.168.8.1"}, {"fd00::1"}, {"fe80::1"}} {
		got, err := ResolveInterfaces(configured, source)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "br-lan" {
			t.Fatalf("ResolveInterfaces(%v) = %#v", configured, got)
		}
	}
	got, err := ResolveInterfaces([]string{"missing"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("missing interface fell back to %#v", got)
	}
}

type fakeRegistration struct {
	mu       sync.Mutex
	shutdown int
}

func (r *fakeRegistration) Shutdown() {
	r.mu.Lock()
	r.shutdown++
	r.mu.Unlock()
}

func (r *fakeRegistration) shutdowns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shutdown
}

type registerCall struct {
	instance, service, domain string
	port                      int
	txt                       []string
	interfaces                []net.Interface
	registration              *fakeRegistration
}

type fakeRegistrar struct {
	mu    sync.Mutex
	calls []registerCall
}

func (r *fakeRegistrar) Register(instance, service, domain string, port int, txt []string, interfaces []net.Interface) (Registration, error) {
	registration := &fakeRegistration{}
	r.mu.Lock()
	r.calls = append(r.calls, registerCall{instance, service, domain, port, append([]string(nil), txt...), append([]net.Interface(nil), interfaces...), registration})
	r.mu.Unlock()
	return registration, nil
}

func (r *fakeRegistrar) snapshot() []registerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]registerCall(nil), r.calls...)
}

func waitCalls(t *testing.T, registrar *fakeRegistrar, count int) []registerCall {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls := registrar.snapshot(); len(calls) >= count {
			return calls
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("registrations = %d, want %d", len(registrar.snapshot()), count)
	return nil
}

func TestServiceSuppressesUntilMACAndReregistersOnlyOnMeaningfulChange(t *testing.T) {
	store := state.NewStore()
	registrar := &fakeRegistrar{}
	cfg := &config.Config{DeviceMAC: "", MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377, HTTPSEnabled: true, HTTPSPort: 8378}
	var cfgMu sync.RWMutex
	var fingerprint atomic.Value
	fingerprint.Store("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	service := NewService(Options{
		Version: "1.3.0", Store: store, Config: func() *config.Config {
			cfgMu.RLock()
			defer cfgMu.RUnlock()
			copy := *cfg
			copy.MDNSInterfaces = append([]string(nil), cfg.MDNSInterfaces...)
			return &copy
		},
		TLSFingerprint: func() string { return fingerprint.Load().(string) },
		Registrar:      registrar,
		Interfaces:     fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan"}}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	if calls := registrar.snapshot(); len(calls) != 0 {
		t.Fatalf("published preliminary identity: %#v", calls)
	}

	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Model: "BP4SL3V2", CID: 0x305, Features: 0xfff, FeatureSet: proto.DecodeFeatures(0xfff)})
	calls := waitCalls(t, registrar, 1)
	if calls[0].service != "_wattline._tcp" || calls[0].domain != "local." || calls[0].port != 8378 || len(calls[0].interfaces) != 1 || calls[0].interfaces[0].Name != "br-lan" {
		t.Fatalf("registration = %+v", calls[0])
	}
	wantTXT := TXT(Metadata{Version: "1.3.0", API: 1, ID: "DC:04:5A:EB:72:2B", Model: "BP4SL3V2", CID: 0x305, Features: 0xfff, TLS: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if !reflect.DeepEqual(calls[0].txt, wantTXT) {
		t.Fatalf("TXT = %#v, want %#v", calls[0].txt, wantTXT)
	}
	store.SetConnected(true) // telemetry-only state is not meaningful to DNS-SD.
	time.Sleep(10 * time.Millisecond)
	if got := len(registrar.snapshot()); got != 1 {
		t.Fatalf("telemetry caused %d registrations", got)
	}

	cfgMu.Lock()
	cfg.HTTPSEnabled = false
	cfgMu.Unlock()
	service.Refresh()
	calls = waitCalls(t, registrar, 2)
	if calls[1].port != 8377 || calls[1].txt[6] != "tls=none" || calls[0].registration.shutdowns() != 1 {
		t.Fatalf("updated registrations = %+v, first shutdowns=%d", calls, calls[0].registration.shutdowns())
	}

	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Model: "BP4SL3V2", CID: 0x305, Features: 0x10})
	calls = waitCalls(t, registrar, 3)
	if calls[2].txt[5] != "features=00000010" || calls[1].registration.shutdowns() != 1 {
		t.Fatalf("identity update = %+v", calls[2])
	}
	cfgMu.Lock()
	cfg.HTTPSEnabled = true
	cfgMu.Unlock()
	fingerprint.Store("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	service.Refresh()
	calls = waitCalls(t, registrar, 4)
	if calls[3].port != 8378 || calls[3].txt[6] != "tls=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || calls[2].registration.shutdowns() != 1 {
		t.Fatalf("TLS/listener update = %+v", calls[3])
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls[3].registration.shutdowns() != 1 {
		t.Fatalf("active registration shutdowns = %d", calls[3].registration.shutdowns())
	}
}

func TestServiceNeverFallsBackWhenConfiguredInterfaceIsAbsent(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar, Interfaces: fakeInterfaces{interfaces: []net.Interface{{Index: 1, Name: "lo"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls := registrar.snapshot(); len(calls) != 0 {
		t.Fatalf("registered on wildcard interfaces: %#v", calls)
	}
}

func TestServiceDoesNotRegisterWhenContextIsAlreadyCanceled(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar, Interfaces: fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan"}}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := registrar.snapshot(); len(calls) != 0 {
		t.Fatalf("canceled service registered: %#v", calls)
	}
}
