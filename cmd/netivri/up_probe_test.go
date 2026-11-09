package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/core"
)

func TestServeControlTimeoutDoesNotStartCore(t *testing.T) {
	oldProbe, oldCore := angelProbe, coreStart
	angelProbe = func(context.Context, string) (angel.State, error) {
		return angel.State{}, errors.Join(angel.ErrControlTimeout, errors.New("deadline"))
	}
	started := false
	coreStart = func(context.Context, core.Options) (coreInstance, error) {
		started = true
		return nil, errors.New("unexpected core start")
	}
	t.Cleanup(func() { angelProbe, coreStart = oldProbe, oldCore })

	err := serve(upOptions{Home: t.TempDir()}, &bytes.Buffer{}, "127.0.0.1", 7890, config.Default())
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_NOT_RESPONDING" {
		t.Fatalf("serve error = %v, want ANGEL_NOT_RESPONDING", err)
	}
	if started {
		t.Fatal("serve started a core after a control timeout")
	}
}

func TestServeControlFailureDoesNotStartCore(t *testing.T) {
	probeErr := errors.New("decode status answer: malformed response")
	oldProbe, oldCore := angelProbe, coreStart
	angelProbe = func(context.Context, string) (angel.State, error) {
		return angel.State{}, probeErr
	}
	started := false
	coreStart = func(context.Context, core.Options) (coreInstance, error) {
		started = true
		return nil, errors.New("unexpected core start")
	}
	t.Cleanup(func() { angelProbe, coreStart = oldProbe, oldCore })

	err := serve(upOptions{Home: t.TempDir()}, &bytes.Buffer{}, "127.0.0.1", 7890, config.Default())
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || ce.Exit != cli.ExitInternal || ce.Message != probeErr.Error() {
		t.Fatalf("serve error = %v, want INTERNAL with the probe error", err)
	}
	if started {
		t.Fatal("serve started a core after an inconclusive control failure")
	}
}

func TestRunUpAngelControlFailureDoesNotSpawn(t *testing.T) {
	probeErr := errors.New("decode status answer: malformed response")
	spawns := 0
	_ = stubAngels(t, func(context.Context, string) (angel.State, error) {
		return angel.State{}, probeErr
	}, func(context.Context, angel.SpawnOptions) (angel.State, error) {
		spawns++
		return angel.State{}, errors.New("unexpected spawn")
	})
	var out bytes.Buffer
	err := runUpAngel(upOptions{Home: t.TempDir()}, []string{"--json"}, &out)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || ce.Exit != cli.ExitInternal || ce.Message != probeErr.Error() {
		t.Fatalf("runUpAngel error = %v, want INTERNAL with the probe error", err)
	}
	if spawns != 0 || out.Len() != 0 {
		t.Fatalf("spawns=%d stdout=%q, want no spawn or output", spawns, out.String())
	}
}
