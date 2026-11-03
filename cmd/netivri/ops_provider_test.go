package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

const providerSecret = "provider-secret-never-return"

func providerSeed(t *testing.T, subs ...config.Subscription) string {
	t.Helper()
	a, _ := testApp(t, nil)
	a.home = t.TempDir()
	seedConfig(t, a, subs...)
	return a.home
}
func providerFetcher(b []byte) func(context.Context, string, fetch.Options) (*fetch.Result, error) {
	return func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return &fetch.Result{Body: b}, nil
	}
}
func assertNoSecret(t *testing.T, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), providerSecret) {
		t.Fatalf("secret: %s", b)
	}
	walkSecret(t, reflect.ValueOf(v))
}
func walkSecret(t *testing.T, v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if !v.IsNil() {
			walkSecret(t, v.Elem())
		}
		return
	}
	switch v.Kind() {
	case reflect.String:
		if strings.Contains(v.String(), providerSecret) {
			t.Fatal("secret in output")
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkSecret(t, v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkSecret(t, v.Index(i))
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			walkSecret(t, it.Key())
			walkSecret(t, it.Value())
		}
	}
}
func TestProviderAddTypedAndSecretRedaction(t *testing.T) {
	home := providerSeed(t)
	r, e := ProviderAdd(home, ProviderAddOptions{URL: "https://provider.invalid/token?key=" + providerSecret}, providerFetcher([]byte(testBody)), func() time.Time { return testNow })
	if e != nil || !r.Added || r.Name != "provider.invalid" {
		t.Fatalf("%+v %v", r, e)
	}
	assertNoSecret(t, r)
	list, e := ProviderList(home)
	if e != nil || len(list.Providers) != 1 || list.Providers[0].Host != "provider.invalid" {
		t.Fatalf("%+v %v", list, e)
	}
	assertNoSecret(t, list.Providers[0])
}
func TestProviderAddErrors(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "taken", URL: "https://taken.invalid"})
	_, e := ProviderAdd(home, ProviderAddOptions{URL: "https://new.invalid", Name: "bad/name"}, providerFetcher([]byte(testBody)), time.Now)
	var ce *cli.Error
	if !errors.As(e, &ce) || ce.Exit != cli.ExitUsage || ce.Code != "USAGE" {
		t.Fatalf("invalid name: %v", e)
	}
	_, e = ProviderAdd(home, ProviderAddOptions{URL: "https://new.invalid", Name: "taken"}, providerFetcher([]byte(testBody)), time.Now)
	if !errors.As(e, &ce) || ce.Code != codeSubExistsName {
		t.Fatalf("conflict: %v", e)
	}
	_, e = ProviderAdd(home, ProviderAddOptions{URL: "https://new.invalid"}, providerFetcher([]byte("not a subscription")), time.Now)
	if !errors.As(e, &ce) || ce.Code != "SUBSCRIPTION_EMPTY" {
		t.Fatalf("parse: %T %v", e, e)
	}

	home = t.TempDir()
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "safe"}
	}
	_, e = ProviderAdd(home, ProviderAddOptions{URL: "https://x.invalid/" + providerSecret}, get, time.Now)
	if !errors.As(e, &ce) || ce.Code != "FETCH_FAILED" {
		t.Fatalf("fetch: %v", e)
	}
	if _, e = os.Stat(config.ConfigPath(home)); !os.IsNotExist(e) {
		t.Fatalf("config changed: %v", e)
	}
}
func TestProviderRefreshWrapperDoesNotPrintEarlyErrors(t *testing.T) {
	app, out := testApp(t, nil)
	seedConfig(t, app, config.Subscription{Name: "known", URL: "https://known.invalid"})
	e := app.update([]string{"unknown"})
	var ce *cli.Error
	if !errors.As(e, &ce) || ce.Code != codeSubNotFound {
		t.Fatalf("err=%v", e)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout=%q", out.String())
	}
	if e = os.WriteFile(app.configPath(), []byte("subscriptions: ["), 0600); e != nil {
		t.Fatal(e)
	}
	e = app.update([]string{"known"})
	if !errors.As(e, &ce) || ce.Code != "CONFIG_INVALID" || out.Len() != 0 {
		t.Fatalf("config error output %q %v", out.String(), e)
	}
}
func TestProviderRefreshMixedFailurePreservesSnapshot(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "one", URL: "https://one.invalid"}, config.Subscription{Name: "two", URL: "https://two.invalid"})
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	old := subscription.Snapshot{FetchedAt: testNow, Nodes: []subscription.Node{{ID: "old"}}}
	if e := store.Save("two", old); e != nil {
		t.Fatal(e)
	}
	get := func(_ context.Context, u string, _ fetch.Options) (*fetch.Result, error) {
		if strings.Contains(u, "two") {
			return nil, errors.New("failed")
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	rs, e := ProviderRefresh(home, "", true, get, func() time.Time { return testNow })
	var ce *cli.Error
	if !errors.As(e, &ce) || ce.Code != codeSubUpdateFailed || len(rs) != 2 || !rs[0].OK || rs[1].OK {
		t.Fatalf("%+v %v", rs, e)
	}
	after, e := store.Load("two")
	if e != nil || after.Nodes[0].ID != "old" {
		t.Fatalf("snapshot %+v %v", after, e)
	}
}
func TestProviderRemove(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://x.invalid"})
	_, e := ProviderRemove(home, "absent")
	var ce *cli.Error
	if !errors.As(e, &ce) || ce.Code != codeSubNotFound {
		t.Fatalf("missing remove: %T %v", e, e)
	}
	cfg, e := loadConfig(home)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p"}}
	if e = config.Save(config.ConfigPath(home), cfg); e != nil {
		t.Fatal(e)
	}
	logins, e := ProviderRemove(home, "p")
	if !errors.As(e, &ce) || ce.Code != "SUB_IN_USE" || len(logins) != 1 || logins[0] != "alice" {
		t.Fatalf("%v %v", logins, e)
	}
	cfg, e = loadConfig(home)
	if e != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal("config changed")
	}
	cfg.Accounts = nil
	if e = config.Save(config.ConfigPath(home), cfg); e != nil {
		t.Fatal(e)
	}
	if _, e = ProviderRemove(home, "p"); e != nil {
		t.Fatal(e)
	}
}
func TestProviderCountries(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://x.invalid"}, config.Subscription{Name: "missing", URL: "https://y.invalid"})
	_, missingErr := ProviderCountries(home, "absent")
	var missing *cli.Error
	if !errors.As(missingErr, &missing) || missing.Code != codeSubNotFound {
		t.Fatalf("unknown provider: %T %v", missingErr, missingErr)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	parsed, e := subscription.ParseDetailed([]byte(testBody))
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Save("p", subscription.Snapshot{Nodes: parsed.Nodes}); e != nil {
		t.Fatal(e)
	}
	one, e := ProviderCountries(home, "p")
	if e != nil || one.Nodes == nil {
		t.Fatalf("%+v %v", one, e)
	}
	all, e := ProviderCountriesAll(home)
	if e != nil || len(all.Providers) != 2 || all.Providers[1].Nodes != nil {
		t.Fatalf("%+v %v", all, e)
	}
	if e = os.WriteFile(filepath.Join(home, "subscriptions", "p.ktav"), []byte("not valid ktav data"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e = ProviderCountries(home, "p")
	var ce *cli.Error
	if !errors.As(e, &ce) || ce.Code != "SUB_SNAPSHOT_INVALID" {
		t.Fatalf("corrupt: %v", e)
	}
}
func TestProviderCountriesURL(t *testing.T) {
	home := providerSeed(t)
	n, c, _, e := ProviderCountriesURL(home, "https://x.invalid/"+providerSecret, "", providerFetcher([]byte(testBody)))
	if e != nil || n == 0 || len(c) == 0 {
		t.Fatalf("%d %v %v", n, c, e)
	}
	if _, e = os.Stat(filepath.Join(home, "subscriptions")); e == nil {
		t.Fatal("URL fetch stored")
	}
	bad := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "safe failure", Exit: cli.ExitNetwork}
	}
	_, _, _, e = ProviderCountriesURL(home, "https://x.invalid/"+providerSecret, "", bad)
	var fetchErr *cli.Error
	if !errors.As(e, &fetchErr) || fetchErr.Code != "FETCH_FAILED" || strings.Contains(e.Error(), providerSecret) {
		t.Fatalf("fetch error: %T %v", e, e)
	}
}
func TestProviderReturnedTypesHaveNoCredentialFields(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(ProviderInfo{}), reflect.TypeOf(ProviderAddResult{}), reflect.TypeOf(ProviderRefreshResult{}), reflect.TypeOf(ProviderCountriesResult{})} {
		for i := 0; i < typ.NumField(); i++ {
			n := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(n, "url") || strings.Contains(n, "via") && n != "viaset" {
				t.Fatalf("credential field %s.%s", typ.Name(), n)
			}
		}
	}
}
