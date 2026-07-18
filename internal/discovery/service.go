package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

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

func configuredIP(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if zone := strings.LastIndexByte(value, '%'); zone >= 0 {
		value = value[:zone]
	}
	address, err := netip.ParseAddr(value)
	return address.Unmap(), err == nil
}

func interfaceAddress(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	return configuredIP(value)
}

// ResolveInterfaces returns only existing interfaces explicitly named by UCI,
// or owning an explicitly configured IPv4/IPv6 address. It never substitutes
// all interfaces when a name is absent.
func ResolveInterfaces(configured []string, source InterfaceSource) ([]net.Interface, error) {
	if source == nil {
		source = systemInterfaces{}
	}
	interfaces, err := source.Interfaces()
	if err != nil {
		return nil, err
	}
	wantedNames := make(map[string]struct{}, len(configured))
	wantedIPs := make(map[netip.Addr]struct{}, len(configured))
	for _, value := range configured {
		if address, ok := configuredIP(value); ok {
			wantedIPs[address] = struct{}{}
		} else {
			wantedNames[value] = struct{}{}
		}
	}
	selected := make([]net.Interface, 0, len(configured))
	seen := make(map[int]struct{})
	for _, iface := range interfaces {
		_, match := wantedNames[iface.Name]
		if !match && len(wantedIPs) != 0 {
			addresses, addressErr := source.Addrs(iface)
			if addressErr != nil {
				return nil, addressErr
			}
			for _, raw := range addresses {
				address, ok := interfaceAddress(raw.String())
				if ok {
					if _, match = wantedIPs[address]; match {
						break
					}
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
	Version        string
	Hostname       string
	Store          *state.Store
	Config         func() *config.Config
	TLSFingerprint func() string
	Registrar      Registrar
	Interfaces     InterfaceSource
	Logf           func(string, ...any)
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
	interfaces, err := ResolveInterfaces(cfg.MDNSInterfaces, service.options.Interfaces)
	if err != nil || len(interfaces) == 0 {
		return publication{}, false, err
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
	defer func() {
		if active != nil {
			active.Shutdown()
		}
	}()

	reconcile := func() {
		desired, publish, err := service.desired()
		if err != nil {
			if service.options.Logf != nil {
				service.options.Logf("wattline: mDNS interface resolution: %v", err)
			}
			return
		}
		if !publish {
			if active != nil {
				active.Shutdown()
				active, activeKey = nil, ""
			}
			return
		}
		if desired.key == activeKey {
			return
		}
		next, err := service.options.Registrar.Register(desired.instance, serviceType, "local.", desired.port, desired.txt, desired.interfaces)
		if err != nil {
			if service.options.Logf != nil {
				service.options.Logf("wattline: mDNS registration: %v", err)
			}
			return
		}
		previous := active
		active, activeKey = next, desired.key
		if previous != nil {
			previous.Shutdown()
		}
	}

	reconcile()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-updates:
			reconcile()
		case <-service.refresh:
			reconcile()
		}
	}
}
