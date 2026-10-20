package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// The public entry points (runSub/runNode) must hand command-specific flags
// to the subcommand: parseHomeArgs only strips the global flags.
func TestParseHomeArgs(t *testing.T) {
	envHome := t.TempDir()
	t.Setenv("NETIVRI_HOME", envHome)
	cases := []struct {
		name     string
		args     []string
		wantHome string
		wantRest []string
	}{
		{"no flags", []string{"https://x/y"}, envHome, []string{"https://x/y"}},
		{"command flags pass through", []string{"https://x/y", "--name", "n", "--via", "socks5://h:1"}, envHome,
			[]string{"https://x/y", "--name", "n", "--via", "socks5://h:1"}},
		{"home separate", []string{"--home", "/h1", "a"}, "/h1", []string{"a"}},
		{"home equals", []string{"--home=/h2", "a"}, "/h2", []string{"a"}},
		{"home single dash", []string{"-home", "/h3"}, "/h3", nil},
		{"home single dash equals", []string{"-home=/h4"}, "/h4", nil},
		{"globals stripped", []string{"--json", "--quiet", "--no-color", "a", "-json"}, envHome, []string{"a"}},
		{"terminator keeps the rest", []string{"--", "--home", "x"}, envHome, []string{"--", "--home", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, rest, err := parseHomeArgs("sub test", tc.args)
			if err != nil {
				t.Fatalf("parseHomeArgs(%v): %v", tc.args, err)
			}
			if home != tc.wantHome {
				t.Errorf("home = %q, want %q", home, tc.wantHome)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %#v, want %#v", rest, tc.wantRest)
			}
		})
	}
	if _, _, err := parseHomeArgs("sub test", []string{"--home"}); err == nil {
		t.Error("--home without a value: want a usage error")
	} else {
		assertCode(t, err, "USAGE", cli.ExitUsage)
	}
}

func TestRunNodeListPublicFlags(t *testing.T) {
	home := t.TempDir() // no config file: defaults, no Ktav library needed
	var out bytes.Buffer
	if err := runNode([]string{"list", "--country", "de", "--json", "--home", home}, &out); err != nil {
		t.Fatalf("runNode list: %v", err)
	}
	data := decodeData(t, &out)
	if data["count"] != float64(0) {
		t.Errorf("count = %v, want 0", data["count"])
	}

	// --sub reaches the command: an unknown name is SUB_NOT_FOUND, not USAGE.
	err := runNode([]string{"list", "--sub", "nosuch", "--home", home}, &bytes.Buffer{})
	assertCode(t, err, codeSubNotFound, cli.ExitNotFound)

	err = runNode([]string{"list", "--alive", "--home", home}, &bytes.Buffer{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" || !strings.Contains(ce.Message, "--alive is not available yet") {
		t.Errorf("--alive error = %v, want the not-available-yet usage error", err)
	}

	err = runNode([]string{"list", "--country", "ZZ", "--home", home}, &bytes.Buffer{})
	assertCode(t, err, "USAGE", cli.ExitUsage)
}

func TestRunSubListEmpty(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	if err := runSub([]string{"list", "--home", home}, &out); err != nil {
		t.Fatalf("runSub list: %v", err)
	}
	data := decodeData(t, &out)
	subs, ok := data["subscriptions"].([]any)
	if !ok || len(subs) != 0 {
		t.Errorf("subscriptions = %#v, want an empty array", data["subscriptions"])
	}
}

// add -> update -> rm through the public entry point with real flag parsing.
func TestRunSubFlagsEndToEnd(t *testing.T) {
	testutil.RequireKtavLib(t)
	var gotProxies []string
	old := subFetch
	subFetch = func(_ context.Context, _ string, o fetch.Options) (*fetch.Result, error) {
		gotProxies = append(gotProxies, o.Proxy)
		return &fetch.Result{Body: []byte(testBody), Status: 200}, nil
	}
	t.Cleanup(func() { subFetch = old })

	home := t.TempDir()
	var out bytes.Buffer
	err := runSub([]string{"add", "https://p.example/s?token=SECRET", "--name", "mine", "--via", "socks5://127.0.0.1:1", "--home", home}, &out)
	if err != nil {
		t.Fatalf("runSub add: %v", err)
	}
	data := decodeData(t, &out)
	if data["name"] != "mine" || data["added"] != true {
		t.Errorf("add data = %v", data)
	}
	if strings.Contains(out.String(), "SECRET") {
		t.Errorf("add output leaks the URL token: %s", out.String())
	}
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Via != "socks5://127.0.0.1:1" {
		t.Fatalf("subscriptions = %+v, want one with the via proxy", cfg.Subscriptions)
	}

	out.Reset()
	if err := runSub([]string{"update", "--all", "--home", home}, &out); err != nil {
		t.Fatalf("runSub update --all: %v", err)
	}
	if want := []string{"socks5://127.0.0.1:1", "socks5://127.0.0.1:1"}; !reflect.DeepEqual(gotProxies, want) {
		t.Errorf("fetch proxies = %v, want %v (the sub's via both times)", gotProxies, want)
	}

	out.Reset()
	if err := runSub([]string{"rm", "mine", "--home", home}, &out); err != nil {
		t.Fatalf("runSub rm: %v", err)
	}
	if data := decodeData(t, &out); data["removed"] != "mine" {
		t.Errorf("rm data = %v", data)
	}
}

func TestRunSubUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"nosuchcommand"},
		{"add", "--home", t.TempDir()},
		{"rm", "--home", t.TempDir()},
	} {
		err := runSub(args, &bytes.Buffer{})
		assertCode(t, err, "USAGE", cli.ExitUsage)
	}
}
