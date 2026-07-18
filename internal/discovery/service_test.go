package discovery

import (
	"context"
	"errors"
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

type fakeLANMembership struct {
	names []string
	err   error
}

func (membership fakeLANMembership) LANInterfaces() ([]string, error) {
	return append([]string(nil), membership.names...), membership.err
}

func lanOnly(names ...string) LANMembershipSource { return fakeLANMembership{names: names} }

type dynamicLANMembership struct {
	mu    sync.RWMutex
	names []string
}

func (membership *dynamicLANMembership) LANInterfaces() ([]string, error) {
	membership.mu.RLock()
	defer membership.mu.RUnlock()
	return append([]string(nil), membership.names...), nil
}

func (membership *dynamicLANMembership) set(names ...string) {
	membership.mu.Lock()
	membership.names = append([]string(nil), names...)
	membership.mu.Unlock()
}

func TestResolveInterfacesMatchesOnlyConfiguredNamesOrAddresses(t *testing.T) {
	source := fakeInterfaces{
		interfaces: []net.Interface{
			{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagMulticast | net.FlagLoopback},
			{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast},
			{Index: 3, Name: "tailscale0", Flags: net.FlagUp | net.FlagMulticast},
			{Index: 4, Name: "wan", Flags: net.FlagUp | net.FlagMulticast},
			{Index: 5, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast},
			{Index: 6, Name: "internet", Flags: net.FlagUp | net.FlagMulticast},
		},
		addresses: map[int][]net.Addr{
			3: {fakeAddr("100.64.0.1/32")},
			4: {fakeAddr("fe80::1%wan/64")},
			5: {fakeAddr("192.168.9.1/24")},
			6: {fakeAddr("192.168.10.1/24")},
			2: {fakeAddr("192.168.8.1/24"), fakeAddr("fd00::1/64"), fakeAddr("2001:db8::1/64")},
		},
	}
	source.addresses[2] = append(source.addresses[2], fakeAddr("fe80::1%br-lan/64"))
	membership := lanOnly("br-lan")
	for _, configured := range [][]string{{"br-lan"}, {"192.168.8.1"}, {"fd00::1"}, {"fe80::1%br-lan"}} {
		got, err := ResolveInterfaces(configured, source, membership)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "br-lan" {
			t.Fatalf("ResolveInterfaces(%v) = %#v", configured, got)
		}
	}
	got, err := ResolveInterfaces([]string{"missing"}, source, membership)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("missing interface fell back to %#v", got)
	}
	if _, err := ResolveInterfaces([]string{"fe80::1"}, source, membership); err == nil {
		t.Fatal("accepted ambiguous unscoped link-local selector")
	}
	for _, forbidden := range []string{"lo", "wan", "tailscale0", "fe80::1%wan", "eth0", "internet"} {
		got, err := ResolveInterfaces([]string{forbidden}, source, membership)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("forbidden interface %q resolved to %#v", forbidden, got)
		}
	}
	got, err = ResolveInterfaces([]string{"192.168.9.1"}, source, membership)
	if err != nil || len(got) != 0 {
		t.Fatalf("non-LAN explicit address selector = %#v, %v", got, err)
	}
	got, err = ResolveInterfaces([]string{"192.168.9.1"}, source, lanOnly("br-lan", "eth0"))
	if err != nil || len(got) != 1 || got[0].Name != "eth0" {
		t.Fatalf("authoritative LAN address selector = %#v, %v", got, err)
	}
	got, err = ResolveInterfaces([]string{"2001:db8::1"}, source, membership)
	if err != nil || len(got) != 1 || got[0].Name != "br-lan" {
		t.Fatalf("authoritative LAN global selector = %#v, %v", got, err)
	}
	if _, err := ResolveInterfaces([]string{"br-lan"}, source, fakeLANMembership{err: errors.New("ubus unavailable")}); err == nil {
		t.Fatal("LAN membership failure did not fail closed")
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
	mu              sync.Mutex
	calls           []registerCall
	fail            int
	nilRegistration bool
}

type blockingRegistrar struct {
	base      *fakeRegistrar
	blockNext atomic.Bool
	entered   chan struct{}
	release   chan struct{}
}

func newBlockingRegistrar() *blockingRegistrar {
	return &blockingRegistrar{base: &fakeRegistrar{}, entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingRegistrar) Register(instance, service, domain string, port int, txt []string, interfaces []net.Interface) (Registration, error) {
	if r.blockNext.CompareAndSwap(true, false) {
		close(r.entered)
		<-r.release
	}
	return r.base.Register(instance, service, domain, port, txt, interfaces)
}

func (r *fakeRegistrar) Register(instance, service, domain string, port int, txt []string, interfaces []net.Interface) (Registration, error) {
	r.mu.Lock()
	if r.fail > 0 {
		r.fail--
		r.mu.Unlock()
		return nil, errors.New("temporary registration failure")
	}
	if r.nilRegistration {
		r.mu.Unlock()
		return nil, nil
	}
	registration := &fakeRegistration{}
	r.calls = append(r.calls, registerCall{instance, service, domain, port, append([]string(nil), txt...), append([]net.Interface(nil), interfaces...), registration})
	r.mu.Unlock()
	return registration, nil
}

type manualRetryTimer struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (timer *manualRetryTimer) C() <-chan time.Time { return timer.ch }
func (timer *manualRetryTimer) Stop()               { timer.stopped.Store(true) }

type manualTimerFactory struct {
	mu        sync.Mutex
	timers    []*manualRetryTimer
	durations []time.Duration
}

func (factory *manualTimerFactory) New(duration time.Duration) RetryTimer {
	timer := &manualRetryTimer{ch: make(chan time.Time, 1)}
	factory.mu.Lock()
	factory.timers = append(factory.timers, timer)
	factory.durations = append(factory.durations, duration)
	factory.mu.Unlock()
	return timer
}

func (factory *manualTimerFactory) wait(t *testing.T, count int) *manualRetryTimer {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		factory.mu.Lock()
		if len(factory.timers) >= count {
			timer := factory.timers[count-1]
			factory.mu.Unlock()
			return timer
		}
		factory.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("retry timers < %d", count)
	return nil
}

func (factory *manualTimerFactory) count() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return len(factory.timers)
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
		Interfaces:     fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership:  lanOnly("br-lan"),
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

func TestServiceResubscribesAfterStateSubscriberOverflow(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 1})
	registrar := newBlockingRegistrar()
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar,
		Interfaces:    fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership: lanOnly("br-lan"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitCalls(t, registrar.base, 1)

	registrar.blockNext.Store(true)
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 2})
	select {
	case <-registrar.entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("service did not enter blocking registration")
	}
	// Overflow the subscription with snapshots whose discovery key is still 2.
	// The later identity update therefore cannot be recovered by reconciling an
	// accepted queued frame; Run must observe closure and resubscribe atomically.
	for update := 0; update < 200; update++ {
		store.SetConnected(update%2 == 0)
	}
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 200})
	close(registrar.release)

	deadline := time.Now().Add(3 * time.Second)
	foundLatest := false
	for time.Now().Before(deadline) {
		for _, call := range registrar.base.snapshot() {
			for _, entry := range call.txt {
				if entry == "features=000000c8" {
					foundLatest = true
					break
				}
			}
		}
		if foundLatest {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !foundLatest {
		cancel()
		<-done
		t.Fatal("service did not publish the latest identity after overflow")
	}
	// Prove the replacement subscription remains live after its atomic catch-up.
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 201})
	deadline = time.Now().Add(time.Second)
	foundFuture := false
	for time.Now().Before(deadline) {
		for _, call := range registrar.base.snapshot() {
			for _, entry := range call.txt {
				if entry == "features=000000c9" {
					foundFuture = true
					break
				}
			}
		}
		if foundFuture {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not stop after subscriber overflow")
	}
	if !foundFuture {
		t.Fatal("service did not resubscribe and publish a future identity after overflow")
	}
}

func TestServiceNeverFallsBackWhenConfiguredInterfaceIsAbsent(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar, Interfaces: fakeInterfaces{interfaces: []net.Interface{{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagMulticast | net.FlagLoopback}}}, LANMembership: lanOnly("br-lan")})
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
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar, Interfaces: fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}}, LANMembership: lanOnly("br-lan")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls := registrar.snapshot(); len(calls) != 0 {
		t.Fatalf("canceled service registered: %#v", calls)
	}
}

func TestServiceRetriesRegistrationFailuresWithoutHealthyPolling(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{fail: 1}
	timers := &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar,
		Interfaces:    fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership: lanOnly("br-lan"),
		NewRetryTimer: timers.New,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	retry := timers.wait(t, 1)
	retry.ch <- time.Now()
	waitCalls(t, registrar, 1)
	store.SetConnected(true)
	time.Sleep(10 * time.Millisecond)
	if got := timers.count(); got != 1 {
		t.Fatalf("healthy service created %d retry timers", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRetryDelayIsExponentiallyBounded(t *testing.T) {
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		if got := defaultRetryDelay(attempt); got != want {
			t.Fatalf("attempt %d delay = %v, want %v", attempt, got, want)
		}
	}
}

func TestServiceRetriesMissingInterfaceAndStopsTimerOnCancel(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	timers := &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: &fakeRegistrar{}, Interfaces: fakeInterfaces{}, LANMembership: lanOnly("br-lan"), NewRetryTimer: timers.New})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	retry := timers.wait(t, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !retry.stopped.Load() {
		t.Fatal("retry timer was not stopped on cancellation")
	}
}

func TestServiceDoesNotRetryWithoutMACOrAfterNilRegistration(t *testing.T) {
	store := state.NewStore()
	timers := &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	registrar := &fakeRegistrar{nilRegistration: true}
	service := NewService(Options{Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar, Interfaces: fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}}, LANMembership: lanOnly("br-lan"), NewRetryTimer: timers.New})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	if timers.count() != 0 {
		t.Fatal("service polled while MAC was unknown")
	}
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	timers.wait(t, 1) // nil registrations are failures, not an active responder.
	cancel()
	<-done
}

func TestServicePeriodicallyRevalidatesAndWithdrawsLostLANMembership(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{}
	membership := &dynamicLANMembership{names: []string{"br-lan"}}
	healthyTimers, retryTimers := &manualTimerFactory{}, &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar,
		Interfaces:    fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership: membership, NewRetryTimer: retryTimers.New, NewRevalidateTimer: healthyTimers.New,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	calls := waitCalls(t, registrar, 1)
	revalidate := healthyTimers.wait(t, 1)
	if retryTimers.count() != 0 {
		t.Fatal("healthy service entered failure retry")
	}
	membership.set()
	revalidate.ch <- time.Now()
	retry := retryTimers.wait(t, 1)
	if calls[0].registration.shutdowns() != 1 {
		t.Fatal("registration remained active after LAN membership was removed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !retry.stopped.Load() {
		t.Fatal("failure retry was not stopped on shutdown")
	}
}

func TestServiceStopsHealthyRevalidationTimerOnShutdown(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	healthyTimers := &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: &fakeRegistrar{},
		Interfaces:    fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership: lanOnly("br-lan"), NewRevalidateTimer: healthyTimers.New,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	healthy := healthyTimers.wait(t, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !healthy.stopped.Load() {
		t.Fatal("healthy revalidation timer was not stopped")
	}
}

func TestServiceRevokesUnauthorizedInterfaceBeforeReplacementAttempt(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B"})
	registrar := &fakeRegistrar{}
	membership := &dynamicLANMembership{names: []string{"br-a"}}
	healthyTimers, retryTimers := &manualTimerFactory{}, &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-a", "br-b"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar,
		Interfaces: fakeInterfaces{interfaces: []net.Interface{
			{Index: 2, Name: "br-a", Flags: net.FlagUp | net.FlagMulticast},
			{Index: 3, Name: "br-b", Flags: net.FlagUp | net.FlagMulticast},
		}},
		LANMembership: membership, NewRetryTimer: retryTimers.New, NewRevalidateTimer: healthyTimers.New,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	calls := waitCalls(t, registrar, 1)
	if calls[0].interfaces[0].Name != "br-a" {
		t.Fatalf("initial interfaces = %#v", calls[0].interfaces)
	}
	healthy := healthyTimers.wait(t, 1)
	membership.set("br-b")
	registrar.mu.Lock()
	registrar.fail = 1
	registrar.mu.Unlock()
	healthy.ch <- time.Now()
	retry := retryTimers.wait(t, 1)
	if calls[0].registration.shutdowns() != 1 {
		t.Fatal("old responder remained active after its interface lost LAN authorization")
	}
	retry.ch <- time.Now()
	calls = waitCalls(t, registrar, 2)
	if len(calls[1].interfaces) != 1 || calls[1].interfaces[0].Name != "br-b" {
		t.Fatalf("replacement interfaces = %#v", calls[1].interfaces)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceKeepsAuthorizedResponderWhenMetadataReplacementFails(t *testing.T) {
	store := state.NewStore()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 1})
	registrar := &fakeRegistrar{}
	retryTimers := &manualTimerFactory{}
	cfg := &config.Config{MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"}, HTTPEnabled: true, HTTPPort: 8377}
	service := NewService(Options{
		Version: "dev", Store: store, Config: func() *config.Config { return cfg }, Registrar: registrar,
		Interfaces:    fakeInterfaces{interfaces: []net.Interface{{Index: 2, Name: "br-lan", Flags: net.FlagUp | net.FlagMulticast}}},
		LANMembership: lanOnly("br-lan"), NewRetryTimer: retryTimers.New, NewRevalidateTimer: (&manualTimerFactory{}).New,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	calls := waitCalls(t, registrar, 1)
	registrar.mu.Lock()
	registrar.fail = 1
	registrar.mu.Unlock()
	store.SetIdentity(state.Identity{MAC: "DC:04:5A:EB:72:2B", Features: 2})
	retry := retryTimers.wait(t, 1)
	if calls[0].registration.shutdowns() != 0 {
		t.Fatal("authorized responder was revoked by a metadata-only registration failure")
	}
	retry.ch <- time.Now()
	calls = waitCalls(t, registrar, 2)
	if calls[0].registration.shutdowns() != 1 {
		t.Fatal("old metadata responder was not replaced after retry succeeded")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
