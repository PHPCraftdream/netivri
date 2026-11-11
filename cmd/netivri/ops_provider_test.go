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
func TestProviderHopUpdateCandidateRollback(t *testing.T) {
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://provider.invalid", PreSOCKS5: config.HopList{"socks5://first.example:1080"}}}
	before := *cfg
	before.Subscriptions = append([]config.Subscription(nil), cfg.Subscriptions...)
	called := false
	replacement := config.HopList{"none"}
	err := providerUpdateWith(cfg, "p", ProviderUpdateOptions{Pre: &replacement, PreEdit: &HopEdit{Remove: []int{1}}}, func(*config.Config) error {
		called = true
		return nil
	})
	if err == nil || called || !reflect.DeepEqual(cfg, before) {
		t.Fatal("conflicting provider edit mutated or saved configuration")
	}

	edit := HopEdit{Remove: []int{1}}
	err = providerUpdateWith(cfg, "p", ProviderUpdateOptions{PreEdit: &edit}, func(candidate *config.Config) error {
		called = true
		if len(candidate.Subscriptions[0].PreSOCKS5) != 0 || len(cfg.Subscriptions[0].PreSOCKS5) != 1 {
			t.Fatal("provider update did not validate an isolated candidate")
		}
		return errors.New("injected save failure")
	})
	if err == nil || !called || !reflect.DeepEqual(cfg, before) {
		t.Fatal("failed provider save changed source config")
	}
}

func TestProviderAddExistingURLIgnoresHopFlagsAndRefreshesSnapshot(t *testing.T) {
	pre := "socks5://pre-user:" + providerSecret + "@first.example:1080"
	post := "https://post-user:" + providerSecret + "@last.example:8443"
	home := providerSeed(t, config.Subscription{
		Name:       "p",
		URL:        "https://provider.invalid",
		PreSOCKS5:  config.HopList{pre},
		PostSOCKS5: config.HopList{post},
	})
	beforeCfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	oldSnapshot := subscription.Snapshot{
		FetchedAt: testNow.Add(-time.Hour),
		Nodes:     []subscription.Node{{ID: "stale"}},
	}
	if err := store.Save("p", oldSnapshot); err != nil {
		t.Fatal(err)
	}
	fetchCalls := 0
	get := func(_ context.Context, raw string, _ fetch.Options) (*fetch.Result, error) {
		fetchCalls++
		if raw != "https://provider.invalid" {
			t.Fatal("re-fetch did not use the configured provider URL")
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	providedPre := config.HopList{"http://different-pre.example:8080"}
	providedPost := config.HopList{"socks5://different-post.example:1080"}
	result, err := ProviderAdd(home, ProviderAddOptions{
		URL:  "https://provider.invalid",
		Pre:  providedPre,
		Post: providedPost,
	}, get, func() time.Time { return testNow })
	if err != nil || !result.Existing || result.Added || result.Name != "p" {
		t.Fatalf("result = %+v, err = %v; want existing provider success", result, err)
	}
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d; want 1", fetchCalls)
	}
	assertNoSecret(t, result)

	afterCfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterCfg, beforeCfg) {
		t.Fatal("re-adding the provider changed its config")
	}
	if !reflect.DeepEqual(afterCfg.Subscriptions[0].PreSOCKS5, config.HopList{pre}) || !reflect.DeepEqual(afterCfg.Subscriptions[0].PostSOCKS5, config.HopList{post}) {
		t.Fatal("re-adding the provider changed its stored hops")
	}
	refreshed, err := store.Load("p")
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.FetchedAt.Equal(testNow) || len(refreshed.Nodes) != 3 || refreshed.Nodes[0].ID == "stale" {
		t.Fatalf("snapshot was not refreshed: fetched_at=%s nodes=%d", refreshed.FetchedAt, len(refreshed.Nodes))
	}
}

func TestProviderAddSaveFailureRemovesNewSnapshot(t *testing.T) {
	home := providerSeed(t)
	_, err := providerAddWith(home, ProviderAddOptions{URL: "https://provider.invalid", Name: "provider"}, providerFetcher([]byte(testBody)), func() time.Time { return testNow }, func(_ string, _ *config.Config) error {
		return errors.New("injected save failure")
	})
	if err == nil {
		t.Fatal("provider add reported success after injected config save failure")
	}
	if _, err := subscription.NewStore(filepath.Join(home, "subscriptions")).Load("provider"); !os.IsNotExist(err) {
		t.Fatal("provider snapshot was not rolled back after config save failure")
	}
	if _, err := os.Stat(config.ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatal("provider config was written despite injected save failure")
	}
}

func TestProviderUpdatePreservesReferencesAndSnapshot(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid/token", Via: "http://proxy.invalid:8080"})
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	old := subscription.Snapshot{FetchedAt: testNow, Nodes: []subscription.Node{{ID: "cached"}}}
	if err := store.Save("p", old); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load("p")
	if err != nil {
		t.Fatal(err)
	}
	newURL := "https://new.invalid/token"
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{URL: &newURL}); err != nil {
		t.Fatal(err)
	}
	clearVia := ""
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{Via: &clearVia}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subscriptions[0].URL != newURL || cfg.Subscriptions[0].Via != "" || len(cfg.Accounts) != 1 || cfg.Accounts[0].Provider != "p" {
		t.Fatal("provider settings or account reference changed unexpectedly")
	}
	cached, err := store.Load("p")
	if err != nil || !reflect.DeepEqual(cached, before) {
		t.Fatal("provider update changed cached snapshot")
	}
}

func TestProviderUpdateEditsURLAndVia(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid/token", Via: ""}, config.Subscription{Name: "other", URL: "https://other.invalid"})
	newURL, via := "https://new.invalid/secret", "http://proxy.invalid:8080"
	if err := ProviderUpdate(home, "p", ProviderUpdateOptions{URL: &newURL, Via: &via}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Subscriptions[0].URL != newURL || cfg.Subscriptions[0].Via != via || cfg.Subscriptions[1].Name != "other" {
		t.Fatal("provider update did not preserve unrelated settings")
	}
}

func TestProviderUpdateErrorsDoNotPersistAndRedact(t *testing.T) {
	home := providerSeed(t, config.Subscription{Name: "p", URL: "https://old.invalid"})
	readConfig := func() *config.Config {
		cfg, err := loadConfig(home)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	before := readConfig()
	secret := "gopher://" + providerSecret
	cases := []struct {
		name    string
		options ProviderUpdateOptions
		code    string
	}{
		{"rename", ProviderUpdateOptions{NewName: providerUpdateString("renamed")}, codeSubExistsName},
		{"unknown scheme", ProviderUpdateOptions{URL: &secret}, "CONFIG_INVALID"},
		{"missing host", ProviderUpdateOptions{URL: providerUpdateString("https:///path")}, "CONFIG_INVALID"},
		{"missing scheme", ProviderUpdateOptions{URL: providerUpdateString("//provider.invalid/path")}, "CONFIG_INVALID"},
		{"malformed URL", ProviderUpdateOptions{URL: providerUpdateString("https://[invalid")}, "CONFIG_INVALID"},
		{"empty URL", ProviderUpdateOptions{URL: providerUpdateString("")}, "CONFIG_INVALID"},
		{"whitespace URL", ProviderUpdateOptions{URL: providerUpdateString(" \t\n")}, "CONFIG_INVALID"},
		{"surrounding whitespace", ProviderUpdateOptions{URL: providerUpdateString(" https://provider.invalid")}, "CONFIG_INVALID"},
		{"control character", ProviderUpdateOptions{URL: providerUpdateString("https://provider.invalid/\npath")}, "CONFIG_INVALID"},
		{"invalid via", ProviderUpdateOptions{Via: providerUpdateString("gopher://" + providerSecret)}, "CONFIG_INVALID"},
	}
	for _, tc := range cases {
		err := ProviderUpdate(home, "p", tc.options)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != tc.code {
			t.Fatal("provider update returned unexpected error")
		}
		if strings.Contains(err.Error(), providerSecret) {
			t.Fatalf("%s leaked secret", tc.name)
		}
		if !reflect.DeepEqual(before, readConfig()) {
			t.Fatal("provider update changed config")
		}
	}
	err := ProviderUpdate(home, "absent", ProviderUpdateOptions{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != codeSubNotFound {
		t.Fatalf("missing provider: %v", err)
	}
}

func providerUpdateString(s string) *string { return &s }

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
func TestProviderRefreshEmptyConfigSucceedsWithoutFetching(t *testing.T) {
	home := providerSeed(t)
	fetchCalled := false
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		fetchCalled = true
		return &fetch.Result{Body: []byte(testBody)}, nil
	}

	results, err := ProviderRefresh(home, "", false, get, time.Now)
	if err != nil || results == nil || len(results) != 0 {
		t.Fatalf("results = %#v, err = %v; want empty successful results", results, err)
	}
	if fetchCalled {
		t.Fatal("empty provider configuration reached provider fetch")
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
