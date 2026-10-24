package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// DefaultSpawnTimeout bounds how long Spawn waits for the handshake.
const DefaultSpawnTimeout = 15 * time.Second

// spawnPollInterval is how often the handshake file is checked.
const spawnPollInterval = 50 * time.Millisecond

// SpawnOptions configures Spawn.
type SpawnOptions struct {
	Exe     string        // required: the netivri executable
	Args    []string      // the child's command line after the executable; Spawn appends the handshake flags
	Home    string        // required: state directory
	Timeout time.Duration // default DefaultSpawnTimeout
}

// awaitHandshake outcomes; Spawn maps them to cli errors.
var (
	errSpawnTimeout   = errors.New("daemon start timed out")
	errSpawnCancelled = errors.New("daemon start cancelled")
	errSpawnExited    = errors.New("daemon exited before reporting startup")
)

// randomHex returns n random bytes as hex.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handshakeArgs appends the internal handshake flags. Pure, so the argument
// building is tested without spawning anything.
func handshakeArgs(args []string, path string) []string {
	out := make([]string, 0, len(args)+3)
	out = append(out, args...)
	return append(out, "--foreground", "--handshake", path)
}

// awaitHandshake polls read until it yields a handshake, the child exits, the
// context ends, or the timeout hits. After the child exited the handshake is
// read once more: one written just before the exit must not be lost.
// Pure channel and callback logic, unit-testable without spawning anything.
func awaitHandshake(ctx context.Context, read func() (handshake, error), waitCh <-chan error, timeout time.Duration) (State, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(spawnPollInterval)
	defer ticker.Stop()
	for {
		if h, err := read(); err == nil {
			return handshakeResult(h)
		}
		select {
		case <-waitCh:
			if h, err := read(); err == nil {
				return handshakeResult(h)
			}
			return State{}, errSpawnExited
		case <-ctx.Done():
			return State{}, errSpawnCancelled
		case <-deadline.C:
			return State{}, errSpawnTimeout
		case <-ticker.C:
		}
	}
}

// Spawn starts the detached daemon child and waits for its handshake:
// (State, nil) on success, (zero, *cli.Error) on a reported failure, early
// exit or timeout. Only the child started here is ever killed.
func Spawn(ctx context.Context, o SpawnOptions) (State, error) {
	var zero State
	if o.Exe == "" || o.Home == "" {
		return zero, &cli.Error{Code: "INTERNAL", Message: "spawn: Exe and Home are required", Exit: cli.ExitInternal}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultSpawnTimeout
	}
	if err := os.MkdirAll(RunDir(o.Home), 0o700); err != nil {
		return zero, &cli.Error{Code: "INTERNAL", Message: "create run directory: " + err.Error(), Exit: cli.ExitInternal}
	}
	suffix, err := randomHex(8)
	if err != nil {
		return zero, &cli.Error{Code: "INTERNAL", Message: "generate handshake name: " + err.Error(), Exit: cli.ExitInternal}
	}
	handshakePath := filepath.Join(RunDir(o.Home), "start-"+suffix+".json")
	defer os.Remove(handshakePath)

	_ = RotateLog(o.Home, MaxLogBytes) // best effort: a log kept whole is only larger
	log, err := os.OpenFile(LogPath(o.Home), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return zero, &cli.Error{Code: "INTERNAL", Message: "open daemon log: " + err.Error(), Exit: cli.ExitInternal}
	}
	defer log.Close()

	cmd, err := startChild(o.Exe, handshakeArgs(o.Args, handshakePath), log, log)
	if err != nil {
		return zero, &cli.Error{Code: "DAEMON_START_FAILED", Message: "start daemon: " + err.Error(), Exit: cli.ExitInternal}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	read := func() (handshake, error) { return readHandshake(handshakePath) }
	st, err := awaitHandshake(ctx, read, waitCh, timeout)
	switch {
	case err == nil:
		return st, nil
	case errors.Is(err, errSpawnTimeout):
		_ = cmd.Process.Kill()
		return zero, &cli.Error{
			Code:    "DAEMON_START_TIMEOUT",
			Message: "daemon did not report startup within " + timeout.String(),
			Hint:    "see run/netivri.log in the state directory",
			Exit:    cli.ExitInternal,
		}
	case errors.Is(err, errSpawnCancelled):
		_ = cmd.Process.Kill()
		return zero, &cli.Error{Code: "DAEMON_START_FAILED", Message: "startup cancelled: " + ctx.Err().Error(), Exit: cli.ExitInternal}
	case errors.Is(err, errSpawnExited):
		return zero, &cli.Error{
			Code:    "DAEMON_START_FAILED",
			Message: "daemon exited before reporting startup",
			Hint:    "see run/netivri.log in the state directory",
			Exit:    cli.ExitInternal,
		}
	default:
		return zero, err // a failure the child reported itself: same code and exit
	}
}
