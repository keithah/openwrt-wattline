package discovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseTailscaleStatusNormalizesMagicDNSName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"present", `{"Self":{"DNSName":"wattline.example.ts.net."}}`, "wattline.example.ts.net"},
		{"absent", `{"Self":{}}`, ""},
		{"null self", `{"Self":null}`, ""},
		{"malformed", `{`, ""},
		{"whitespace", `{"Self":{"DNSName":"  "}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseTailscaleStatus([]byte(tt.raw)); got != tt.want {
				t.Fatalf("ParseTailscaleStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleNameRequiresExecutableAndUsesBoundedContext(t *testing.T) {
	runnerCalled := false
	resolver := Tailscale{
		Timeout: 25 * time.Millisecond,
		LookPath: func(name string) (string, error) {
			if name != "tailscale" {
				t.Fatalf("lookup = %q", name)
			}
			return "/usr/bin/tailscale", nil
		},
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			runnerCalled = true
			if executable != "/usr/bin/tailscale" || len(args) != 2 || args[0] != "status" || args[1] != "--json" {
				t.Fatalf("command = %q %#v", executable, args)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("runner context has no deadline")
			}
			return []byte(`{"Self":{"DNSName":"router.tail.example."}}`), nil
		},
	}
	if got := resolver.Name(context.Background()); got != "router.tail.example" {
		t.Fatalf("Name = %q", got)
	}
	if !runnerCalled {
		t.Fatal("runner not called")
	}

	runnerCalled = false
	resolver.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if got := resolver.Name(context.Background()); got != "" || runnerCalled {
		t.Fatalf("missing executable: name=%q runnerCalled=%v", got, runnerCalled)
	}
}

func TestTailscaleNameTreatsEveryCommandFailureAsUnavailable(t *testing.T) {
	resolver := Tailscale{
		LookPath: func(string) (string, error) { return "/usr/bin/tailscale", nil },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("daemon down") },
	}
	if got := resolver.Name(context.Background()); got != "" {
		t.Fatalf("Name = %q", got)
	}
}

func TestMagicDNSCachePublishesRefreshAtomically(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"Self":{"DNSName":"first.example.ts.net."}}`),
		[]byte(`{"Self":{"DNSName":"second.example.ts.net."}}`),
	}
	cache := NewMagicDNSCache(Tailscale{
		LookPath: func(string) (string, error) { return "/usr/bin/tailscale", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			response := responses[0]
			responses = responses[1:]
			return response, nil
		},
	})
	if cache.Name() != "" {
		t.Fatal("new cache is not empty")
	}
	cache.Refresh(context.Background())
	if got := cache.Name(); got != "first.example.ts.net" {
		t.Fatalf("first Name = %q", got)
	}
	cache.Refresh(context.Background())
	if got := cache.Name(); got != "second.example.ts.net" {
		t.Fatalf("second Name = %q", got)
	}
}
