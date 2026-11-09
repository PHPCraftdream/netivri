package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

type ProviderInfo struct {
	Name, Host string
	ViaSet     bool
	// Pre and Post are the provider-level hops without credentials; "" inherits
	// the global chain, "none" disables the hop.
	Pre, Post string
	Nodes     *int
	FetchedAt *string
	Quota     map[string]any
	Countries []subscription.CountryCount
	Unknown   *int
	State     string
}
type ProviderListResult struct{ Providers []ProviderInfo }
type ProviderAddOptions struct{ URL, Name, Via, Pre, Post string }

// ProviderUpdateOptions: a nil field is left as it is; an empty Pre or Post
// returns that hop to the global chain.
type ProviderUpdateOptions struct{ NewName, URL, Via, Pre, Post *string }

func validProviderURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ProviderUpdate edits a provider's connection settings without fetching it.
// Renames are refused because config references and stored snapshots cannot be
// updated atomically by this operation.
func ProviderUpdate(home, name string, o ProviderUpdateOptions) error {
	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}
	idx := -1
	for i := range cfg.Subscriptions {
		if cfg.Subscriptions[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	if o.NewName != nil && *o.NewName != name {
		return &cli.Error{Code: codeSubExistsName, Message: "provider rename is unavailable because related configuration and stored data cannot be updated safely", Exit: cli.ExitConflict}
	}
	if o.URL != nil {
		if !validProviderURL(*o.URL) {
			return &cli.Error{Code: "CONFIG_INVALID", Message: "invalid subscription URL"}
		}
		cfg.Subscriptions[idx].URL = *o.URL
	}
	if o.Via != nil {
		cfg.Subscriptions[idx].Via = *o.Via
	}
	if o.Pre != nil {
		cfg.Subscriptions[idx].PreSOCKS5 = *o.Pre
	}
	if o.Post != nil {
		cfg.Subscriptions[idx].PostSOCKS5 = *o.Post
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "save config failed", Exit: cli.ExitInternal}
	}
	return nil
}

// ProviderRename renames a provider, rewriting every account's provider list
// member and moving the stored snapshot so references keep resolving. Nothing
// is written when validation fails. Case-only renames are rejected because
// snapshot names alias on case-insensitive filesystems. A leftover old snapshot
// file is harmless: it is orphaned data, the config no longer references it.
func ProviderRename(home, oldName, newName string) error {
	if !validSubName(newName) {
		return cli.Usage("sub rename: --name must match [A-Za-z0-9._-]{1,64}")
	}
	if strings.EqualFold(oldName, newName) {
		return cli.Usage("sub rename: the new name must differ from the current name")
	}
	cfg, e := loadConfig(home)
	if e != nil {
		return e
	}
	idx := -1
	for i := range cfg.Subscriptions {
		if cfg.Subscriptions[i].Name == oldName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	for _, s := range cfg.Subscriptions {
		if s.Name == newName {
			return &cli.Error{Code: codeSubExistsName, Message: "subscription name is already used", Exit: cli.ExitConflict}
		}
	}
	// Move the snapshot before anything else is written: a failure here
	// leaves the old config and the old snapshot untouched.
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	snap, err := store.Load(oldName)
	hasSnap := false
	switch {
	case err == nil:
		hasSnap = true
		if err = store.Save(newName, *snap); err != nil {
			return &cli.Error{Code: "INTERNAL", Message: "store subscription: " + err.Error(), Exit: cli.ExitInternal}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return snapshotError(oldName)
	}
	cleanupSnap := func() {
		if hasSnap {
			_ = store.Remove(newName)
		}
	}
	cfg.Subscriptions[idx].Name = newName
	for i := range cfg.Accounts {
		p, ok := config.ParseProviderList(cfg.Accounts[i].Provider)
		if !ok || !hasProvider(p, oldName) {
			continue
		}
		for j := range p {
			if p[j] == oldName {
				p[j] = newName
			}
		}
		cfg.Accounts[i].Provider = config.JoinList(p)
	}
	if e = cfg.Validate(); e != nil {
		cleanupSnap()
		return e
	}
	if e = config.Save(config.ConfigPath(home), cfg); e != nil {
		cleanupSnap()
		return &cli.Error{Code: "INTERNAL", Message: "save config: " + e.Error(), Exit: cli.ExitInternal}
	}
	if err = store.Remove(oldName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return nil
}

type ProviderAddResult struct {
	Name            string
	Added, Existing bool
	Nodes, Skipped  int
	Countries       map[string]int
}
type ProviderRefreshResult struct {
	Name           string
	OK             bool
	Nodes, Skipped *int
	Quota          map[string]any
	Error          map[string]string
}
type ProviderCountriesResult struct {
	Name      string
	Nodes     *int
	Countries []subscription.CountryCount
	Unknown   *int
}
type ProviderCountriesAllResult struct {
	Providers []ProviderCountriesResult
	Countries []subscription.CountryCount
}

func ProviderList(home string) (ProviderListResult, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return ProviderListResult{}, e
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	out := ProviderListResult{Providers: make([]ProviderInfo, 0, len(cfg.Subscriptions))}
	for _, s := range cfg.Subscriptions {
		p := ProviderInfo{Name: s.Name, Host: urlHost(s.URL), ViaSet: s.Via != "", Pre: redactChain(s.PreSOCKS5), Post: redactChain(s.PostSOCKS5), State: "missing"}
		snap, err := store.Load(s.Name)
		if err == nil {
			n := len(snap.Nodes)
			ts := snap.FetchedAt.Format(time.RFC3339)
			p.Nodes = &n
			p.FetchedAt = &ts
			p.Quota = quotaView(snap.Quota)
			counts, u := subscription.CountryCounts(snap.Nodes)
			p.Countries = counts
			p.Unknown = &u
			p.State = "stored"
		} else if !errors.Is(err, os.ErrNotExist) {
			return ProviderListResult{}, snapshotError(s.Name)
		}
		out.Providers = append(out.Providers, p)
	}
	return out, nil
}
func ProviderAdd(home string, o ProviderAddOptions, get func(context.Context, string, fetch.Options) (*fetch.Result, error), now func() time.Time) (ProviderAddResult, error) {
	if o.Name != "" && !validSubName(o.Name) {
		return ProviderAddResult{}, cli.Usage("sub add: --name must match [A-Za-z0-9._-]{1,64}")
	}
	cfg, e := loadConfig(home)
	if e != nil {
		return ProviderAddResult{}, e
	}
	existing := false
	name := o.Name
	if name == "" {
		name = deriveName(o.URL, cfg.Subscriptions)
	}
	for _, s := range cfg.Subscriptions {
		if s.URL == o.URL {
			existing = true
			name = s.Name
			break
		}
	}
	if !existing && o.Name != "" {
		for _, s := range cfg.Subscriptions {
			if s.Name == name {
				return ProviderAddResult{}, &cli.Error{Code: codeSubExistsName, Message: "subscription name is already used by a different URL", Hint: "pick another --name or remove the existing subscription with `netivri sub rm`", Exit: cli.ExitConflict}
			}
		}
	}
	snap, e := fetchSnapshot(context.Background(), get, now, cfg, config.Subscription{Name: name, URL: o.URL, Via: o.Via})
	if e != nil {
		return ProviderAddResult{}, e
	}
	if e = subscription.NewStore(filepath.Join(home, "subscriptions")).Save(name, snap); e != nil {
		return ProviderAddResult{}, &cli.Error{Code: "INTERNAL", Message: "store subscription: " + e.Error(), Exit: cli.ExitInternal}
	}
	if !existing {
		cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: name, URL: o.URL, Via: o.Via, PreSOCKS5: o.Pre, PostSOCKS5: o.Post})
		if e = cfg.Validate(); e != nil {
			return ProviderAddResult{}, e
		}
		if e = config.Save(config.ConfigPath(home), cfg); e != nil {
			return ProviderAddResult{}, &cli.Error{Code: "INTERNAL", Message: "save config: " + e.Error(), Exit: cli.ExitInternal}
		}
	}
	counts := map[string]int{}
	for _, n := range snap.Nodes {
		c := n.Country
		if c == "" {
			c = "unknown"
		}
		counts[c]++
	}
	return ProviderAddResult{Name: name, Added: !existing, Existing: existing, Nodes: len(snap.Nodes), Skipped: snap.Skipped, Countries: counts}, nil
}
func ProviderRefresh(home, name string, all bool, get func(context.Context, string, fetch.Options) (*fetch.Result, error), now func() time.Time) ([]ProviderRefreshResult, error) {
	if all && name != "" {
		return nil, cli.Usage("sub update: pass a name or --all, not both")
	}
	cfg, e := loadConfig(home)
	if e != nil {
		return nil, e
	}
	var targets []config.Subscription
	if name != "" {
		for _, s := range cfg.Subscriptions {
			if s.Name == name {
				targets = append(targets, s)
				break
			}
		}
		if targets == nil {
			return nil, &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
		}
	} else {
		targets = cfg.Subscriptions
	}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	results := make([]ProviderRefreshResult, 0, len(targets))
	failed := false
	for _, s := range targets {
		r := ProviderRefreshResult{Name: s.Name}
		snap, err := fetchSnapshot(context.Background(), get, now, cfg, s)
		if err != nil {
			var ce *cli.Error
			if !errors.As(err, &ce) {
				ce = &cli.Error{Code: "INTERNAL", Message: err.Error()}
			}
			r.Error = map[string]string{"code": ce.Code, "message": ce.Message}
			failed = true
		} else if err = store.Save(s.Name, snap); err != nil {
			r.Error = map[string]string{"code": "INTERNAL", "message": "store subscription: " + err.Error()}
			failed = true
		} else {
			n, k := len(snap.Nodes), snap.Skipped
			r.OK = true
			r.Nodes = &n
			r.Skipped = &k
			r.Quota = quotaView(snap.Quota)
		}
		results = append(results, r)
	}
	if failed {
		return results, &cli.Error{Code: codeSubUpdateFailed, Message: "one or more subscriptions failed to update", Exit: cli.ExitNetwork}
	}
	return results, nil
}
func ProviderRemove(home, name string) ([]string, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return nil, e
	}
	idx := -1
	for i, s := range cfg.Subscriptions {
		if s.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	var logins []string
	for _, ac := range cfg.Accounts {
		p, _ := config.ParseProviderList(ac.Provider)
		if slices.Contains(p, name) {
			logins = append(logins, ac.Login)
		}
	}
	if len(logins) > 0 {
		return logins, &cli.Error{Code: "SUB_IN_USE", Message: fmt.Sprintf("subscription %q is bound to an account", name), Hint: "netivri account set-provider LOGIN --none", Exit: cli.ExitConflict}
	}
	cfg.Subscriptions = append(cfg.Subscriptions[:idx], cfg.Subscriptions[idx+1:]...)
	if e = cfg.Validate(); e != nil {
		return nil, e
	}
	if e = config.Save(config.ConfigPath(home), cfg); e != nil {
		return nil, &cli.Error{Code: "INTERNAL", Message: "save config: " + e.Error(), Exit: cli.ExitInternal}
	}
	if e = subscription.NewStore(filepath.Join(home, "subscriptions")).Remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, &cli.Error{Code: "INTERNAL", Message: "remove stored subscription: " + e.Error(), Exit: cli.ExitInternal}
	}
	return nil, nil
}
func countryEntry(name string, snap *subscription.Snapshot, err error) (ProviderCountriesResult, error) {
	r := ProviderCountriesResult{Name: name}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return r, snapshotError(name)
	}
	counts, u := subscription.CountryCounts(snap.Nodes)
	n := len(snap.Nodes)
	r.Nodes = &n
	r.Countries = counts
	r.Unknown = &u
	return r, nil
}
func ProviderCountries(home, name string) (ProviderCountriesResult, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return ProviderCountriesResult{}, e
	}
	found := false
	for _, s := range cfg.Subscriptions {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		return ProviderCountriesResult{}, &cli.Error{Code: codeSubNotFound, Message: "no subscription with that name", Exit: cli.ExitNotFound}
	}
	snap, err := subscription.NewStore(filepath.Join(home, "subscriptions")).Load(name)
	return countryEntry(name, snap, err)
}
func ProviderCountriesAll(home string) (ProviderCountriesAllResult, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return ProviderCountriesAllResult{}, e
	}
	r := ProviderCountriesAllResult{Providers: make([]ProviderCountriesResult, 0, len(cfg.Subscriptions))}
	union := map[string]int{}
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	for _, s := range cfg.Subscriptions {
		snap, err := store.Load(s.Name)
		entry, err := countryEntry(s.Name, snap, err)
		if err != nil {
			return ProviderCountriesAllResult{}, err
		}
		if entry.Countries != nil {
			for _, c := range entry.Countries {
				union[c.Code] += c.Nodes
			}
		}
		r.Providers = append(r.Providers, entry)
	}
	r.Countries = make([]subscription.CountryCount, 0, len(union))
	for c, n := range union {
		r.Countries = append(r.Countries, subscription.CountryCount{Code: c, Nodes: n})
	}
	sort.Slice(r.Countries, func(i, j int) bool { return r.Countries[i].Code < r.Countries[j].Code })
	return r, nil
}
func ProviderCountriesURL(home, rawURL, via string, get func(context.Context, string, fetch.Options) (*fetch.Result, error)) (int, []subscription.CountryCount, int, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return 0, nil, 0, e
	}
	res, e := get(context.Background(), rawURL, fetch.Options{Proxy: proxyFor(via, cfg.Fetch.Proxy)})
	if e != nil {
		return 0, nil, 0, e
	}
	parsed, e := subscription.ParseDetailed(res.Body)
	if e != nil {
		return 0, nil, 0, e
	}
	counts, u := subscription.CountryCounts(parsed.Nodes)
	return len(parsed.Nodes), counts, u, nil
}
