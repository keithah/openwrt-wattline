//go:build !windows

package discovery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const maxCommandOutput = 1 << 20

var ErrCommandOutputTooLarge = errors.New("command output exceeds 1 MiB")

type boundedBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := maxCommandOutput - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return 0, ErrCommandOutputTooLarge
	}
	if len(value) > remaining {
		_, _ = buffer.buffer.Write(value[:remaining])
		buffer.overflow = true
		return remaining, ErrCommandOutputTooLarge
	}
	return buffer.buffer.Write(value)
}

// runBoundedCommand contains the process tree and output of optional helper
// commands. WaitDelay prevents inherited pipes from keeping Wait alive after a
// parent exits, and the final group kill cleans up any descendants.
func runBoundedCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 250 * time.Millisecond
	output := &boundedBuffer{}
	command.Stdout = output
	command.Stderr = nil
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	pid := command.Process.Pid
	err := command.Wait()
	// Wait may have returned because the direct child exited while a descendant
	// retained inherited descriptors. Always tear down the isolated group.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	result := append([]byte(nil), output.buffer.Bytes()...)
	if output.overflow {
		return result, ErrCommandOutputTooLarge
	}
	return result, err
}
