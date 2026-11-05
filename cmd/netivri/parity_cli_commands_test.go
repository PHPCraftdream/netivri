package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func parityCLIErrCode(t *testing.T, err error) string {
	t.Helper()
	var ce *cli.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a *cli.Error, got %v", err)
	}
	return ce.Code
}

func TestSubRenameCommand(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "old", URL: "https://old.invalid/path"}, config.Subscription{Name: "other", URL: "https://other.invalid/p"})
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "old,other"}}
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = runSub([]string{"rename", "old", "new", "--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "new") || strings.Contains(out.String(), "old.invalid") {
		t.Fatalf("output = %s", out.String())
	}
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subscriptions[0].Name != "new" || cfg.Subscriptions[0].URL != "https://old.invalid/path" {
		t.Fatalf("subscriptions = %+v", cfg.Subscriptions)
	}
	if cfg.Accounts[0].Provider != "new,other" {
		t.Fatalf("account provider = %q, want new,other", cfg.Accounts[0].Provider)
	}
	for _, c := range []struct {
		name string
		args []string
		code string
	}{
		{"missing", []string{"rename", "nope", "x", "--home", home}, "SUB_NOT_FOUND"},
		{"name in use", []string{"rename", "new", "other", "--home", home}, "SUB_EXISTS_NAME"},
		{"same name", []string{"rename", "new", "new", "--home", home}, "USAGE"},
		{"bad name", []string{"rename", "new", "a b", "--home", home}, "USAGE"},
		{"one argument", []string{"rename", "new", "--home", home}, "USAGE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parityCLIErrCode(t, runSub(c.args, &out)); got != c.code {
				t.Fatalf("code = %s, want %s", got, c.code)
			}
		})
	}
	after, _ := loadConfig(home)
	if after.Subscriptions[0].Name != "new" || after.Subscriptions[1].Name != "other" {
		t.Fatalf("failed renames changed the config: %+v", after.Subscriptions)
	}
}

func TestSubSetCommand(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid/old-token", Via: "socks5h://127.0.0.1:1080"})
	var out bytes.Buffer
	if err := runSub([]string{"set", "p", "--home", home, "--url", "https://p.invalid/new-token", "--via", "none"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "new-token") || strings.Contains(out.String(), "old-token") {
		t.Fatalf("the URL is a credential and must not be printed: %s", out.String())
	}
	cfg, _ := loadConfig(home)
	if cfg.Subscriptions[0].URL != "https://p.invalid/new-token" || cfg.Subscriptions[0].Via != "" {
		t.Fatalf("saved = %+v", cfg.Subscriptions[0])
	}
	// only --via: the URL stays
	if err := runSub([]string{"set", "p", "--home", home, "--via", "socks5h://127.0.0.1:2080"}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(home)
	if cfg.Subscriptions[0].URL != "https://p.invalid/new-token" || cfg.Subscriptions[0].Via != "socks5h://127.0.0.1:2080" {
		t.Fatalf("saved = %+v", cfg.Subscriptions[0])
	}
	for _, c := range []struct {
		name string
		args []string
		code string
	}{
		{"nothing to set", []string{"set", "p", "--home", home}, "USAGE"},
		{"no name", []string{"set", "--home", home, "--via", "none"}, "USAGE"},
		{"empty url", []string{"set", "p", "--home", home, "--url", ""}, "USAGE"},
		{"empty via", []string{"set", "p", "--home", home, "--via", ""}, "USAGE"},
		{"bad url", []string{"set", "p", "--home", home, "--url", "ftp://p.invalid/x"}, "CONFIG_INVALID"},
		{"missing", []string{"set", "nope", "--home", home, "--via", "none"}, "SUB_NOT_FOUND"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := runSub(c.args, &out)
			if got := parityCLIErrCode(t, err); got != c.code {
				t.Fatalf("code = %s, want %s (%v)", got, c.code, err)
			}
			if strings.Contains(err.Error(), "new-token") {
				t.Fatalf("error echoes the URL: %v", err)
			}
		})
	}
}

func TestServerUpdateCommand(t *testing.T) {
	home := serversFixture(t)
	var out bytes.Buffer
	if err := runServer([]string{"update", "ss", "--home", home, "--port", "3456", "--public-host", "new.example"}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadConfig(home)
	s, _ := cfg.GetServer("ss")
	if s.Port != 3456 || s.PublicHost != "new.example" || s.Bind != "127.0.0.1" || s.Accounts != "alice" {
		t.Fatalf("saved = %+v", s)
	}
	out.Reset()
	if err := runServer([]string{"update", "vless", "--home", home, "--param", "sni=changed.example"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "changed.example") {
		t.Fatalf("parameter values must not be printed: %s", out.String())
	}
	cfg, _ = loadConfig(home)
	v, _ := cfg.GetServer("vless")
	if v.Params["sni"] != "changed.example" || v.Port != 2345 {
		t.Fatalf("saved = %+v", v)
	}
	for _, c := range []struct {
		name string
		args []string
		code string
	}{
		{"nothing to change", []string{"update", "ss", "--home", home}, "USAGE"},
		{"no name", []string{"update", "--home", home, "--port", "1"}, "USAGE"},
		{"bad port", []string{"update", "ss", "--home", home, "--port", "70000"}, "USAGE"},
		{"bad param", []string{"update", "ss", "--home", home, "--param", "novalue"}, "USAGE"},
		{"duplicate param", []string{"update", "ss", "--home", home, "--param", "a=1", "--param", "a=2"}, "USAGE"},
		{"missing", []string{"update", "nope", "--home", home, "--port", "4000"}, "SERVER_NOT_FOUND"},
		{"bad account", []string{"update", "ss", "--home", home, "--account", "bad login"}, "CONFIG_INVALID"},
		{"port taken", []string{"update", "ss", "--home", home, "--port", "2345"}, "PORT_IN_USE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parityCLIErrCode(t, runServer(c.args, &out)); got != c.code {
				t.Fatalf("code = %s, want %s", got, c.code)
			}
		})
	}
	cfg, _ = loadConfig(home)
	s, _ = cfg.GetServer("ss")
	if s.Port != 3456 {
		t.Fatalf("a failed update changed the server: %+v", s)
	}
}
