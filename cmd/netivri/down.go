package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
)

var (
	// downStopWait bounds how long `down` waits for the angel to disappear
	// after the stop request was accepted.
	downStopWait = 15 * time.Second
	// downPollInterval is how often `down` re-probes during that wait.
	downPollInterval = 100 * time.Millisecond
)

// runDown stops the angel idempotently. Only the control channel stops it:
// there is no kill by pid.
func runDown(args []string, stdout io.Writer) error {
	home, rest, err := parseHomeArgs("down", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return cli.Usage("down: unexpected argument %q", rest[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), downStopWait+angel.ProbeTimeout)
	defer cancel()

	st, err := angelProbe(ctx, home)
	if errors.Is(err, angel.ErrControlTimeout) {
		return angelNotResponding()
	}
	if errors.Is(err, angel.ErrNotRunning) {
		cli.Success(stdout, map[string]any{"angel": "stopped", "was_running": false})
		return nil
	}
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	if err := angelControlStop(ctx, st.Control, st.Token); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "stop request failed: " + err.Error(), Exit: cli.ExitInternal}
	}

	timeout := &cli.Error{
		Code:    "ANGEL_STOP_TIMEOUT",
		Message: "angel did not stop within " + downStopWait.String(),
		Exit:    cli.ExitInternal,
	}
	deadline := time.Now().Add(downStopWait)
	for {
		if _, err := angelProbe(ctx, home); errors.Is(err, angel.ErrNotRunning) {
			cli.Success(stdout, map[string]any{"angel": "stopped", "was_running": true, "pid": st.PID})
			return nil
		}
		if time.Now().After(deadline) {
			return timeout
		}
		select {
		case <-ctx.Done():
			return timeout
		case <-time.After(downPollInterval):
		}
	}
}
