package toolprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const gracefulStopWait = 10 * time.Second

// CommandContext asks child tools to stop on cancellation before force-killing them.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := cmd.Process.Signal(os.Interrupt)
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = gracefulStopWait
	return cmd
}
