package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestServerOperationsListShowAndSafeViews(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 12345, Accounts: "alice", PublicHost: "edge.example", Params: map[string]string{"cipher": "aes-128-gcm"}}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	list, err := ServerList(home)
	if err != nil || len(list) != 1 || list[0].Name != "edge" || strings.Join(list[0].Params, ",") != "cipher" {
		t.Fatalf("list=%#v err=%v", list, err)
	}
	shown, err := ServerShow(home, "edge")
	if err != nil || shown.Name != "edge" {
		t.Fatalf("show=%#v err=%v", shown, err)
	}
	for _, view := range []serverView{list[0], shown} {
		if !reflect.DeepEqual(view.Params, []string{"cipher"}) {
			t.Fatalf("unsafe params view: %#v", view.Params)
		}
		encoded, err := json.Marshal(view)
		if err != nil || strings.Contains(string(encoded), "aes-128-gcm") || strings.Contains(string(encoded), "secret") {
			t.Fatalf("view leaked parameter value: %s err=%v", encoded, err)
		}
	}
}

func TestServerOperationsEmptyMissingMalformedAndConfigError(t *testing.T) {
	home := t.TempDir()
	items, err := ServerList(home)
	if err != nil || len(items) != 0 {
		t.Fatalf("empty list=%v err=%v", items, err)
	}
	_, err = ServerShow(home, "missing")
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "SERVER_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Fatalf("missing error=%v", err)
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "config.ktav"), []byte("servers: ["), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = ServerList(bad)
	if !errors.As(err, &ce) || ce.Code != "CONFIG_INVALID" {
		t.Fatalf("malformed error=%v", err)
	}
	_, err = ServerList("\x00")
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Fatalf("config error=%v", err)
	}
}

func TestServerOperationViewsContainNoConfigCredentialFields(t *testing.T) {
	typ := reflect.TypeOf(serverView{})
	for _, forbidden := range []string{"Password", "Secret", "Link", "URL", "Credential"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Errorf("serverView has forbidden field %q", forbidden)
		}
	}
}
