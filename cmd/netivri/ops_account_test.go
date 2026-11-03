package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestAccountOperationsRoundTrip(t *testing.T) {
	stubAccountProbe(t)
	home := t.TempDir()
	if e := config.Save(config.ConfigPath(home), config.Default()); e != nil {
		t.Fatal(e)
	}
	a, e := AccountAdd(home, AccountAddOptions{Login: "alice", Clients: "loopback"})
	if e != nil || a.Existing {
		t.Fatalf("add=%#v %v", a, e)
	}
	if _, e = AccountUpdate(home, AccountUpdateOptions{Login: "alice", PostSOCKS5: ptr("none")}); e != nil {
		t.Fatal(e)
	}
	clone, e := AccountClone(home, AccountCloneOptions{Source: "alice", Login: "bob"})
	if e != nil || clone.Password != clonePasswordInit {
		t.Fatalf("clone=%#v %v", clone, e)
	}
	show, e := AccountShow(home, "bob")
	if e != nil || show.Login != "bob" {
		t.Fatalf("show=%#v %v", show, e)
	}
	rows, e := AccountList(home)
	if e != nil || len(rows) != 2 {
		t.Fatalf("list=%#v %v", rows, e)
	}
	if e = AccountRemove(home, "bob"); e != nil {
		t.Fatal(e)
	}
}
func stubAccountProbe(t *testing.T) {
	old := angelProbeLive
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, errors.New("not running")
	}
	t.Cleanup(func() { angelProbeLive = old })
}
func TestAccountOpsValidationMatrix(t *testing.T) {
	home := accountProviderHome(t)
	cases := []struct {
		name string
		o    AccountAddOptions
		code string
	}{{"login", AccountAddOptions{Login: "bad login"}, "ACCOUNT_INVALID"}, {"provider", AccountAddOptions{Login: "u", Provider: "missing"}, "SUB_NOT_FOUND"}, {"country", AccountAddOptions{Login: "u", Country: "NLD"}, "COUNTRY_INVALID"}, {"pre", AccountAddOptions{Login: "u", PreSOCKS5: "bad"}, "CONFIG_INVALID"}, {"post", AccountAddOptions{Login: "u", PostSOCKS5: "http://host"}, "CONFIG_INVALID"}, {"clients", AccountAddOptions{Login: "u", Clients: "bad rule"}, "CONFIG_INVALID"}, {"exit", AccountAddOptions{Login: "u", ExitGiven: true}, config.CodeConfigInvalid}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e := AccountAdd(home, tc.o)
			if !codeIs(e, tc.code) {
				t.Fatalf("got %v want %s", e, tc.code)
			}
		})
	}
	a, e := AccountAdd(home, AccountAddOptions{Login: "alice", Provider: "p1", Country: "DE"})
	if e != nil || a.Existing {
		t.Fatalf("add %#v %v", a, e)
	}
	a, e = AccountAdd(home, AccountAddOptions{Login: "alice", Provider: "p1", Country: "de"})
	if e != nil || !a.Existing {
		t.Fatalf("idem %#v %v", a, e)
	}
	if _, e = AccountAdd(home, AccountAddOptions{Login: "alice", Provider: "p2", Country: "DE"}); !codeIs(e, "ACCOUNT_EXISTS") {
		t.Fatalf("conflict=%v", e)
	}
}
func TestAccountCloneValidationAndModes(t *testing.T) {
	home := accountProviderHome(t)
	c := config.Default()
	c.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://user:token@host.invalid/secret"}}
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$fake", Provider: "p1", ExitCountry: "DE", AllowedClients: "loopback"}}
	if e := config.Save(config.ConfigPath(home), c); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		o    AccountCloneOptions
		code string
	}{{"source", AccountCloneOptions{Source: "absent", Login: "bob"}, "ACCOUNT_NOT_FOUND"}, {"login", AccountCloneOptions{Source: "alice", Login: "bad login"}, "ACCOUNT_INVALID"}, {"empty pass", AccountCloneOptions{Source: "alice", Login: "bob", PasswordGiven: true}, "USAGE"}, {"long pass", AccountCloneOptions{Source: "alice", Login: "bob", PasswordGiven: true, Password: strings.Repeat("x", auth.MaxPasswordLen+1)}, "USAGE"}, {"conflict", AccountCloneOptions{Source: "alice", Login: "alice"}, "ACCOUNT_EXISTS"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			_, e := AccountClone(home, tc.o)
			if !codeIs(e, tc.code) {
				t.Fatalf("got %v want %s", e, tc.code)
			}
			after, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed clone persisted changes")
			}
		})
	}
	beforeClear, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AccountClone(home, AccountCloneOptions{Source: "alice", Login: "clearclients", ClientsGiven: true}); err != nil {
		t.Fatal(err)
	}
	cleared, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Accounts[0].AllowedClients != "loopback" || cleared.Accounts[len(cleared.Accounts)-1].AllowedClients != "" {
		t.Fatalf("source/clone clients = %q/%q", cleared.Accounts[0].AllowedClients, cleared.Accounts[len(cleared.Accounts)-1].AllowedClients)
	}
	if bytes.Equal(beforeClear, mustReadConfig(t, home)) {
		t.Fatal("successful clone was not persisted")
	}
	if _, e := AccountClone(home, AccountCloneOptions{Source: "alice", Login: "route", Country: "SE"}); !codeIs(e, "NO_NODES_FOR_COUNTRY") {
		t.Fatalf("route %v", e)
	}
	for _, tc := range []struct {
		name string
		o    AccountCloneOptions
		want string
	}{{"copied", AccountCloneOptions{Source: "alice", Login: "copy"}, clonePasswordCopied}, {"new", AccountCloneOptions{Source: "alice", Login: "fresh", PasswordGiven: true, Password: "fake new password"}, clonePasswordNew}, {"init", AccountCloneOptions{Source: "alice", Login: "pending", PasswordGiven: true, Password: config.InitPlaceholder}, clonePasswordInit}} {
		r, e := AccountClone(home, tc.o)
		if e != nil || r.Password != tc.want {
			t.Fatalf("%s %#v %v", tc.name, r, e)
		}
	}
}
func TestAccountUpdateIndependentFieldsAtomic(t *testing.T) {
	home := accountProviderHome(t)
	c := config.Default()
	c.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://x.invalid/a"}, {Name: "p2", URL: "https://x.invalid/b"}}
	c.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p1", ExitCountry: "DE"}}
	if e := config.Save(config.ConfigPath(home), c); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		o    AccountUpdateOptions
		ok   func(config.Account) bool
	}{{"provider", AccountUpdateOptions{Login: "alice", Provider: ptr("p2")}, func(a config.Account) bool { return a.Provider == "p2" && a.ExitCountry == "DE" }}, {"country", AccountUpdateOptions{Login: "alice", Country: ptr("NL")}, func(a config.Account) bool { return a.Provider == "p1" && a.ExitCountry == "NL" }}, {"clients", AccountUpdateOptions{Login: "alice", Clients: ptr("loopback")}, func(a config.Account) bool { return a.Provider == "p1" && a.AllowedClients == "loopback" }}, {"exit", AccountUpdateOptions{Login: "alice", Exit: ptr("direct")}, func(a config.Account) bool { return a.Exit == "direct" }}, {"pre", AccountUpdateOptions{Login: "alice", PreSOCKS5: ptr("none")}, func(a config.Account) bool { return a.PreSOCKS5 == "none" }}, {"post", AccountUpdateOptions{Login: "alice", PostSOCKS5: ptr("http://proxy.example:8080")}, func(a config.Account) bool { return a.PostSOCKS5 == "http://proxy.example:8080" }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := config.Default()
			base.Subscriptions = c.Subscriptions
			account := config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "p1", ExitCountry: "DE"}
			if tc.name == "exit" {
				account.Provider, account.ExitCountry = "", ""
			}
			base.Accounts = []config.Account{account}
			if e := config.Save(config.ConfigPath(home), base); e != nil {
				t.Fatal(e)
			}
			if _, e := AccountUpdate(home, tc.o); e != nil {
				t.Fatal(e)
			}
			got, e := config.Load(config.ConfigPath(home))
			if e != nil {
				t.Fatal(e)
			}
			if !tc.ok(got.Accounts[0]) {
				t.Fatalf("got %#v", got.Accounts[0])
			}
		})
	}
	invalidCountry := "SE"
	before, e := os.ReadFile(config.ConfigPath(home))
	if e != nil {
		t.Fatal(e)
	}
	_, e = AccountUpdate(home, AccountUpdateOptions{Login: "alice", Country: &invalidCountry, Clients: ptr("loopback")})
	if !codeIs(e, "NO_NODES_FOR_COUNTRY") {
		t.Fatalf("route error %v", e)
	}
	after, e := os.ReadFile(config.ConfigPath(home))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed update persisted")
	}
}
func TestAccountDirectAtomicCombinedChange(t *testing.T) {
	h := t.TempDir()
	c := config.Default()
	c.Subscriptions = []config.Subscription{{Name: "p", URL: "https://x.invalid"}}
	c.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"}}
	if e := config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	empty := ""
	direct := "direct"
	none := "none"
	if _, e := AccountUpdate(h, AccountUpdateOptions{Login: "alice", Provider: &empty, Country: &empty, PreSOCKS5: &none, Exit: &direct}); e != nil {
		t.Fatal(e)
	}
}
func TestAccountShowRemoveValidationAndSecrets(t *testing.T) {
	stubAccountProbe(t)
	h := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$FAKEHASH", PreSOCKS5: "socks5://fakeuser:FAKEPASS@proxy.invalid:1080", PostSOCKS5: "http://postuser:POSTPASS@post.invalid:8080"}}
	c.Subscriptions = []config.Subscription{{Name: "p", URL: "https://subuser:SUBTOKEN@sub.invalid/private/token"}}
	if e := config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	if _, e := AccountShow(h, "bad login"); !codeIs(e, "ACCOUNT_INVALID") {
		t.Fatalf("show invalid %v", e)
	}
	if _, e := AccountShow(h, "missing"); !codeIs(e, "ACCOUNT_NOT_FOUND") {
		t.Fatalf("show missing %v", e)
	}
	if e := AccountRemove(h, "bad login"); !codeIs(e, "ACCOUNT_INVALID") {
		t.Fatalf("rm invalid %v", e)
	}
	if e := AccountRemove(h, "missing"); !codeIs(e, "ACCOUNT_NOT_FOUND") {
		t.Fatalf("rm missing %v", e)
	}
	rows, e := AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	show, e := AccountShow(h, "alice")
	if e != nil {
		t.Fatal(e)
	}
	outputs := []any{rows, show, show.document()}
	for _, out := range outputs {
		for _, secret := range []string{"FAKEHASH", "fakeuser", "FAKEPASS", "postuser", "POSTPASS", "subuser", "SUBTOKEN", "/private/token"} {
			if containsString(out, secret) {
				t.Errorf("output includes %q", secret)
			}
		}
	}
}
func containsString(v any, needle string) bool {
	if v == nil {
		return false
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.String:
		return strings.Contains(r.String(), needle)
	case reflect.Interface, reflect.Pointer:
		if r.IsNil() {
			return false
		}
		return containsString(r.Elem().Interface(), needle)
	case reflect.Struct:
		for i := 0; i < r.NumField(); i++ {
			if containsString(r.Field(i).Interface(), needle) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < r.Len(); i++ {
			if containsString(r.Index(i).Interface(), needle) {
				return true
			}
		}
	case reflect.Map:
		iter := r.MapRange()
		for iter.Next() {
			if containsString(iter.Key().Interface(), needle) || containsString(iter.Value().Interface(), needle) {
				return true
			}
		}
	}
	return false
}
func TestAccountOperationSecretTypeSafety(t *testing.T) {
	stubAccountProbe(t)
	h := t.TempDir()
	c := config.Default()
	c.Subscriptions = []config.Subscription{{Name: "p", URL: "https://subuser:SUBTOKEN@sub.invalid/private/token"}}
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$FAKEHASH", Provider: "p", ExitCountry: "DE", AllowedClients: "loopback", PreSOCKS5: "socks5://hopuser:HOPPASS@hop.invalid:1080", PostSOCKS5: "http://postuser:POSTPASS@post.invalid:8080"}}
	c.Servers = []config.Server{{Name: "s", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 10000, Accounts: "alice", Params: map[string]string{"cipher": "aes-128-gcm"}}}
	if e := config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	parsed, e := subscription.ParseDetailed([]byte(testBody))
	if e != nil {
		t.Fatal(e)
	}
	if e = subscription.NewStore(filepath.Join(h, "subscriptions")).Save("p", subscription.Snapshot{Nodes: parsed.Nodes}); e != nil {
		t.Fatal(e)
	}
	rows, e := AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	show, e := AccountShow(h, "alice")
	if e != nil {
		t.Fatal(e)
	}
	providers, e := ProviderList(h)
	if e != nil {
		t.Fatal(e)
	}
	countries, e := ProviderCountries(h, "p")
	if e != nil {
		t.Fatal(e)
	}
	servers, e := ServerList(h)
	if e != nil {
		t.Fatal(e)
	}
	server, e := ServerShow(h, "s")
	if e != nil {
		t.Fatal(e)
	}
	secrets := []string{"FAKEHASH", "hopuser", "HOPPASS", "postuser", "POSTPASS", "subuser", "SUBTOKEN", "/private/token"}
	for _, v := range []any{rows, show, providers, countries, servers, server} {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		s := fmt.Sprintf("%+v %s", v, b)
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Errorf("output includes %q: %s", secret, s)
			}
		}
	}
}

func TestAccountUpdateCLIErrorParity(t *testing.T) {
	seed := func(t *testing.T, home string, mode string) []byte {
		t.Helper()
		c := config.Default()
		c.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.invalid"}}
		a := config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "p", ExitCountry: "DE"}
		if mode == "direct-conflict" {
			a.Exit = "direct"
		}
		if mode == "direct" {
			a.Provider = ""
			a.ExitCountry = ""
			a.Exit = "direct"
		}
		c.Accounts = []config.Account{a}
		if err := config.Save(config.ConfigPath(home), c); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name, mode string
		update     AccountUpdateOptions
		args       []string
		code       string
	}{
		{"invalid provider name", "", AccountUpdateOptions{Login: "alice", Provider: ptr("bad/name")}, []string{"set-provider", "alice", "bad/name"}, "USAGE"},
		{"missing provider", "", AccountUpdateOptions{Login: "alice", Provider: ptr("missing")}, []string{"set-provider", "alice", "missing"}, "SUB_NOT_FOUND"},
		{"invalid country", "", AccountUpdateOptions{Login: "alice", Country: ptr("NLD")}, []string{"set-country", "alice", "NLD"}, "COUNTRY_INVALID"},
		{"invalid clients", "", AccountUpdateOptions{Login: "alice", Clients: ptr("bad rule")}, []string{"set-clients", "alice", "bad rule"}, "CONFIG_INVALID"},
		{"invalid exit", "", AccountUpdateOptions{Login: "alice", Exit: ptr("bad")}, []string{"set-exit", "alice", "bad"}, config.CodeConfigInvalid},
		{"direct route conflict", "", AccountUpdateOptions{Login: "alice", Exit: ptr("direct")}, []string{"set-exit", "alice", "direct"}, config.CodeConfigInvalid},
		{"invalid pre", "", AccountUpdateOptions{Login: "alice", PreSOCKS5: ptr("bad")}, []string{"set-socks5", "alice", "--pre", "bad"}, "CONFIG_INVALID"},
		{"invalid post", "", AccountUpdateOptions{Login: "alice", PostSOCKS5: ptr("http://host")}, []string{"set-socks5", "alice", "--post", "http://host"}, "CONFIG_INVALID"},
		{"direct account country conflict", "direct", AccountUpdateOptions{Login: "alice", Country: ptr("NL")}, []string{"set-country", "alice", "NL"}, config.CodeConfigInvalid},
		{"direct account provider conflict", "direct", AccountUpdateOptions{Login: "alice", Provider: ptr("p")}, []string{"set-provider", "alice", "p"}, config.CodeConfigInvalid},
		{"direct account pre conflict", "direct", AccountUpdateOptions{Login: "alice", PreSOCKS5: ptr("socks5://proxy.invalid:1080")}, []string{"set-socks5", "alice", "--pre", "socks5://proxy.invalid:1080"}, config.CodeConfigInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h1, h2 := t.TempDir(), t.TempDir()
			before1 := seed(t, h1, tc.mode)
			before2 := seed(t, h2, tc.mode)
			_, opErr := AccountUpdate(h1, tc.update)
			out := &bytes.Buffer{}
			cliArgs := append(append([]string(nil), tc.args...), "--home", h2)
			cliErr := runAccount(cliArgs, out)
			var a, b *cli.Error
			if !errors.As(opErr, &a) || !errors.As(cliErr, &b) {
				t.Fatalf("errors not cli.Error: update=%v cli=%v", opErr, cliErr)
			}
			if a.Code != tc.code || a.Code != b.Code || a.Message != b.Message || a.Hint != b.Hint || a.Exit != b.Exit {
				t.Fatalf("update=%+v cli=%+v", a, b)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout=%q", out.String())
			}
			for _, h := range []string{h1, h2} {
				after, e := os.ReadFile(config.ConfigPath(h))
				if e != nil {
					t.Fatal(e)
				}
				want := before1
				if h == h2 {
					want = before2
				}
				if !bytes.Equal(after, want) {
					t.Fatalf("%s persisted failed update", h)
				}
			}
		})
	}
}
func mustReadConfig(t *testing.T, home string) []byte {
	t.Helper()
	b, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func ptr(s string) *string { return &s }
func codeIs(err error, code string) bool {
	var ce *cli.Error
	return errors.As(err, &ce) && ce.Code == code
}
