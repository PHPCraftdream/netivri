package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestAccountDirectMutationValidationIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"country", []string{"set-country", "bob,alice", "DE"}},
		{"provider", []string{"set-provider", "bob,alice", "provider"}},
		{"pre", []string{"set-socks5", "bob,alice", "--pre", "socks5://localhost:1080"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			store := subscription.NewStore(filepath.Join(home, "subscriptions"))
			if err := store.Save("provider", subscription.Snapshot{Nodes: []subscription.Node{{ID: "de-node", Name: "de-node", Type: "socks5", Server: "127.0.0.1", Port: 1080, Country: "DE", Raw: map[string]any{"type": "socks5", "server": "127.0.0.1", "port": 1080}}}}); err != nil {
				t.Fatal(err)
			}
			seed := config.Default()
			seed.Subscriptions = []config.Subscription{{Name: "provider", URL: "https://example.invalid/sub"}}
			seed.Accounts = []config.Account{
				{Login: "alice", Password: config.InitPlaceholder, Exit: "direct"},
				{Login: "bob", Password: config.InitPlaceholder},
			}
			path := config.ConfigPath(home)
			if err := config.Save(path, seed); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			args := append(append([]string(nil), tc.args...), "--home", home, "--json")
			var out bytes.Buffer
			err = runAccount(args, &out)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != config.CodeConfigInvalid {
				t.Fatalf("error = %v, output=%s; want CONFIG_INVALID", err, out.String())
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("failed batch changed config on disk\nbefore: %s\nafter: %s", before, after)
			}
			out.Reset()
			if err := runAccount([]string{"set-exit", "alice", "route", "--home", home, "--json"}, &out); err != nil {
				t.Fatalf("repair exit: %v", err)
			}
			out.Reset()
			if err := runAccount(args, &out); err != nil {
				t.Fatalf("retry after route repair: %v", err)
			}
			got, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, login := range []string{"alice", "bob"} {
				a, _ := got.Account(login)
				if a == nil {
					t.Fatalf("%s missing after successful retry", login)
				}
				switch tc.name {
				case "country":
					if a.ExitCountry != "DE" {
						t.Fatalf("%s country = %q", login, a.ExitCountry)
					}
				case "provider":
					if a.Provider != "provider" {
						t.Fatalf("%s provider = %q", login, a.Provider)
					}
				case "pre":
					if a.PreSOCKS5 != "socks5://localhost:1080" {
						t.Fatalf("%s pre_socks5 = %q", login, a.PreSOCKS5)
					}
				}
			}
		})
	}
}

func TestAccountDirectAllowsPostAndClients(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	for _, args := range [][]string{
		{"add", "alice", "--exit", "direct"},
		{"add", "bob"},
		{"set-socks5", "alice", "--post", "socks5://localhost:1080"},
		{"set-clients", "alice", "127.0.0.1"},
	} {
		out.Reset()
		if err := runAccount(append(args, "--home", home, "--json"), &out); err != nil {
			t.Fatalf("account %v: %v", args, err)
		}
	}
	got, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := got.Account("alice")
	if a == nil || a.Exit != "direct" || a.PostSOCKS5 != "socks5://localhost:1080" || a.AllowedClients != "127.0.0.1" {
		t.Fatalf("direct account after post/client changes = %+v", a)
	}
}

func TestAccountAddIdempotencyNormalizesRoute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		first    []string
		repeat   []string
		setRoute bool
	}{
		{"empty-empty", nil, nil, false},
		{"empty-route", nil, []string{"--exit", "route"}, false},
		{"route-empty", []string{"--exit", "route"}, nil, false},
		{"route-route", []string{"--exit", "route"}, []string{"--exit", "route"}, false},
		{"after-set-exit-route", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var out bytes.Buffer
			first := append([]string{"add", "alice"}, tc.first...)
			if err := runAccount(append(first, "--home", home, "--json"), &out); err != nil {
				t.Fatal(err)
			}
			if tc.setRoute {
				out.Reset()
				if err := runAccount([]string{"set-exit", "alice", "route", "--home", home, "--json"}, &out); err != nil {
					t.Fatal(err)
				}
			}
			repeat := append([]string{"add", "alice"}, tc.repeat...)
			out.Reset()
			if err := runAccount(append(repeat, "--home", home, "--json"), &out); err != nil {
				t.Fatalf("repeat add: %v", err)
			}
			data := exitOutData(t, out.Bytes())
			if data["existing"] != true {
				t.Fatalf("repeat output: %s", out.String())
			}
		})
	}
}
