//go:build windows

package discovery

import (
	"context"
	"os/exec"
)

func runBoundedCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, overflow, readErr := readBoundedOutput(stdout)
	if overflow {
		_ = command.Process.Kill()
		_ = command.Wait()
		return output, ErrCommandOutputTooLarge
	}
	waitErr := command.Wait()
	if readErr != nil {
		return output, readErr
	}
	return output, waitErr
}
