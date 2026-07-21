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
		t.Fatalf("error leaked caller environment: %v", err)
	}
}

func TestRunSecurityResetRedactsTokenAndPINOutput(t *testing.T) {
	helper := writeResetScript(t, "printf 'adapter failed\\ntoken=hunter2 PIN: 020555\\n'; exit 1")
	err := RunSecurityReset(context.Background(), helper)
	if err == nil || !strings.Contains(err.Error(), "adapter failed") {
		t.Fatalf("error omitted helper diagnostic: %v", err)
	}
	for _, secret := range []string{"hunter2", "020555"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
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
