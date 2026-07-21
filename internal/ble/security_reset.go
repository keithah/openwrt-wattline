package ble

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

const securityResetOutputLimit = 4 * 1024

var securityResetSecret = regexp.MustCompile(`(?i)\b(token|pin)(?:\s*[:=]\s*|\s+)\S+`)

type cappedOutput struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	want := securityResetOutputLimit - w.b.Len()
	if want > len(p) {
		want = len(p)
	}
	if want > 0 {
		_, _ = w.b.Write(p[:want])
	}
	return len(p), nil
}

func (w *cappedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func safeSecurityResetOutput(output string) string {
	output = securityResetSecret.ReplaceAllString(output, "${1}=<redacted>")
	if len(output) > securityResetOutputLimit {
		output = output[:securityResetOutputLimit]
	}
	return strings.TrimSpace(output)
}

// RunSecurityReset directly executes the packaged security-reset helper. The
// helper receives no daemon environment or credentials. Its failure output is
// capped and token/PIN-shaped values are redacted from daemon diagnostics.
func RunSecurityReset(ctx context.Context, helper string) error {
	cmd := exec.CommandContext(ctx, helper)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var output cappedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("security reset helper timed out: %w", ctxErr)
	}
	if diagnostic := safeSecurityResetOutput(output.String()); diagnostic != "" {
		return fmt.Errorf("security reset helper: %w; output: %s", err, diagnostic)
	}
	return fmt.Errorf("security reset helper: %w", err)
}
