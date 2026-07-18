//go:build windows

package discovery

import (
	"context"
	"errors"
	"os/exec"
)

const maxCommandOutput = 1 << 20

var ErrCommandOutputTooLarge = errors.New("command output exceeds 1 MiB")

func runBoundedCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, executable, args...).Output()
	if len(output) > maxCommandOutput {
		return append([]byte(nil), output[:maxCommandOutput]...), ErrCommandOutputTooLarge
	}
	return output, err
}
