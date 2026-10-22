package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func daemonTestState() daemon.State {
	return daemon.State{
		PID:        111,
		Listen:     "127.0.0.1:7890",
		Control:    "127.0.0.1:54321",
		Token:      "tok",
		Foreground: false,
		Auth:       "none",
		Protocols:  []string{"socks5", "socks4", "http"},
		Route:      "direct",
	}
}

// stubDaemons swaps the daemon seams; restore is registered as cleanup.
func stubDaemons(t *testing.T, probe func(context.Context, string) (daemon.State, error), spawn func(context.Context, daemon.SpawnOptions) (daemon.State, error)) *[]daemon.SpawnOptions {
	t.Helper()
	oldProbe, oldLive, oldSpawn := daemonProbe, daemonProbeLive, daemonSpawn
	daemonProbe, daemonProbeLive, daemonSpawn = probe, func(ctx context.Context, home string) (daemon.State, daemon.Status, error) {
		x, err := probe(ctx, home)
		return x, daemon.Status{Daemon: "running", PID: x.PID, Listen: x.Listen, Auth: x.Auth, Foreground: x.Foreground, Protocols: x.Protocols, Route: x.Route, StartedAt: x.StartedAt, Version: x.Version}, err
	}, spawn
	t.Cleanup(func() { daemonProbe, daemonProbeLive, daemonSpawn = oldProbe, oldLive, oldSpawn })
	return &[]daemon.SpawnOptions{}
}

func probeRunning(st daemon.State) func(context.Context, string) (daemon.State, error) {
	return func(context.Context, string) (daemon.State, error) { return st, nil }
}

func probeNotRunning(context.Context, string) (daemon.State, error) {
	return daemon.State{}, daemon.ErrNotRunning
}

func decodeUpData(t *testing.T, out bytes.Buffer) (map[string]any, bool) {
	t.Helper()
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode stdout %q: %v", out.String(), err)
	}
	return env.Data, env.OK
}

func TestRunUpDaemonAlreadyRunningIdentical(t *testing.T) {
	st := daemonTestState()
	var spawns []daemon.SpawnOptions
	_ = stubDaemons(t, probeRunning(st), func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		spawns = append(spawns, daemon.SpawnOptions{})
		return daemon.State{}, errors.New("must not spawn")
	})
	testutil.TempHome(t)
	var out bytes.Buffer
	if err := runUp([]string{"--port", "7890"}, &out); err != nil {
		t.Fatalf("runUp: %v", err)
	}
	if len(spawns) != 0 {
		t.Error("a spawn happened although a daemon was running")
	}
	data, ok := decodeUpData(t, out)
	if !ok {
		t.Fatalf("stdout not ok: %s", out.String())
	}
	if data["already_running"] != true || data["pid"] != float64(st.PID) || data["listen"] != st.Listen {
		t.Errorf("data = %v", data)
	}
}

func TestRunUpDaemonAlreadyRunningDifferentPort(t *testing.T) {
	st := daemonTestState()
	_ = stubDaemons(t, probeRunning(st), func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		return daemon.State{}, errors.New("must not spawn")
	})
	testutil.TempHome(t)
	err := runUp([]string{"--port", "9999"}, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "ALREADY_RUNNING" || ce.Exit != cli.ExitConflict {
		t.Errorf("code/exit = %q/%d, want ALREADY_RUNNING/%d", ce.Code, ce.Exit, cli.ExitConflict)
	}
}

func TestRunUpDaemonAlreadyRunningPortZero(t *testing.T) {
	// Explicit --port 0 means "don't care": idempotent success.
	st := daemonTestState()
	_ = stubDaemons(t, probeRunning(st), func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		return daemon.State{}, errors.New("must not spawn")
	})
	testutil.TempHome(t)
	var out bytes.Buffer
	if err := runUp([]string{"--port", "0"}, &out); err != nil {
		t.Fatalf("runUp: %v", err)
	}
	if data, ok := decodeUpData(t, out); !ok || data["already_running"] != true {
		t.Errorf("data = %v", data)
	}
}

func TestRunUpDaemonAlreadyRunningBindVariants(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"localhost equals loopback", []string{"--bind", "localhost"}, false},
		{"same numeric bind", []string{"--bind", "127.0.0.1"}, false},
		{"different bind", []string{"--bind", "127.0.0.2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := daemonTestState()
			_ = stubDaemons(t, probeRunning(st), func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
				return daemon.State{}, errors.New("must not spawn")
			})
			testutil.TempHome(t)
			err := runUp(tc.args, &bytes.Buffer{})
			if tc.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRunUpDaemonNotRunningSpawns(t *testing.T) {
	home := t.TempDir()
	spawned := daemon.State{}
	_ = stubDaemons(t, probeNotRunning, func(ctx context.Context, o daemon.SpawnOptions) (daemon.State, error) {
		st := daemonTestState()
		spawned = st
		if len(o.Args) < 4 || o.Args[0] != "up" || o.Args[len(o.Args)-1] != home {
			t.Errorf("child args = %v", o.Args)
		}
		return st, nil
	})
	var out bytes.Buffer
	if err := runUpDaemon(upOptions{Home: home}, []string{"--port", "1234", "--json"}, &out); err != nil {
		t.Fatalf("runUpDaemon: %v", err)
	}
	if spawned.PID != 111 {
		t.Errorf("spawn did not report PID %d", spawned.PID)
	}
	data, ok := decodeUpData(t, out)
	if !ok {
		t.Fatalf("stdout not ok: %s", out.String())
	}
	if data["already_running"] != false || data["foreground"] != false {
		t.Errorf("data = %v", data)
	}
}

func TestFilterUpArgs(t *testing.T) {
	got := filterUpArgs([]string{"--port", "1234", "--json", "--quiet", "--no-color", "--home", "/old", "--bind", "::1"}, "/new")
	want := []string{"up", "--port", "1234", "--bind", "::1", "--home", "/new"}
	if len(got) != len(want) {
		t.Fatalf("filterUpArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterUpArgs = %v, want %v", got, want)
		}
	}
	got = filterUpArgs([]string{"--home=/old", "-quiet"}, "/new")
	want = []string{"up", "--home", "/new"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("filterUpArgs = %v, want %v", got, want)
	}
}

func TestConflictsWithRunning(t *testing.T) {
	cases := []struct {
		name   string
		opts   upOptions
		listen string
		want   bool
	}{
		{"no explicit flags", upOptions{}, "127.0.0.1:7890", false},
		{"same port", upOptions{Port: 7890, PortSet: true}, "127.0.0.1:7890", false},
		{"other port", upOptions{Port: 1, PortSet: true}, "127.0.0.1:7890", true},
		{"port zero does not care", upOptions{Port: 0, PortSet: true}, "127.0.0.1:7890", false},
		{"same bind", upOptions{Bind: "127.0.0.1", BindSet: true}, "127.0.0.1:7890", false},
		{"localhost bind", upOptions{Bind: "localhost", BindSet: true}, "127.0.0.1:7890", false},
		{"other bind", upOptions{Bind: "127.0.0.2", BindSet: true}, "127.0.0.1:7890", true},
		{"bad listen", upOptions{Port: 1, PortSet: true}, "garbage", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conflictsWithRunning(tc.opts, tc.listen); got != tc.want {
				t.Errorf("conflictsWithRunning(%+v, %q) = %v, want %v", tc.opts, tc.listen, got, tc.want)
			}
		})
	}
}

func TestRunDownNotRunning(t *testing.T) {
	_ = stubDaemons(t, probeNotRunning, nil)
	testutil.TempHome(t)
	var out bytes.Buffer
	if err := runDown(nil, &out); err != nil {
		t.Fatalf("runDown: %v", err)
	}
	data, ok := decodeUpData(t, out)
	if !ok {
		t.Fatalf("stdout not ok: %s", out.String())
	}
	if data["daemon"] != "stopped" || data["was_running"] != false {
		t.Errorf("data = %v", data)
	}
}

func TestRunDownStopsDaemon(t *testing.T) {
	st := daemonTestState()
	stopped := make(chan struct{}, 1)
	_ = stubDaemons(t, probeRunning(st), nil)
	oldStop := daemonControlStop
	probeCalls := 0
	daemonControlStop = func(ctx context.Context, addr, token string) error {
		if addr != st.Control || token != st.Token {
			t.Errorf("stop(addr=%s, token len %d)", addr, len(token))
		}
		stopped <- struct{}{}
		return nil
	}
	// First probe sees the daemon, later probes see it gone.
	daemonProbe = func(context.Context, string) (daemon.State, error) {
		probeCalls++
		if probeCalls == 1 {
			return st, nil
		}
		return daemon.State{}, daemon.ErrNotRunning
	}
	t.Cleanup(func() { daemonControlStop = oldStop })
	testutil.TempHome(t)
	var out bytes.Buffer
	if err := runDown(nil, &out); err != nil {
		t.Fatalf("runDown: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop callback not called")
	}
	data, ok := decodeUpData(t, out)
	if !ok || data["was_running"] != true || data["pid"] != float64(st.PID) {
		t.Errorf("data = %v (ok=%v)", data, ok)
	}
}

func TestRunDownStopRequestFails(t *testing.T) {
	st := daemonTestState()
	_ = stubDaemons(t, probeRunning(st), nil)
	oldStop := daemonControlStop
	daemonControlStop = func(context.Context, string, string) error { return errors.New("boom") }
	t.Cleanup(func() { daemonControlStop = oldStop })
	testutil.TempHome(t)
	err := runDown(nil, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "INTERNAL" {
		t.Fatalf("err = %v, want INTERNAL", err)
	}
}

func TestRunDownTimeout(t *testing.T) {
	st := daemonTestState()
	oldWait := downStopWait
	downStopWait = 50 * time.Millisecond
	t.Cleanup(func() { downStopWait = oldWait })
	_ = stubDaemons(t, probeRunning(st), nil)
	oldStop := daemonControlStop
	daemonControlStop = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { daemonControlStop = oldStop })
	testutil.TempHome(t)
	err := runDown(nil, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "DAEMON_STOP_TIMEOUT" || ce.Exit != cli.ExitInternal {
		t.Errorf("code/exit = %q/%d, want DAEMON_STOP_TIMEOUT/%d", ce.Code, ce.Exit, cli.ExitInternal)
	}
}

func TestRunDownUnexpectedArg(t *testing.T) {
	_ = stubDaemons(t, probeNotRunning, nil)
	testutil.TempHome(t)
	err := runDown([]string{"extra"}, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "USAGE" {
		t.Fatalf("err = %v, want USAGE", err)
	}
}

func TestRunStatusNotRunning(t *testing.T) {
	_ = stubDaemons(t, probeNotRunning, nil)
	testutil.TempHome(t)
	err := runStatus(nil, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "DAEMON_NOT_RUNNING" || ce.Exit != cli.ExitNoDaemon {
		t.Errorf("code/exit = %q/%d, want DAEMON_NOT_RUNNING/%d", ce.Code, ce.Exit, cli.ExitNoDaemon)
	}
	if ce.Hint == "" {
		t.Error("empty hint")
	}
	var out bytes.Buffer
	_ = runStatus(nil, &out)
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
}

func TestRunStatusRunning(t *testing.T) {
	st := daemonTestState()
	st.StartedAt = time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339)
	st.Version = "1.2.3"
	_ = stubDaemons(t, probeRunning(st), nil)
	testutil.TempHome(t)
	var out bytes.Buffer
	if err := runStatus(nil, &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	data, ok := decodeUpData(t, out)
	if !ok {
		t.Fatalf("stdout not ok: %s", out.String())
	}
	want := map[string]any{
		"daemon":     "running",
		"foreground": false,
		"pid":        float64(st.PID),
		"listen":     st.Listen,
		"auth":       "none",
		"route":      "direct",
		"version":    "1.2.3",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("%s = %v, want %v", k, data[k], v)
		}
	}
	up, ok := data["uptime_seconds"].(float64)
	if !ok || up < 80 {
		t.Errorf("uptime_seconds = %v, want >= 80", data["uptime_seconds"])
	}
	if !reflect.DeepEqual(data["protocols"], []any{"socks5", "socks4", "http"}) {
		t.Errorf("protocols=%v", data["protocols"])
	}
	if _, present := data["token"]; present {
		t.Error("status output leaks the control token")
	}
}

func TestRunStatusUnexpectedArg(t *testing.T) {
	_ = stubDaemons(t, probeNotRunning, nil)
	testutil.TempHome(t)
	err := runStatus([]string{"--wat"}, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "USAGE" {
		t.Fatalf("err = %v, want USAGE", err)
	}
}

func TestUptimeSeconds(t *testing.T) {
	now := time.Now().UTC()
	if got := uptimeSeconds(now.Add(-90 * time.Second).Format(time.RFC3339)); got != 90 {
		t.Errorf("uptime = %d, want 90", got)
	}
	if got := uptimeSeconds("not-a-time"); got != 0 {
		t.Errorf("uptime(bad input) = %d, want 0", got)
	}
	if got := uptimeSeconds(now.Add(time.Hour).Format(time.RFC3339)); got != 0 {
		t.Errorf("uptime(future) = %d, want 0", got)
	}
}

func TestForegroundFailureWritesHandshake(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.json")
	calls := 0
	old := runForegroundInner
	runForegroundInner = func(upOptions, io.Writer, string, int, *config.Config) error {
		calls++
		return errors.New("startup failure")
	}
	t.Cleanup(func() { runForegroundInner = old })
	err := runUpForeground(upOptions{Handshake: path}, &bytes.Buffer{}, "127.0.0.1", 0, config.Default())
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	var h struct {
		OK    bool            `json:"ok"`
		Error json.RawMessage `json:"error"`
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatal(err)
	}
	var herr struct {
		Code    string
		Message string
		Exit    int
	}
	if err := json.Unmarshal(h.Error, &herr); err != nil {
		t.Fatalf("decode handshake error: %v", err)
	}
	if h.OK || herr.Code != "INTERNAL" || herr.Message != "startup failure" || herr.Exit != cli.ExitInternal {
		t.Errorf("handshake error = %+v, ok=%v", herr, h.OK)
	}
}

func TestAlreadyRunningDataShape(t *testing.T) {
	st := daemonTestState()
	data := alreadyRunningData(daemon.Status{Daemon: "running", PID: st.PID, Listen: st.Listen, Protocols: st.Protocols, Auth: st.Auth, Foreground: st.Foreground}, true)
	for _, k := range []string{"daemon", "pid", "listen", "protocols", "auth", "foreground", "already_running"} {
		if _, ok := data[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if _, ok := data["token"]; ok {
		t.Error("token leaked in up output")
	}
}

func TestRunUpDaemonUsesLiveStatus(t *testing.T) {
	home := testutil.TempHome(t)
	disk := daemonTestState()
	disk.Listen = "127.0.0.1:7890"
	live := daemon.Status{Daemon: "running", PID: 222, Listen: "127.0.0.1:7891", Auth: "enabled", Protocols: []string{"http"}, Foreground: true, Route: "proxy", StartedAt: time.Now().UTC().Format(time.RFC3339), Version: "live-version"}
	oldProbe, oldLive, oldSpawn := daemonProbe, daemonProbeLive, daemonSpawn
	daemonProbe = func(context.Context, string) (daemon.State, error) { return disk, nil }
	daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) { return disk, live, nil }
	daemonSpawn = func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		t.Fatal("unexpected spawn")
		return daemon.State{}, nil
	}
	t.Cleanup(func() { daemonProbe, daemonProbeLive, daemonSpawn = oldProbe, oldLive, oldSpawn })
	var out bytes.Buffer
	if err := runUpDaemon(upOptions{Home: home}, nil, &out); err != nil {
		t.Fatal(err)
	}
	data, ok := decodeUpData(t, out)
	if !ok || data["pid"] != float64(live.PID) || data["listen"] != live.Listen || data["auth"] != live.Auth {
		t.Errorf("up data=%v, ok=%v", data, ok)
	}

	var status bytes.Buffer
	if err := runStatus([]string{"--home", home}, &status); err != nil {
		t.Fatalf("status: %v", err)
	}
	statusData, ok := decodeUpData(t, status)
	if !ok || statusData["pid"] != float64(live.PID) || statusData["listen"] != live.Listen || statusData["auth"] != live.Auth || statusData["version"] != live.Version {
		t.Errorf("status data=%v, ok=%v", statusData, ok)
	}
}

func TestRunUpDaemonLiveStatusConflictUsesLiveListen(t *testing.T) {
	home := testutil.TempHome(t)
	disk := daemonTestState()
	disk.Listen = "127.0.0.1:7890"
	live := daemon.Status{Daemon: "running", PID: 222, Listen: "127.0.0.1:7891", Auth: "enabled"}
	oldProbe, oldLive, oldSpawn := daemonProbe, daemonProbeLive, daemonSpawn
	daemonProbe = func(context.Context, string) (daemon.State, error) { return disk, nil }
	daemonProbeLive = func(context.Context, string) (daemon.State, daemon.Status, error) { return disk, live, nil }
	daemonSpawn = func(context.Context, daemon.SpawnOptions) (daemon.State, error) {
		t.Fatal("unexpected spawn")
		return daemon.State{}, nil
	}
	t.Cleanup(func() { daemonProbe, daemonProbeLive, daemonSpawn = oldProbe, oldLive, oldSpawn })
	err := runUp([]string{"--home", home, "--port", "7890"}, &bytes.Buffer{})
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "ALREADY_RUNNING" || ce.Exit != cli.ExitConflict {
		t.Fatalf("error = %v, want live-listen conflict", err)
	}
}

func TestLiveAuthPersistence(t *testing.T) {
	temp := t.TempDir()
	cfg := config.Default()
	cfg.Auth = config.Auth{Login: config.InitPlaceholder, Password: config.InitPlaceholder}
	persist := newPersist(config.ConfigPath(temp), cfg)
	if got := liveAuth(cfg); got != "init-pending" {
		t.Fatalf("initial liveAuth = %q", got)
	}
	hash, err := auth.Hash("claim-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := persist("alice", hash); err != nil {
		t.Fatal(err)
	}
	if got := liveAuth(cfg); got != "enabled" {
		t.Errorf("liveAuth after persist = %q", got)
	}
	reloaded, err := config.Load(config.ConfigPath(temp))
	if err != nil {
		t.Fatal(err)
	}
	if got := liveAuth(reloaded); got != "enabled" {
		t.Errorf("liveAuth from disk = %q, want enabled", got)
	}
}

func TestHandshakeFlagOnlyWithForeground(t *testing.T) {
	testutil.TempHome(t)
	if _, err := parseUp([]string{"--foreground", "--handshake", filepath.Join(t.TempDir(), "hs.json")}, func(string) string { return "" }, func() (string, error) { return t.TempDir(), nil }); err != nil {
		t.Errorf("handshake with foreground rejected: %v", err)
	}
}
