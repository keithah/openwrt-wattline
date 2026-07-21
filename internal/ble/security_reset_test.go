package ble

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeResetScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reset helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSecurityResetSuccess(t *testing.T) {
	if err := RunSecurityReset(context.Background(), writeResetScript(t, "exit 0")); err != nil {
		t.Fatalf("RunSecurityReset() error = %v", err)
	}
}

func TestRunSecurityResetReportsExitAndCapsOutput(t *testing.T) {
	helper := writeResetScript(t, "head -c 8192 /dev/zero | tr '\\000' x; exit 7")
	err := RunSecurityReset(context.Background(), helper)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("RunSecurityReset() error = %v", err)
	}
	if len(err.Error()) > 4200 {
		t.Fatalf("error length = %d, want bounded diagnostics", len(err.Error()))
	}
}

func TestRunSecurityResetDoesNotPassCallerEnvironment(t *testing.T) {
	t.Setenv("WATTLINE_TOKEN", "super-secret-token")
	helper := writeResetScript(t, "printf '%s' \"$WATTLINE_TOKEN\"; exit 1")
	err := RunSecurityReset(context.Background(), helper)
	if err == nil {
		t.Fatal("RunSecurityReset() error = nil")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatal("error leaked caller environment")
	}
}

func TestRunSecurityResetRedactsTokenAndPINOutput(t *testing.T) {
	helper := writeResetScript(t, "printf 'adapter failed\\ntoken=hunter2 PIN: 020555\\n'; exit 1")
	err := RunSecurityReset(context.Background(), helper)
	if err == nil || !strings.Contains(err.Error(), "adapter failed") {
		t.Fatal("error omitted the nonsecret helper diagnostic")
	}
	for _, secret := range []string{"hunter2", "020555"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error leaked a token or PIN value")
		}
	}
}

func TestSafeSecurityResetOutputRedactsCredentialForms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"authorization bearer", "adapter failed: Authorization: Bearer abc", "adapter failed: Authorization: Bearer <redacted>"},
		{"bare bearer", "adapter failed: Bearer abc", "adapter failed: Bearer <redacted>"},
		{"pin code", "adapter failed: PIN code: 020555", "adapter failed: PIN code: <redacted>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if safeSecurityResetOutput(tt.in) != tt.want {
				t.Fatal("sanitized helper output did not match the safe expected diagnostic")
			}
		})
	}
}

func TestRunSecurityResetCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RunSecurityReset(ctx, writeResetScript(t, "exit 0"))
	if !errors.Is(err, context.Canceled) {
		t.Fatal("RunSecurityReset() did not preserve context.Canceled")
	}
	if !strings.Contains(err.Error(), "canceled") || strings.Contains(err.Error(), "timed out") {
		t.Fatal("RunSecurityReset() did not distinguish cancellation from timeout")
	}
}

func TestRunSecurityResetTimesOutAndKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	helper := writeResetScript(t, fmt.Sprintf("sleep 30 &\nprintf '%%s' $! > %q\nwait", pidFile))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := RunSecurityReset(ctx, helper)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunSecurityReset() error = %v, want deadline exceeded", err)
	}
	b, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read child PID: %v", readErr)
	}
	pid, convErr := strconv.Atoi(string(b))
	if convErr != nil {
		t.Fatalf("parse child PID %q: %v", b, convErr)
	}
	deadline := time.Now().Add(time.Second)
	for {
		signalErr := syscall.Kill(pid, 0)
		if errors.Is(signalErr, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper child process %d survived timeout", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
