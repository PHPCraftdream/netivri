package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/ktav-lang/golang/pure"
)

func TestAccountSetExitValidationAndAtomicity(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "alice", Exit: "route"}, {Login: "bob", Provider: "p"}}}
	saved := false
	err := accountSetExit(t.TempDir(), cfg, []string{"alice,bob", "direct"}, func() error { saved = true; return nil }, &bytes.Buffer{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != config.CodeConfigInvalid || !strings.Contains(ce.Message, "accounts[1].exit") || saved || cfg.Accounts[0].Exit != "route" {
		t.Fatalf("err=%v saved=%v accounts=%+v", err, saved, cfg.Accounts)
	}
}

func TestAccountSetExitAddressingHintsAndIdempotency(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	saveCount := 0
	save := func() error { saveCount++; return nil }
	var out bytes.Buffer
	if err := accountSetExit(t.TempDir(), cfg, []string{"alice", "direct"}, save, &out); err != nil {
		t.Fatal(err)
	}
	data := exitOutData(t, out.Bytes())
	if _, ok := data["account"]; !ok {
		t.Fatalf("single output shape: %s", out.String())
	}
	if exitOutData(t, out.Bytes())["hint"] != directExitHint {
		t.Fatalf("missing hint: %s", out.String())
	}
	out.Reset()
	if err := accountSetExit(t.TempDir(), cfg, []string{"alice,alice", "direct"}, save, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := exitOutData(t, out.Bytes())["accounts"]; !ok {
		t.Fatalf("deduped multi output shape: %s", out.String())
	}
	out.Reset()
	if err := accountSetExit(t.TempDir(), cfg, []string{"--all", "direct"}, save, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := exitOutData(t, out.Bytes())["accounts"]; !ok {
		t.Fatalf("--all output shape: %s", out.String())
	}
	if saveCount != 3 {
		t.Fatalf("save count=%d", saveCount)
	}
}

func exitOutData(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(b, &envelope) == nil {
		return envelope.Data
	}
	parsed, err := pure.Loads(string(b))
	if err != nil {
		t.Fatalf("decode output: %v: %s", err, b)
	}
	return parsed.(map[string]any)["data"].(map[string]any)
}

func TestAccountSetExitRouteChecksBeforeWrite(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct"}, {Login: "bob", Password: config.InitPlaceholder, Exit: "direct", Provider: "missing"}}
	called := false
	err := accountSetExit(t.TempDir(), cfg, []string{"alice,bob", "route"}, func() error { called = true; return nil }, &bytes.Buffer{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NO_NODES_FOR_COUNTRY" || called || cfg.Accounts[0].Exit != "direct" {
		t.Fatalf("err=%v called=%v accounts=%+v", err, called, cfg.Accounts)
	}
}

func TestAccountShowDirectHasNoPreChainOrTiers(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct"}}
	var out bytes.Buffer
	if err := accountShow(t.TempDir(), cfg, []string{"alice"}, &out); err != nil {
		t.Fatal(err)
	}
	data := exitOutData(t, out.Bytes())
	if data["exit"] != "direct" || len(data["tiers"].([]any)) != 0 {
		t.Fatalf("show data=%v", data)
	}
	pre := data["pre_socks5"].(map[string]any)
	if pre["value"] != "none" || pre["source"] != "direct" {
		t.Fatalf("pre_socks5=%v", pre)
	}
	if own, ok := pre["own"]; !ok || own != nil {
		t.Fatalf("pre_socks5 own=%v present=%v", own, ok)
	}
}

func TestAccountSetExitRejectsNoneAndInvalidValue(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	called := false
	for _, args := range [][]string{{"alice", "bogus"}, {"--none", "alice", "route"}} {
		err := accountSetExit(t.TempDir(), cfg, args, func() error { called = true; return nil }, &bytes.Buffer{})
		var ce *cli.Error
		if !errors.As(err, &ce) || called {
			t.Fatalf("args=%v err=%v called=%v", args, err, called)
		}
		wantCode := config.CodeConfigInvalid
		if args[0] == "alice" {
			wantCode = config.CodeConfigInvalid
		}
		if args[0] == "--none" {
			wantCode = "USAGE"
		}
		if ce.Code != wantCode {
			t.Fatalf("args=%v code=%s want=%s", args, ce.Code, wantCode)
		}
	}
}

func TestAccountAddAndCloneExit(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	call := func(args ...string) error {
		out.Reset()
		return runAccount(append(args, "--home", home, "--json"), &out)
	}
	if err := call("add", "alice", "--exit", "direct"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(directExitHint)) {
		t.Fatalf("add hint missing: %s", out.String())
	}
	if err := call("add", "alice", "--exit", "direct"); err != nil {
		t.Fatal(err)
	}
	addData := exitOutData(t, out.Bytes())
	addedAccount, ok := addData["account"].(map[string]any)
	if !ok || addData["existing"] != true || addData["hint"] != directExitHint {
		t.Fatalf("repeat add: %s", out.String())
	}
	if addedAccount["exit"] != "direct" || addedAccount["login"] != "alice" {
		t.Fatalf("repeat add account: %v", addedAccount)
	}
	if err := call("add", "alice", "--exit", "route"); err == nil {
		t.Fatal("repeated add with a different exit should conflict")
	} else {
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "ACCOUNT_EXISTS" {
			t.Fatalf("repeated add with route: err=%v", err)
		}
	}
	if err := call("clone", "alice", "bob"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("bob"); a == nil || a.Exit != "direct" {
		t.Fatalf("clone exit: %+v", a)
	}
	if err := call("clone", "alice", "carol", "--exit", ""); err == nil {
		t.Fatal("explicit empty clone exit should fail")
	}
	if err := call("clone", "alice", "carol", "--exit", "route"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := cfg.Account("carol"); a == nil || a.Exit != "route" {
		t.Fatalf("clone override: %+v", a)
	}
	if err := call("list"); err != nil {
		t.Fatal(err)
	}
	listed := exitOutData(t, out.Bytes())["accounts"].([]any)[0].(map[string]any)
	if listed["exit"] != "direct" {
		t.Fatalf("listed exit=%v", listed["exit"])
	}
}

func TestAccountEffectiveExit(t *testing.T) {
	if effectiveExit(&config.Account{}) != "route" {
		t.Fatal("empty exit should mean route")
	}
}
