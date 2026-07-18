package discovery

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const defaultTailscaleTimeout = 2 * time.Second

// ParseTailscaleStatus extracts the local node's MagicDNS name. The CLI emits
// an absolute DNS name with a trailing dot; HTTP and QR consumers use a host.
func ParseTailscaleStatus(raw []byte) string {
	var status struct {
		Self *struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Self == nil {
		return ""
	}
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(status.Self.DNSName), "."))
	if !validDNSName(name) {
		return ""
	}
	return name
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

// Tailscale provides a dependency-free, optional MagicDNS lookup. Its seams
// keep process execution out of unit tests.
type Tailscale struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
	Timeout  time.Duration
}

// Name returns an empty string whenever Tailscale is absent, its daemon is
// unavailable, the command times out, or its response is invalid.
func (tailscale Tailscale) Name(ctx context.Context) string {
	lookup := tailscale.LookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	executable, err := lookup("tailscale")
	if err != nil {
		return ""
	}
	timeout := tailscale.Timeout
	if timeout <= 0 {
		timeout = defaultTailscaleTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := tailscale.Run
	if run == nil {
		run = func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			return runBoundedCommand(ctx, executable, args...)
		}
	}
	raw, err := run(bounded, executable, "status", "--json")
	if err != nil {
		return ""
	}
	return ParseTailscaleStatus(raw)
}

// MagicDNSName uses the optional local Tailscale CLI without creating a
// daemon/runtime dependency.
func MagicDNSName(ctx context.Context) string { return (Tailscale{}).Name(ctx) }

// MagicDNSCache gives HTTP handlers a zero-I/O, race-safe callback while
// allowing lifecycle events such as startup and SIGHUP to refresh the CLI fact.
type MagicDNSCache struct {
	resolver  Tailscale
	refreshMu sync.Mutex
	mu        sync.RWMutex
	name      string
}

func NewMagicDNSCache(resolver Tailscale) *MagicDNSCache {
	return &MagicDNSCache{resolver: resolver}
}

func (cache *MagicDNSCache) Refresh(ctx context.Context) {
	cache.refreshMu.Lock()
	name := cache.resolver.Name(ctx)
	cache.mu.Lock()
	cache.name = name
	cache.mu.Unlock()
	cache.refreshMu.Unlock()
}

func (cache *MagicDNSCache) Name() string {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.name
}
