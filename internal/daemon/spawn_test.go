package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestHandshakeArgs(t *testing.T) {
	got := handshakeArgs([]string{"up", "--port", "0", "--home", "h"}, "hs.json")
	want := []string{"up", "--port", "0", "--home", "h", "--foreground", "--handshake", "hs.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestRandomHex(t *testing.T) {
	a, err := randomHex(8)
	if err != nil {
		t.Fatalf("randomHex: %v", err)
	}
	if len(a) != 16 {
		t.Errorf("len = %d, want 16 hex chars", len(a))
	}
	b, _ := randomHex(8)
	if a == b {
		t.Error("two randomHex calls returned the same value")
	}
}

func TestSpawnValidation(t *testing.T) {
	if _, err := Spawn(context.Background(), SpawnOptions{Home: t.TempDir()}); err == nil {
		t.Error("Spawn without Exe accepted")
	}
	if _, err := Spawn(context.Background(), SpawnOptions{Exe: "netivri"}); err == nil {
		t.Error("Spawn without Home accepted")
	}
}

// fileReader reads the handshake file at path, like Spawn does.
func fileReader(path string) func() (handshake, error) {
	return func() (handshake, error) { return readHandshake(path) }
}

// exited returns a waitCh on which the child already exited.
func exited() chan error {
	ch := make(chan error, 1)
	ch <- errors.New("exit status 1")
	return ch
}

func TestAwaitHandshakeSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs.json")
	if err := WriteHandshake(path, testState()); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	st, err := awaitHandshake(context.Background(), fileReader(path), make(chan error, 1), 5*time.Second)
	if err != nil {
		t.Fatalf("awaitHandshake: %v", err)
	}
	if st.PID != testState().PID {
		t.Errorf("pid = %d, want %d", st.PID, testState().PID)
	}
}

func TestAwaitHandshakeErrorKeepsCodeAndExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hs.json")
	orig := &cli.Error{Code: "PORT_IN_USE", Message: "port is already in use", Exit: cli.ExitConflict}
	if err := WriteHandshakeError(path, NewHandshakeError(orig)); err != nil {
		t.Fatalf("WriteHandshakeError: %v", err)
	}
	_, err := awaitHandshake(context.Background(), fileReader(path), make(chan error, 1), 5*time.Second)
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != orig.Code || ce.Exit != orig.Exit {
		t.Errorf("code/exit = %q/%d, want %q/%d", ce.Code, ce.Exit, orig.Code, orig.Exit)
	}
}

// A handshake that appears after the first read but before the child exit
// is observed must win over "exited without a handshake".
func TestAwaitHandshakeRereadsAfterExit(t *testing.T) {
	calls := 0
	read := func() (handshake, error) {
		calls++
		if calls == 1 {
			return handshake{}, errors.New("not written yet")
		}
		st := testState()
		return handshake{OK: true, State: &st}, nil
	}
	st, err := awaitHandshake(context.Background(), read, exited(), 5*time.Second)
	if err != nil {
		t.Fatalf("awaitHandshake: %v", err)
	}
	if st.PID != testState().PID || calls != 2 {
		t.Errorf("pid = %d after %d reads, want %d after 2", st.PID, calls, testState().PID)
	}
}

func TestAwaitHandshakeRereadsErrorAfterExit(t *testing.T) {
	calls := 0
	read := func() (handshake, error) {
		calls++
		if calls == 1 {
			return handshake{}, errors.New("not written yet")
		}
		return handshake{Error: NewHandshakeError(&cli.Error{Code: "PORT_IN_USE", Message: "busy", Exit: cli.ExitConflict})}, nil
	}
	_, err := awaitHandshake(context.Background(), read, exited(), 5*time.Second)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" || ce.Exit != cli.ExitConflict {
		t.Errorf("err = %v, want the reported PORT_IN_USE", err)
	}
}

func TestAwaitHandshakeExitedWithoutHandshake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, err := awaitHandshake(context.Background(), fileReader(path), exited(), 5*time.Second)
	if !errors.Is(err, errSpawnExited) {
		t.Errorf("err = %v, want errSpawnExited", err)
	}
}

func TestAwaitHandshakeTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, err := awaitHandshake(context.Background(), fileReader(path), make(chan error, 1), 2*spawnPollInterval)
	if !errors.Is(err, errSpawnTimeout) {
		t.Errorf("err = %v, want errSpawnTimeout", err)
	}
}

func TestAwaitHandshakeCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := awaitHandshake(ctx, fileReader(path), make(chan error, 1), 5*time.Second)
	if !errors.Is(err, errSpawnCancelled) {
		t.Errorf("err = %v, want errSpawnCancelled", err)
	}
}
