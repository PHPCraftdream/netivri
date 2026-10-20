package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func TestAccountCommands(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	var out bytes.Buffer
	call := func(args ...string) error {
		out.Reset()
		return runAccount(append(args, "--home", home), &out)
	}
	if err := call("add", "alice", "--country", "de"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"existing":false`)) {
		t.Fatalf("add output %s", out.Bytes())
	}
	if err := call("add", "alice", "--country", "DE"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"existing":true`)) {
		t.Fatalf("idempotent output %s", out.Bytes())
	}
	if bytes.Contains(out.Bytes(), []byte(config.InitPlaceholder)) {
		t.Fatalf("output disclosed placeholder: %s", out.Bytes())
	}
	if err := call("list"); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data struct {
			Accounts []struct {
				Login       string `json:"login"`
				PasswordSet bool   `json:"password_set"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("decode list output %s: %v", out.Bytes(), err)
	}
	if len(list.Data.Accounts) != 1 || list.Data.Accounts[0].Login != "alice" || list.Data.Accounts[0].PasswordSet {
		t.Fatalf("list accounts = %+v, want alice with password_set false", list.Data.Accounts)
	}
	if bytes.Contains(out.Bytes(), []byte(config.InitPlaceholder)) || bytes.Contains(out.Bytes(), []byte("$argon2id$")) {
		t.Fatalf("list disclosed password data: %s", out.Bytes())
	}
	if err := call("set-country", "alice", "--none"); err != nil {
		t.Fatal(err)
	}
	var cleared struct {
		Data struct {
			Account struct {
				ExitCountry string `json:"exit_country"`
			} `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &cleared); err != nil {
		t.Fatalf("decode clear output %s: %v", out.Bytes(), err)
	}
	if cleared.Data.Account.ExitCountry != "" {
		t.Fatalf("cleared exit_country = %q, want empty", cleared.Data.Account.ExitCountry)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"exit_country":""`)) {
		t.Fatalf("clear output omitted empty exit_country key: %s", out.Bytes())
	}
	if err := call("rm", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.ConfigPath(home)); err != nil {
		t.Fatalf("config not saved: %v", err)
	}
	other := t.TempDir()
	out.Reset()
	if err := runAccount([]string{"list", "--home", other}, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("alice")) {
		t.Fatalf("home leaked account: %s", out.Bytes())
	}
	if _, err := os.Stat(filepath.Join(other, "config.ktav")); !os.IsNotExist(err) {
		t.Fatalf("read-only list created config: %v", err)
	}
}
