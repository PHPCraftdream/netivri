package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func renameAssertCode(t *testing.T, err error, code string, exit int) {
	t.Helper()
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != code || ce.Exit != exit {
		t.Fatalf("error: %v (want code %s exit %d)", err, code, exit)
	}
}

func TestProviderRenameSuccessFollowsAccountAndSnapshot(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid/token"}, config.Subscription{Name: "q", URL: "https://q.invalid"})
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "other,p,third"}}
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err = store.Save("p", subscription.Snapshot{Nodes: []subscription.Node{{ID: "n1", Country: "DE"}}, FetchedAt: testNow, Skipped: 2}); err != nil {
		t.Fatal(err)
	}
	if err = ProviderRename(home, "p", "renamed"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Subscriptions) != 2 || cfg.Subscriptions[0].Name != "renamed" || cfg.Subscriptions[0].URL != "https://old.invalid/token" || cfg.Subscriptions[1].Name != "q" {
		t.Fatalf("subscriptions after rename: %+v", cfg.Subscriptions)
	}
	p, ok := config.ParseProviderList(cfg.Accounts[0].Provider)
	if !ok || strings.Join(p, ",") != "other,renamed,third" {
		t.Fatalf("account provider list did not follow: %q %v", cfg.Accounts[0].Provider, p)
	}
	snap, err := store.Load("renamed")
	if err != nil || !snap.FetchedAt.Equal(testNow) || snap.Skipped != 2 || len(snap.Nodes) != 1 {
		t.Fatalf("renamed snapshot: %+v %v", snap, err)
	}
	if _, err = store.Load("p"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old snapshot still present: %v", err)
	}
}

func TestProviderRenameUnknownOldName(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	err := ProviderRename(home, "missing", "renamed")
	renameAssertCode(t, err, codeSubNotFound, cli.ExitNotFound)
}

func TestProviderRenameExistingNewName(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"}, config.Subscription{Name: "taken", URL: "https://taken.invalid"})
	err := ProviderRename(home, "p", "taken")
	renameAssertCode(t, err, codeSubExistsName, cli.ExitConflict)
	cfg, e := loadConfig(home)
	if e != nil || len(cfg.Subscriptions) != 2 || cfg.Subscriptions[0].Name != "p" {
		t.Fatalf("rejected rename changed config: %+v %v", cfg.Subscriptions, e)
	}
}

func TestProviderRenameInvalidNewName(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	for _, bad := range []string{"bad/name", "", "..", "with space", strings.Repeat("x", 65)} {
		err := ProviderRename(home, "p", bad)
		renameAssertCode(t, err, "USAGE", cli.ExitUsage)
	}
	cfg, e := loadConfig(home)
	if e != nil || len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "p" {
		t.Fatalf("invalid rename changed config: %+v %v", cfg.Subscriptions, e)
	}
}

func TestProviderRenameValidationFailureWritesNothing(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	// An account provider list that no longer parses: with the old name gone
	// from the follow pass, Validate must refuse and nothing may be written.
	names := make([]string, 0, config.MaxAccountProviders+1)
	for i := 0; i <= config.MaxAccountProviders; i++ {
		names = append(names, "x"+string(rune('a'+i)))
	}
	names[0] = "p"
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: config.JoinList(names)}}
	path := config.ConfigPath(home)
	if err = config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config.ParseProviderList(cfg.Accounts[0].Provider); ok {
		t.Fatal("seeded provider list unexpectedly valid")
	}
	if err = ProviderRename(home, "p", "renamed"); err == nil {
		t.Fatal("rename with unparseable account list succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("validation failure wrote the config")
	}
	if _, serr := subscription.NewStore(filepath.Join(home, "subscriptions")).Load("p"); serr != nil {
		t.Fatalf("snapshot disturbed on validation failure: %v", serr)
	}
}
