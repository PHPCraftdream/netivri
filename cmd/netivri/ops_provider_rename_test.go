package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestProviderRenameSameNameRejected(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("p", subscription.Snapshot{Nodes: []subscription.Node{{ID: "n1", Country: "DE"}}, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	path := config.ConfigPath(home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = ProviderRename(home, "p", "p")
	renameAssertCode(t, err, "USAGE", cli.ExitUsage)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("same-name rename wrote the config")
	}
	snap, serr := store.Load("p")
	if serr != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("snapshot after same-name rename: %+v %v", snap, serr)
	}
}

func TestProviderRenameCaseOnlyRejectedWithoutWrites(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p"}}
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}

	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	wantSnap := subscription.Snapshot{
		Nodes:     []subscription.Node{{ID: "n1", Country: "DE"}},
		FetchedAt: testNow,
		Skipped:   2,
	}
	if err = store.Save("p", wantSnap); err != nil {
		t.Fatal(err)
	}
	configPath := config.ConfigPath(home)
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(home, "subscriptions", "p.ktav")
	beforeSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}

	err = ProviderRename(home, "p", "P")
	renameAssertCode(t, err, "USAGE", cli.ExitUsage)

	afterConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterConfig) != string(beforeConfig) {
		t.Fatal("case-only rename changed the config")
	}
	afterSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSnapshot) != string(beforeSnapshot) {
		t.Fatal("case-only rename changed the stored snapshot")
	}
	afterSnap, err := store.Load("p")
	if err != nil || afterSnap.FetchedAt != wantSnap.FetchedAt || afterSnap.Skipped != wantSnap.Skipped || len(afterSnap.Nodes) != len(wantSnap.Nodes) || afterSnap.Nodes[0].ID != wantSnap.Nodes[0].ID || afterSnap.Nodes[0].Country != wantSnap.Nodes[0].Country {
		t.Fatalf("snapshot after rejected rename: %+v %v", afterSnap, err)
	}
}

func TestProviderRenameSnapshotWriteFailureLeavesEverything(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("p", subscription.Snapshot{Nodes: []subscription.Node{{ID: "n1", Country: "DE"}}, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	// A directory where the new snapshot file would go: the atomic rename in
	// store.Save fails on every platform.
	blocker := filepath.Join(home, "subscriptions", "renamed.ktav")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(blocker) })
	path := config.ConfigPath(home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProviderRename(home, "p", "renamed"); err == nil {
		t.Fatal("rename with a failing snapshot write succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("snapshot write failure wrote the config")
	}
	if _, serr := store.Load("p"); serr != nil {
		t.Fatalf("old snapshot disturbed: %v", serr)
	}
}

func TestProviderRenameConfigSaveFailureLeavesOldState(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save("p", subscription.Snapshot{Nodes: []subscription.Node{{ID: "n1", Country: "DE"}}, FetchedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	// Make the config directory unwritable after the config can still be
	// read from it: config.Save then fails creating its temporary file. The
	// trick does not work on Windows (chmod is ignored) or for root.
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a chmod-respecting, non-root filesystem")
	}
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	path := config.ConfigPath(home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ProviderRename(home, "p", "renamed"); err == nil {
		t.Fatal("rename with a failing config save succeeded")
	}
	if err = os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("config save failure replaced the config")
	}
	if fi, serr := os.Stat(path); serr != nil || fi.IsDir() {
		t.Fatalf("config path after failure: %v %v", fi, serr)
	}
	snap, serr := store.Load("p")
	if serr != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("old snapshot after config save failure: %+v %v", snap, serr)
	}
	if _, serr = os.Stat(filepath.Join(home, "subscriptions", "renamed.ktav")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("stray new-name snapshot left behind: %v", serr)
	}
}

func TestProviderRenameWithoutSnapshot(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	if err := ProviderRename(home, "p", "renamed"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(home)
	if err != nil || len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].Name != "renamed" {
		t.Fatalf("subscriptions after rename without snapshot: %+v %v", cfg.Subscriptions, err)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	names, err := store.List()
	if err != nil || len(names) != 0 {
		t.Fatalf("snapshot files created for a rename without a snapshot: %v %v", names, err)
	}
}
