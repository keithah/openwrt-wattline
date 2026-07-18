package discovery

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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
		{"bad label", `{"Self":{"DNSName":"-router.example.ts.net."}}`, ""},
		{"empty label", `{"Self":{"DNSName":"router..example.ts.net."}}`, ""},
		{"not dns", `{"Self":{"DNSName":"router/example"}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseTailscaleStatus([]byte(tt.raw)); got != tt.want {
				t.Fatalf("ParseTailscaleStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBoundedCommandRejectsOversizedOutput(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := runBoundedCommand(ctx, sh, "-c", "head -c 1100000 /dev/zero")
	if !errors.Is(err, ErrCommandOutputTooLarge) || len(output) > maxCommandOutput {
		t.Fatalf("len=%d err=%v", len(output), err)
	}
}

func TestBoundedCommandKillsHangingChildProcessGroup(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	output, err := runBoundedCommand(ctx, sh, "-c", "sleep 30 & child=$!; echo $child; wait")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("elapsed=%v err=%v", time.Since(started), err)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(output)))
	if parseErr != nil {
		t.Fatalf("child PID %q: %v", output, parseErr)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if killErr := syscall.Kill(pid, 0); errors.Is(killErr, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived cancellation", pid)
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
