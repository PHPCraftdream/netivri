package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// errOutdated is what angel.ProbeLive returns for an angel of another build.
var errOutdated = fmt.Errorf("%w (pid 111, version 0.0.0-old); restart it: netivri down, then netivri up (decode status answer: bad)", angel.ErrStatusShape)

// stubOutdatedAngel makes angelProbeLive answer like an angel of another
// build: the header decodes, the rest does not.
func stubOutdatedAngel(t *testing.T, listen string) {
	t.Helper()
	old := angelProbeLive
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		st := angelTestState()
		return st, angel.Status{
			Angel: "running", PID: st.PID, Listen: listen, Auth: st.Auth,
			Protocols: st.Protocols, Version: "0.0.0-old", StartedAt: "2026-11-01T10:00:00Z",
		}, errOutdated
	}
	t.Cleanup(func() { angelProbeLive = old })
}

func TestRunUpAngelAnotherBuildIsAlreadyRunning(t *testing.T) {
	_ = stubAngels(t, probeNotRunning, func(context.Context, angel.SpawnOptions) (angel.State, error) {
		t.Error("up spawned a second angel next to one of another build")
		return angel.State{}, errors.New("must not spawn")
	})
	stubOutdatedAngel(t, "127.0.0.1:7890")
	var out bytes.Buffer
	if err := runUpAngel(upOptions{Home: t.TempDir()}, []string{"--json"}, &out); err != nil {
		t.Fatalf("runUpAngel: %v", err)
	}
	data, ok := decodeUpData(t, out)
	if !ok || data["already_running"] != true || data["pid"] != float64(111) {
		t.Fatalf("up data = %v (ok %v), want already_running for pid 111", data, ok)
	}
}

func TestRunUpAngelAnotherBuildWithOtherListenConflicts(t *testing.T) {
	_ = stubAngels(t, probeNotRunning, func(context.Context, angel.SpawnOptions) (angel.State, error) {
		t.Error("up spawned a second angel next to one of another build")
		return angel.State{}, errors.New("must not spawn")
	})
	stubOutdatedAngel(t, "127.0.0.1:1")
	err := runUpAngel(upOptions{Home: t.TempDir(), Port: 7890, PortSet: true}, []string{"--json"}, &bytes.Buffer{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ALREADY_RUNNING" {
		t.Fatalf("runUpAngel err = %v, want ALREADY_RUNNING", err)
	}
}

func TestRunStatusAnotherBuildIsOutdated(t *testing.T) {
	testutil.TempHome(t)
	stubOutdatedAngel(t, "127.0.0.1:7890")
	var out bytes.Buffer
	err := runStatus(nil, &out)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "ANGEL_OUTDATED" || ce.Exit != cli.ExitInternal || ce.Exit == cli.ExitNoAngel {
		t.Fatalf("runStatus err = %v, want ANGEL_OUTDATED", err)
	}
	for _, part := range []string{"pid 111", "version 0.0.0-old", "netivri down, then netivri up"} {
		if !strings.Contains(ce.Message, part) {
			t.Errorf("message %q lacks %q", ce.Message, part)
		}
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
}

func TestTUIHeaderAnotherBuildRuns(t *testing.T) {
	stubOutdatedAngel(t, "127.0.0.1:7890")
	h, err := (tuiHeader{home: "{DIR}"}).Header()
	if err != nil || !h.Running || h.Listen != "127.0.0.1:7890" || h.Version != "0.0.0-old" {
		t.Fatalf("header = %+v, %v; want running with the other build's version", h, err)
	}
}

func TestDoctorAnotherBuildFailsAngelCheck(t *testing.T) {
	home := testutil.TempHome(t)
	doctorFixTime(t)
	doctorConfig(t, home, nil)
	doctorStubListen(t, errors.New("taken"))
	stubOutdatedAngel(t, "127.0.0.1:7890")
	checks := doctorRun(home)
	byName := map[string]doctorCheck{}
	for _, c := range checks {
		if _, seen := byName[c.Name]; !seen {
			byName[c.Name] = c
		}
	}
	a := byName["angel"]
	if a.Status != doctorFail || a.Detail != errOutdated.Error() {
		t.Errorf("angel check = %+v, want fail with the probe error", a)
	}
	l := byName["listen"]
	if l.Status != doctorWarn || !strings.Contains(l.Detail, "another netivri build") {
		t.Errorf("listen check = %+v, want a not-checked warn", l)
	}
}
