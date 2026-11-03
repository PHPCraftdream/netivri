package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
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

func TestServerOperationsAddUpdateViewsHideGeneratedSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	firstAdd, err := ServerAdd(home, ServerAddOptions{Name: "edge", Protocol: "vmess", Port: 39001, PublicHost: "edge.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstAdd.GeneratedKeys, []string{"alice.uuid", "bob.uuid"}) {
		t.Fatalf("generated keys=%v", firstAdd.GeneratedKeys)
	}

	view, err := ServerShow(home, "edge")
	if err != nil {
		t.Fatal(err)
	}
	added, err := ServerAdd(home, ServerAddOptions{Name: "edge2", Protocol: "vmess", Port: 39002, PublicHost: "other.example"})
	if err != nil {
		t.Fatal(err)
	}
	if added.Name != "edge2" || !reflect.DeepEqual(added.GeneratedKeys, []string{"alice.uuid", "bob.uuid"}) {
		t.Fatalf("add result=%#v", added)
	}
	if view.Accounts != "*" {
		t.Fatalf("default accounts=%q", view.Accounts)
	}
	resolvedCfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolvedCfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	var specs []servers.Spec
	for _, r := range resolved {
		sp, e := servers.NewStore(home).Apply(r)
		if e != nil {
			t.Fatal(e)
		}
		specs = append(specs, sp)
	}
	addedJSON, err := json.Marshal([]any{firstAdd, added})
	if err != nil {
		t.Fatal(err)
	}
	views := []serverView{view}
	listed, err := ServerList(home)
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, listed...)
	shown, err := ServerShow(home, "edge")
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, shown)
	port := 39002
	updated, err := ServerUpdate(home, "edge", ServerUpdateOptions{Port: &port})
	if err != nil {
		t.Fatal(err)
	}
	views = append(views, updated)
	for _, v := range views {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		for _, spec := range specs {
			for _, secrets := range spec.Users {
				for _, secret := range secrets {
					if secret != "" && (strings.Contains(string(b), secret) || strings.Contains(string(addedJSON), secret)) {
						t.Fatal("public view or add result contains a generated secret")
					}
				}
			}
			for _, secret := range spec.Material {
				if secret != "" && (strings.Contains(string(b), secret) || strings.Contains(string(addedJSON), secret)) {
					t.Fatal("public view or add result contains generated material")
				}
			}
		}
	}
}

func TestServerUpdateMergesParamsAndPreservesSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "trojan", Bind: "127.0.0.1", Port: 39101, Accounts: "alice", PublicHost: "edge.example", Default: true, Params: map[string]string{"sni": "one.example", "alpn": "h2"}}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.EnsureSecrets(specs[0]); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"sni": "two.example"}
	got, err := ServerUpdate(home, "edge", ServerUpdateOptions{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	params["alpn"] = "caller-mutated"
	if got.PublicHost != "edge.example" || !got.Default || got.Protocol != "trojan" {
		t.Fatalf("unchanged fields lost: %#v", got)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("update modified server secret bytes")
	}
	loaded, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	p := loaded.Servers[0].Params
	if p["sni"] != "two.example" || p["alpn"] != "h2" {
		t.Fatalf("merged params=%v", p)
	}
}

func TestServerOperationFailuresPreserveFilesAndReportCodes(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39201, Accounts: "*"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := servers.NewStore(home).EnsureSecrets(specs[0]); err != nil {
		t.Fatal(err)
	}
	configPath, secretPath := config.ConfigPath(home), servers.NewStore(home).Path()
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	secretBefore, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func() error
		code string
	}{
		{"duplicate", func() error {
			_, e := ServerAdd(home, ServerAddOptions{Name: "edge", Protocol: "vmess", Port: 39202})
			return e
		}, "SERVER_EXISTS"},
		{"port", func() error {
			_, e := ServerAdd(home, ServerAddOptions{Name: "other", Protocol: "vmess", Bind: "127.0.0.1", Port: config.Default().Listen.Port})
			return e
		}, "PORT_IN_USE"},
		{"protocol", func() error {
			_, e := ServerAdd(home, ServerAddOptions{Name: "other", Protocol: "unknown", Port: 39203})
			return e
		}, "CONFIG_INVALID"},
		{"invalid update", func() error {
			bad := "not-an-ip"
			_, e := ServerUpdate(home, "edge", ServerUpdateOptions{Bind: &bad})
			return e
		}, "CONFIG_INVALID"},
		{"missing update", func() error { _, e := ServerUpdate(home, "missing", ServerUpdateOptions{}); return e }, "SERVER_NOT_FOUND"},
		{"missing remove", func() error { _, e := ServerRemove(home, "missing"); return e }, "SERVER_NOT_FOUND"},
		{"missing rotate", func() error { _, e := ServerRotate(home, "missing", "alice"); return e }, "SERVER_NOT_FOUND"},
		{"missing link", func() error { _, e := ServerLink(home, "missing", ServerLinkOptions{Format: "uri"}); return e }, "SERVER_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("error=%v want %s", err, tc.code)
			}
			c, e := os.ReadFile(configPath)
			if e != nil || !bytes.Equal(c, configBefore) {
				t.Fatal("failed operation changed config bytes")
			}
			s, e := os.ReadFile(secretPath)
			if e != nil || !bytes.Equal(s, secretBefore) {
				t.Fatal("failed operation changed secret bytes")
			}
		})
	}
}

func TestServerLinkFormatsMatchCLIAndValidateFormat(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39301, Accounts: "alice", PublicHost: "edge.example"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := servers.NewStore(home).EnsureSecrets(specs[0]); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"uri", "mihomo"} {
		direct, err := ServerLink(home, "edge", ServerLinkOptions{Account: "alice", Format: format})
		if err != nil {
			t.Fatal(err)
		}
		env, _, err := serverDispatch(t, home, "link", "edge", "--account", "alice", "--format", format)
		if err != nil {
			t.Fatal(err)
		}
		if env.Data["link"] != direct {
			t.Fatalf("%s CLI link differs from API", format)
		}
	}
	_, err = ServerLink(home, "edge", ServerLinkOptions{Account: "alice", Format: "bogus"})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" {
		t.Fatalf("invalid format error=%v", err)
	}
}

func TestServerUpdateAmbiguousSharedTLSRollsBackFiles(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{
		{Name: "first", Protocol: "trojan", Bind: "127.0.0.1", Port: 39501, Accounts: "*", Params: map[string]string{"sni": "one.example"}},
		{Name: "second", Protocol: "trojan", Bind: "127.0.0.1", Port: 39501, Accounts: "*", Params: map[string]string{"sni": "two.example"}},
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	for _, spec := range specs {
		if _, err := store.EnsureSecrets(spec); err != nil {
			t.Fatal(err)
		}
	}
	configBefore, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	secretsBefore, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"sni": "one.example"}
	_, err = ServerUpdate(home, "second", ServerUpdateOptions{Params: params})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("update error=%v", err)
	}
	configAfter, err := os.ReadFile(config.ConfigPath(home))
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("ambiguous update changed config bytes")
	}
	secretsAfter, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(secretsBefore, secretsAfter) {
		t.Fatal("ambiguous update changed secrets bytes")
	}
}

func TestServerAddSaveFailureRestoresSecretsSnapshot(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	store := servers.NewStore(home)
	_, err := serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39601, Accounts: "alice"}, func(string, *config.Config) error { return errors.New("save failure") })
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Fatalf("save error=%v", err)
	}
	if names := store.Names(); len(names) != 0 {
		t.Fatalf("secrets remain: %v", names)
	}
	if len(cfg.Servers) != 0 {
		t.Fatal("server remains in config")
	}
}

func TestServerAddSaveFailureTrackedCleanupPreservesConcurrentSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	sentinel := errors.New("save failed")
	_, err = serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39611, Accounts: "alice,bob"}, func(_ string, _ *config.Config) error {
		other := servers.Spec{Name: "other", Protocol: "vmess", Bind: "127.0.0.1", Port: 39612, Accounts: []string{"alice"}}
		if _, e := store.EnsureSecrets(other); e != nil {
			t.Fatal(e)
		}
		if _, e := store.Rotate(other, "alice"); e != nil {
			t.Fatal(e)
		}
		return sentinel
	})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || ce.Hint != "" || !errors.Is(err, sentinel) {
		t.Fatalf("save error=%v", err)
	}
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed add changed config")
	}
	if names := store.Names(); len(names) != 1 || names[0] != "other" {
		t.Fatalf("remaining names=%v", names)
	}
	if other, err := store.Apply(servers.Spec{Name: "other", Protocol: "vmess", Accounts: []string{"alice"}}); err != nil || len(other.Users["alice"]) == 0 {
		t.Fatalf("concurrent credentials missing: %v", err)
	}
}

func TestServerUpdatePublicHostAndTypedResults(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39621, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	host := "edge.example"
	updated, err := ServerUpdate(home, "edge", ServerUpdateOptions{PublicHost: &host})
	if err != nil || updated.PublicHost != host {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
	rotated, err := ServerRotate(home, "edge", "")
	if err != nil || rotated.Name != "edge" || !rotated.SharedCredentials || len(rotated.Accounts) != 0 {
		t.Fatalf("rotate=%#v err=%v", rotated, err)
	}
	removed, err := ServerRemove(home, "edge")
	if err != nil || removed.Name != "edge" || !reflect.DeepEqual(removed.Accounts, []string{"alice"}) {
		t.Fatalf("remove=%#v err=%v", removed, err)
	}
}
func TestServerAddCleanupFailureIncludesHint(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	store := servers.NewStore(home)
	_, err := serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39631, Accounts: "alice"}, func(string, *config.Config) error {
		if e := os.RemoveAll(store.Path()); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(store.Path(), 0700); e != nil {
			t.Fatal(e)
		}
		return errors.New("save failed")
	})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || !strings.Contains(ce.Hint, "server-secrets.ktav") || !strings.Contains(ce.Hint, "edge") {
		t.Fatalf("error=%v", err)
	}
}

func TestServerAddFailedSaveNoopDoesNotChangeSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	entry := config.Server{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39641, Accounts: "alice"}
	cfg.Servers = []config.Server{entry}
	if _, err := servers.NewStore(home).EnsureSecrets(servers.Spec{Name: entry.Name, Protocol: entry.Protocol, Bind: entry.Bind, Port: entry.Port, Accounts: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	cfg.Servers = nil
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39642, Accounts: "alice"}, func(string, *config.Config) error { called = true; return errors.New("save failed") })
	var ce *cli.Error
	if !called || !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Fatalf("save hook called=%t error=%v", called, err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("no-op failed save changed existing secrets")
	}
}

func TestServerAddSaveFailureCleanupFailureHint(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	store := servers.NewStore(home)
	_, err := serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39651, Accounts: "alice"}, func(string, *config.Config) error {
		if e := os.RemoveAll(store.Path()); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(store.Path(), 0700); e != nil {
			t.Fatal(e)
		}
		return errors.New("save failed")
	})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || !strings.Contains(ce.Hint, "server-secrets.ktav") || !strings.Contains(ce.Hint, "edge") {
		t.Fatalf("error=%v", err)
	}
}

func TestServerAddUpdatePublicHostPreservesOtherFields(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "trojan", Bind: "127.0.0.1", Port: 39661, Accounts: "alice", PublicHost: "old.example", Default: true, Params: map[string]string{"sni": "old.example", "alpn": "h2"}}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	host := "new.example"
	got, err := ServerUpdate(home, "edge", ServerUpdateOptions{PublicHost: &host})
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicHost != host || !got.Default || got.Protocol != "trojan" || got.Port != 39661 {
		t.Fatalf("partial update lost fields: %#v", got)
	}
}

func TestServerRotateTypedAffectedAccounts(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "multi", Protocol: "vmess", Bind: "127.0.0.1", Port: 39701, Accounts: "alice,bob"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := servers.NewStore(home).EnsureSecrets(servers.Spec{Name: "multi", Protocol: "vmess", Bind: "127.0.0.1", Port: 39701, Accounts: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	all, err := ServerRotate(home, "multi", "")
	if err != nil || all.SharedCredentials || !reflect.DeepEqual(all.Accounts, []string{"alice", "bob"}) {
		t.Fatalf("all rotation=%#v err=%v", all, err)
	}
	one, err := ServerRotate(home, "multi", "alice")
	if err != nil || one.SharedCredentials || !reflect.DeepEqual(one.Accounts, []string{"alice"}) {
		t.Fatalf("account rotation=%#v err=%v", one, err)
	}
}

func TestServerUpdateSaveFailurePreservesConcurrentSecretWrites(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39711, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.EnsureSecrets(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	beforeCfg, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	bob := "alice,bob"
	sentinel := errors.New("save failed")
	_, err = serverUpdateConfigWithSave(home, cfg, "edge", ServerUpdateOptions{Accounts: &bob}, func(string, *config.Config) error {
		other := servers.Spec{Name: "other", Protocol: "vmess", Bind: "127.0.0.1", Port: 39712, Accounts: []string{"alice"}}
		if _, e := store.EnsureSecrets(other); e != nil {
			t.Fatal(e)
		}
		return sentinel
	})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || !errors.Is(err, sentinel) {
		t.Fatalf("update error=%v", err)
	}
	afterCfg, err := os.ReadFile(config.ConfigPath(home))
	if err != nil || !bytes.Equal(beforeCfg, afterCfg) {
		t.Fatal("failed update changed config")
	}
	after, err := store.Apply(specs[0])
	if err != nil || !reflect.DeepEqual(initial.Users["alice"], after.Users["alice"]) {
		t.Fatalf("existing alice credential changed: %v", err)
	}
	if _, ok := after.Users["bob"]; ok {
		t.Fatal("new bob credential was not cleaned")
	}
	if names := store.Names(); !reflect.DeepEqual(names, []string{"edge", "other"}) {
		t.Fatalf("stored entries=%v", names)
	}
}

func TestServerAddSaveFailureCleanupPreservesSameServerRotation(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	beforeCfg, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var rotated servers.Spec
	_, err = serverAddConfigWithSave(home, cfg, ServerAddOptions{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39721, Accounts: "alice,bob"}, func(string, *config.Config) error {
		rotated = servers.Spec{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39721, Accounts: []string{"alice"}}
		if _, e := store.EnsureSecrets(rotated); e != nil {
			t.Fatal(e)
		}
		if _, e := store.Rotate(rotated, "alice"); e != nil {
			t.Fatal(e)
		}
		return errors.New("save failed")
	})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Fatalf("add error=%v", err)
	}
	afterCfg, err := os.ReadFile(config.ConfigPath(home))
	if err != nil || !bytes.Equal(beforeCfg, afterCfg) {
		t.Fatal("failed add changed config")
	}
	after, err := store.Apply(rotated)
	if err != nil || len(after.Users["alice"]) == 0 {
		t.Fatalf("rotated alice credential lost: %v", err)
	}
	if _, ok := after.Users["bob"]; ok {
		t.Fatal("unchanged created bob credential remains")
	}
}

func TestServerProtocolsMetadataHasValidDefaults(t *testing.T) {
	protocols := ServerProtocols()
	if len(protocols) == 0 {
		t.Fatal("no protocols")
	}
	seen := map[string]bool{}
	for _, protocol := range protocols {
		if protocol.Name == "" || protocol.Label == "" || seen[protocol.Name] {
			t.Fatalf("invalid protocol metadata: %#v", protocol)
		}
		seen[protocol.Name] = true
		metadata, ok := servers.Metadata(protocol.Name)
		if !ok || len(protocol.Parameters) != len(metadata.Parameters) || protocol.PublicHost.Name != metadata.PublicHost.Name || protocol.PublicHost.Label != metadata.PublicHost.Label || protocol.PublicHost.Kind != metadata.PublicHost.Kind {
			t.Fatalf("metadata mismatch for %q", protocol.Name)
		}
		params := map[string]string{}
		for i, parameter := range protocol.Parameters {
			if parameter.Name == "" || parameter.Label == "" || parameter.Kind == "" {
				t.Fatalf("invalid parameter schema: %#v", parameter)
			}
			if parameter.Default != metadata.Parameters[i].Default {
				t.Fatalf("default changed for %s.%s", protocol.Name, parameter.Name)
			}
			if parameter.Default != "" {
				params[parameter.Name] = parameter.Default
			}
		}
		p, ok := servers.Lookup(protocol.Name)
		if !ok {
			t.Fatalf("protocol %q is not registered", protocol.Name)
		}
		if err := p.Validate(params); err != nil {
			t.Fatalf("metadata defaults invalid for %s: %v", protocol.Name, err)
		}
	}
}

func TestServerRotateAccountAndRemoveSecrets(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}, {Login: "bob", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "vmess", Bind: "127.0.0.1", Port: 39401, Accounts: "alice,bob"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveServers()
	if err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	if _, err := store.EnsureSecrets(specs[0]); err != nil {
		t.Fatal(err)
	}
	before, err := store.Apply(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ServerRotate(home, "edge", "alice"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Apply(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before.Users["alice"], after.Users["alice"]) {
		t.Fatal("alice secret did not rotate")
	}
	if !reflect.DeepEqual(before.Users["bob"], after.Users["bob"]) {
		t.Fatal("bob secret changed during alice rotation")
	}
	_, err = ServerRotate(home, "edge", "nobody")
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" {
		t.Fatalf("invalid account error=%v", err)
	}
	if _, err := ServerRemove(home, "edge"); err != nil {
		t.Fatal(err)
	}
	list, err := ServerList(home)
	if err != nil || len(list) != 0 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	if names := store.Names(); len(names) != 0 {
		t.Fatal("removed secret entry remains")
	}
}

func TestValidServerHostAcceptsOrdinaryNamesAndRejectsControlCharacters(t *testing.T) {
	for _, h := range []string{"node.example", "other.example", "localhost", "terrain.net", "10.0.0.1", "::1"} {
		if !validServerHost(h) {
			t.Errorf("validServerHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"", "a b", "a/b", "a\tb", "a\rb", "a\nb", "a?b", "a#b", "a@b", "[::1]"} {
		if validServerHost(h) {
			t.Errorf("validServerHost(%q) = true, want false", h)
		}
	}
}
