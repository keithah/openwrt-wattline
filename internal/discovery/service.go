package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/state"
)

const serviceType = "_wattline._tcp"

// Registration is the lifetime of one DNS-SD advertisement.
type Registration interface{ Shutdown() }

// Registrar is the narrow zeroconf seam used by Service.
type Registrar interface {
	Register(instance, service, domain string, port int, text []string, interfaces []net.Interface) (Registration, error)
}

type zeroconfRegistrar struct{}

func (zeroconfRegistrar) Register(instance, service, domain string, port int, text []string, interfaces []net.Interface) (Registration, error) {
	return zeroconf.Register(instance, service, domain, port, text, interfaces)
}

// InterfaceSource makes interface discovery deterministic in tests.
type InterfaceSource interface {
	Interfaces() ([]net.Interface, error)
	Addrs(net.Interface) ([]net.Addr, error)
}

type systemInterfaces struct{}

func (systemInterfaces) Interfaces() ([]net.Interface, error) { return net.Interfaces() }
func (systemInterfaces) Addrs(iface net.Interface) ([]net.Addr, error) {
	return iface.Addrs()
}

type addressSelector struct {
	address netip.Addr
	zone    string
}

func parseAddress(value string) (addressSelector, bool) {
	value = strings.TrimSpace(value)
	address, err := netip.ParseAddr(value)
	if err != nil {
		return addressSelector{}, false
	}
	return addressSelector{address: address.WithZone("").Unmap(), zone: address.Zone()}, true
}

func interfaceAddress(value string) (addressSelector, bool) {
	value = strings.TrimSpace(value)
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	return parseAddress(value)
}

func multicastCapable(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	return true
}

// ResolveInterfaces returns only existing interfaces explicitly named by UCI,
// or owning an explicitly configured IPv4/IPv6 address. It never substitutes
// all interfaces when a name is absent.
func ResolveInterfaces(configured []string, source InterfaceSource, membership LANMembershipSource) ([]net.Interface, error) {
	if source == nil {
		source = systemInterfaces{}
	}
	if membership == nil {
		return nil, errors.New("LAN membership source is unavailable")
	}
	lanNames, err := membership.LANInterfaces()
	if err != nil {
		return nil, err
	}
	authorized := make(map[string]struct{}, len(lanNames))
	for _, name := range lanNames {
		authorized[name] = struct{}{}
	}
	interfaces, err := source.Interfaces()
	if err != nil {
		return nil, err
	}
	wantedNames := make(map[string]struct{}, len(configured))
	wantedIPs := make([]addressSelector, 0, len(configured))
	for _, value := range configured {
		if selector, ok := parseAddress(value); ok {
			if selector.address.Is6() && selector.address.IsLinkLocalUnicast() && selector.zone == "" {
				return nil, fmt.Errorf("link-local mDNS address %q requires an interface zone", value)
			}
			wantedIPs = append(wantedIPs, selector)
		} else {
			wantedNames[value] = struct{}{}
		}
	}
	selected := make([]net.Interface, 0, len(configured))
	seen := make(map[int]struct{})
	for _, iface := range interfaces {
		if !multicastCapable(iface) {
			continue
		}
		if _, isLAN := authorized[iface.Name]; !isLAN {
			continue
		}
		_, requestedByName := wantedNames[iface.Name]
		match := requestedByName
		if !match && len(wantedIPs) != 0 {
			addresses, addressErr := source.Addrs(iface)
			if addressErr != nil {
				return nil, addressErr
			}
			for _, raw := range addresses {
				candidate, ok := interfaceAddress(raw.String())
				if !ok {
					continue
				}
				for _, selector := range wantedIPs {
					if selector.address == candidate.address && (selector.zone == "" || selector.zone == iface.Name) && (candidate.zone == "" || candidate.zone == iface.Name) {
						match = true
						break
					}
				}
				if match {
					break
				}
			}
		}
		if match {
			if _, duplicate := seen[iface.Index]; !duplicate {
				selected = append(selected, iface)
				seen[iface.Index] = struct{}{}
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Index == selected[j].Index {
			return selected[i].Name < selected[j].Name
		}
		return selected[i].Index < selected[j].Index
	})
	return selected, nil
}

// Options supplies cached state and runtime settings. None of these callbacks
// may perform BLE I/O.
type Options struct {
	Version            string
	Hostname           string
	Store              *state.Store
	Config             func() *config.Config
	TLSFingerprint     func() string
	Registrar          Registrar
	Interfaces         InterfaceSource
	LANMembership      LANMembershipSource
	Logf               func(string, ...any)
	NewRetryTimer      func(time.Duration) RetryTimer
	RetryDelay         func(int) time.Duration
	NewRevalidateTimer func(time.Duration) RetryTimer
	RevalidateInterval time.Duration
}

// RetryTimer permits deterministic failure/revalidation tests and explicit
// cleanup on shutdown. Identity-less services do not create timers.
type RetryTimer interface {
	C() <-chan time.Time
	Stop()
}

type systemRetryTimer struct{ timer *time.Timer }

func (timer systemRetryTimer) C() <-chan time.Time { return timer.timer.C }
func (timer systemRetryTimer) Stop()               { timer.timer.Stop() }

func defaultRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second << min(attempt, 5)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

// Service owns at most one zeroconf responder and atomically replaces it when
// any advertised fact changes.
type Service struct {
	options Options
	refresh chan struct{}
}

func NewService(options Options) *Service {
	if options.Registrar == nil {
		options.Registrar = zeroconfRegistrar{}
	}
	if options.Interfaces == nil {
		options.Interfaces = systemInterfaces{}
	}
	if options.LANMembership == nil {
		options.LANMembership = OpenWrtLANMembership{}
	}
	if options.NewRetryTimer == nil {
		options.NewRetryTimer = func(delay time.Duration) RetryTimer { return systemRetryTimer{time.NewTimer(delay)} }
	}
	if options.RetryDelay == nil {
		options.RetryDelay = defaultRetryDelay
	}
	if options.NewRevalidateTimer == nil {
		options.NewRevalidateTimer = func(delay time.Duration) RetryTimer { return systemRetryTimer{time.NewTimer(delay)} }
	}
	if options.RevalidateInterval <= 0 {
		options.RevalidateInterval = 30 * time.Second
	}
	return &Service{options: options, refresh: make(chan struct{}, 1)}
}

// Refresh coalesces settings and certificate updates without polling.
func (service *Service) Refresh() {
	select {
	case service.refresh <- struct{}{}:
	default:
	}
}

type publication struct {
	key        string
	instance   string
	port       int
	txt        []string
	interfaces []net.Interface
}

func (service *Service) desired() (publication, bool, error) {
	if service.options.Config == nil {
		return publication{}, false, nil
	}
	cfg := service.options.Config()
	if cfg == nil || !cfg.MDNSEnabled {
		return publication{}, false, nil
	}
	port := PreferredPort(*cfg)
	if port == 0 {
		return publication{}, false, nil
	}
	identity := state.Identity{}
	if service.options.Store != nil {
		if cached := service.options.Store.Snapshot().Device; cached != nil {
			identity = *cached
		}
	}
	if identity.MAC == "" {
		identity.MAC = cfg.DeviceMAC
	}
	if strings.TrimSpace(identity.MAC) == "" {
		return publication{}, false, nil
	}
	tls := "none"
	if cfg.HTTPSEnabled {
		if service.options.TLSFingerprint == nil {
			return publication{}, false, nil
		}
		tls = strings.ToLower(strings.TrimSpace(service.options.TLSFingerprint()))
		if len(tls) != 64 {
			return publication{}, false, nil
		}
		for _, character := range tls {
			if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
				return publication{}, false, nil
			}
		}
	}
	txt := TXT(Metadata{Version: service.options.Version, API: 1, ID: identity.MAC, Model: identity.Model, CID: identity.CID, Features: identity.Features, TLS: tls})
	interfaces, err := ResolveInterfaces(cfg.MDNSInterfaces, service.options.Interfaces, service.options.LANMembership)
	if err != nil {
		return publication{}, false, err
	}
	if len(interfaces) == 0 {
		return publication{}, false, fmt.Errorf("no configured LAN mDNS interface is available")
	}
	instance := service.options.Hostname
	if strings.TrimSpace(instance) == "" {
		instance = "Wattline " + identity.MAC
	}
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%d\x00%s", instance, port, strings.Join(txt, "\x00"))
	for _, iface := range interfaces {
		fmt.Fprintf(&key, "\x00%d:%s", iface.Index, iface.Name)
	}
	return publication{key: key.String(), instance: instance, port: port, txt: txt, interfaces: interfaces}, true, nil
}

// Run reacts to state publications and explicit Refresh calls until context
// cancellation. Registration errors are non-fatal and retried on the next
// meaningful update.
func (service *Service) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	var updates <-chan state.Snapshot
	cancelSubscription := func() {}
	if service.options.Store != nil {
		updates, cancelSubscription = service.options.Store.Subscribe()
	}
	defer cancelSubscription()
	var active Registration
	activeKey := ""
	var retry RetryTimer
	var retryChannel <-chan time.Time
	retryAttempt := 0
	var revalidate RetryTimer
	var revalidateChannel <-chan time.Time
	stopRetry := func(reset bool) {
		if retry != nil {
			retry.Stop()
			retry, retryChannel = nil, nil
		}
		if reset {
			retryAttempt = 0
		}
	}
	stopRevalidate := func() {
		if revalidate != nil {
			revalidate.Stop()
			revalidate, revalidateChannel = nil, nil
		}
	}
	defer func() {
		stopRetry(false)
		stopRevalidate()
		if active != nil {
			active.Shutdown()
		}
	}()

	scheduleRetry := func() {
		if retry != nil {
			return
		}
		retry = service.options.NewRetryTimer(service.options.RetryDelay(retryAttempt))
		if retry == nil {
			return
		}
		retryChannel = retry.C()
		retryAttempt++
	}
	scheduleRevalidate := func() {
		if revalidate != nil {
			return
		}
		revalidate = service.options.NewRevalidateTimer(service.options.RevalidateInterval)
		if revalidate != nil {
			revalidateChannel = revalidate.C()
		}
	}

	reconcile := func() {
		desired, publish, err := service.desired()
		if err != nil {
			stopRevalidate()
			if active != nil {
				active.Shutdown()
				active, activeKey = nil, ""
			}
			if service.options.Logf != nil {
				service.options.Logf("wattline: mDNS interface resolution: %v", err)
			}
			scheduleRetry()
			return
		}
		if !publish {
			stopRetry(true)
			stopRevalidate()
			if active != nil {
				active.Shutdown()
				active, activeKey = nil, ""
			}
			return
		}
		if desired.key == activeKey {
			stopRetry(true)
			scheduleRevalidate()
			return
		}
		next, err := service.options.Registrar.Register(desired.instance, serviceType, "local.", desired.port, desired.txt, desired.interfaces)
		if next == nil && err == nil {
			err = errors.New("registrar returned no registration")
		}
		if err != nil {
			stopRevalidate()
			if service.options.Logf != nil {
				service.options.Logf("wattline: mDNS registration: %v", err)
			}
			scheduleRetry()
			return
		}
		stopRetry(true)
		stopRevalidate()
		previous := active
		active, activeKey = next, desired.key
		if previous != nil {
			previous.Shutdown()
		}
		scheduleRevalidate()
	}

	identityKey := ""
	if service.options.Store != nil {
		identityKey = discoveryIdentityKey(service.options.Store.Snapshot())
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return nil
		case snapshot := <-updates:
			if key := discoveryIdentityKey(snapshot); key != identityKey {
				identityKey = key
				reconcile()
			}
		case <-service.refresh:
			stopRetry(true)
			stopRevalidate()
			reconcile()
		case <-retryChannel:
			retry, retryChannel = nil, nil
			reconcile()
		case <-revalidateChannel:
			revalidate, revalidateChannel = nil, nil
			reconcile()
		}
	}
}

func discoveryIdentityKey(snapshot state.Snapshot) string {
	if snapshot.Device == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%04x\x00%08x", snapshot.Device.MAC, snapshot.Device.Model, snapshot.Device.CID, snapshot.Device.Features)
}
