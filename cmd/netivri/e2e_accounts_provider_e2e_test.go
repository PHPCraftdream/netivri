//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// accountsRun runs any netivri subcommand against home. Failure messages
// never echo the arguments: they can carry a subscription URL.
func accountsRun(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eIOTimeout)
	defer cancel()
	full := append(append([]string{}, args...), "--home", home, "--json")
	cmd := exec.CommandContext(ctx, netivriBin, full...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
	var out, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("netivri command timed out")
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run netivri: %v", err)
		}
	}
	return code, out.String(), stderr.String()
}

// accountsSubServer serves a small Clash-YAML subscription on the loopback.
func accountsSubServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// accountsSocksNode builds a stored socks5 node pointing at a fake upstream.
func accountsSocksNode(id, country string, up *accountsUpstream) subscription.Node {
	host, portText, err := net.SplitHostPort(up.listener.Addr().String())
	if err != nil {
		panic(err)
	}
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	return subscription.Node{
		ID: id, Name: id, Type: "socks5", Server: host, Port: port, Country: country,
		Raw: map[string]any{"name": id, "type": "socks5", "server": host, "port": port},
	}
}

func accountsSaveSnapshot(t *testing.T, home, name string, nodes []subscription.Node) {
	t.Helper()
	store := subscription.NewStore(filepath.Join(home, "subscriptions"))
	if err := store.Save(name, subscription.Snapshot{FetchedAt: time.Now().UTC(), Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
}

func TestE2EAccountsProviderRouting(t *testing.T) {
	home := t.TempDir()
	pass := "provider-e2e-password"
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsEcho(t)
	p1Up := accountsServer(t)
	p2Up := accountsServer(t)
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "p1", ExitCountry: "DE"},
			{Login: "bob", Password: hash, Provider: "p2", ExitCountry: "DE"},
			{Login: "carol", Password: hash, ExitCountry: "DE"},
			{Login: "dave", Password: hash, Provider: "p2", ExitCountry: "JP"},
		}
		cfg.Subscriptions = []config.Subscription{{Name: "p1", URL: "https://p1.invalid/sub"}, {Name: "p2", URL: "https://p2.invalid/sub"}}
	})
	accountsSaveSnapshot(t, home, "p1", []subscription.Node{accountsSocksNode("de-p1", "DE", p1Up)})
	accountsSaveSnapshot(t, home, "p2", []subscription.Node{accountsSocksNode("nl-p2", "NL", p2Up), accountsSocksNode("de-p2", "DE", p2Up)})

	// dave's provider has no JP node: `up` must fail closed before binding.
	code, out, stderr := runNetivri(t, home, "--port", "0")
	if code != 5 {
		t.Fatalf("up exit = %d, want 5 (stderr %s)", code, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "NO_NODES_FOR_COUNTRY" {
		t.Errorf("error code = %q, want NO_NODES_FOR_COUNTRY", got)
	}
	if strings.Contains(stderr, "p2.invalid") {
		t.Errorf("URL leaked: %s", stderr)
	}

	// Drop dave and start the angel.
	code, out, stderr = accountsCommand(t, home, "rm", "dave")
	if code != 0 {
		t.Fatalf("account rm: %s%s", out, stderr)
	}
	p := startUp(t, home, "--port", "0")
	st := p.Status()
	assertStatusSuccess(t, st)
	listen := st.Data.Listen
	accountsClient(t, listen, "alice", pass, backend)
	if p1Up.count.Load() != 1 || p2Up.count.Load() != 0 {
		t.Fatalf("alice upstream counts p1=%d p2=%d", p1Up.count.Load(), p2Up.count.Load())
	}
	accountsClient(t, listen, "bob", pass, backend)
	if p1Up.count.Load() != 1 || p2Up.count.Load() != 1 {
		t.Fatalf("bob upstream counts p1=%d p2=%d", p1Up.count.Load(), p2Up.count.Load())
	}
	// carol (DE, any provider) may use either DE node: the first connection
	// binds the site to one of them and every further one follows it.
	accountsClient(t, listen, "carol", pass, backend)
	p1After, p2After := p1Up.count.Load(), p2Up.count.Load()
	if p1After+p2After != 3 || p1After < 1 || p2After < 1 {
		t.Fatalf("carol must use exactly one DE node: counts p1=%d p2=%d (before 1/1)", p1After, p2After)
	}
	accountsClient(t, listen, "carol", pass, backend)
	if p1Up.count.Load()+p2Up.count.Load() != 4 {
		t.Fatalf("carol's second connection did not reach a node: p1=%d p2=%d", p1Up.count.Load(), p2Up.count.Load())
	}
	if (p1Up.count.Load() == p1After+1) == (p2Up.count.Load() == p2After+1) {
		t.Fatalf("carol's site moved between nodes: p1 %d->%d p2 %d->%d",
			p1After, p1Up.count.Load(), p2After, p2Up.count.Load())
	}
	p.Stop()
	assertNoSecrets(t, p.Stdout(), p.Stderr(), pass, hash)
}

func TestE2ESubCountriesProviderForms(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://main.invalid/sub"}}
	})
	accountsSaveSnapshot(t, home, "main", []subscription.Node{
		{ID: "1", Name: "Frankfurt 01", Type: "ss", Server: "example.invalid", Port: 8388, Country: "DE"},
		{ID: "2", Name: "Amsterdam 02", Type: "ss", Server: "example.invalid", Port: 8389, Country: "NL"},
		{ID: "3", Name: "Mystery", Type: "ss", Server: "example.invalid", Port: 8390},
	})

	// NAME form: the stored snapshot, no network.
	code, out, stderr := accountsRun(t, home, "sub", "countries", "main")
	if code != 0 {
		t.Fatalf("sub countries main: %s%s", out, stderr)
	}
	var named struct {
		Data struct {
			Provider  string `json:"provider"`
			Nodes     int    `json:"nodes"`
			Countries []struct {
				Code  string `json:"code"`
				Nodes int    `json:"nodes"`
			} `json:"countries"`
			Unknown int `json:"unknown"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &named); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if named.Data.Provider != "main" || named.Data.Nodes != 3 || named.Data.Unknown != 1 ||
		len(named.Data.Countries) != 2 || named.Data.Countries[0].Code != "DE" || named.Data.Countries[0].Nodes != 1 ||
		named.Data.Countries[1].Code != "NL" || named.Data.Countries[1].Nodes != 1 {
		t.Fatalf("unexpected data: %s", out)
	}

	// No-argument form: every configured provider plus the union.
	code, out, stderr = accountsRun(t, home, "sub", "countries")
	if code != 0 {
		t.Fatalf("sub countries: %s%s", out, stderr)
	}
	var all struct {
		Data struct {
			Providers []struct {
				Name  string `json:"name"`
				Nodes *int   `json:"nodes"`
			} `json:"providers"`
			Countries []struct {
				Code  string `json:"code"`
				Nodes int    `json:"nodes"`
			} `json:"countries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(all.Data.Providers) != 1 || all.Data.Providers[0].Name != "main" || all.Data.Providers[0].Nodes == nil || *all.Data.Providers[0].Nodes != 3 {
		t.Fatalf("unexpected providers: %s", out)
	}
	if len(all.Data.Countries) != 2 || all.Data.Countries[0].Code != "DE" || all.Data.Countries[0].Nodes != 1 ||
		all.Data.Countries[1].Code != "NL" || all.Data.Countries[1].Nodes != 1 {
		t.Fatalf("unexpected union: %s", out)
	}

	// Unknown name: SUB_NOT_FOUND, exit 5.
	code, out, stderr = accountsRun(t, home, "sub", "countries", "ghost")
	if code != 5 {
		t.Fatalf("sub countries ghost exit = %d (stdout %s stderr %s)", code, out, stderr)
	}
	if got := decodeErrEnvelope(t, stderr).Error.Code; got != "SUB_NOT_FOUND" {
		t.Errorf("error code = %q, want SUB_NOT_FOUND", got)
	}

	// --url form against a local HTTP server: fetch, parse, store nothing.
	const subBody = `proxies:
  - {name: "Frankfurt 01", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "Amsterdam 02", type: ss, server: example.invalid, port: 8389, cipher: aes-128-gcm, password: pass}
  - {name: "Mystery", type: ss, server: example.invalid, port: 8390, cipher: aes-128-gcm, password: pass}
`
	url := accountsSubServer(t, subBody)
	cfgBefore, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr = accountsRun(t, home, "sub", "countries", "--url", url)
	if code != 0 {
		t.Fatalf("sub countries --url: %s%s", out, stderr)
	}
	var fetched struct {
		Data struct {
			Nodes     int `json:"nodes"`
			Unknown   int `json:"unknown"`
			Countries []struct {
				Code  string `json:"code"`
				Nodes int    `json:"nodes"`
			} `json:"countries"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &fetched); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if fetched.Data.Nodes != 3 || fetched.Data.Unknown != 1 ||
		len(fetched.Data.Countries) != 2 || fetched.Data.Countries[0].Code != "DE" || fetched.Data.Countries[0].Nodes != 1 ||
		fetched.Data.Countries[1].Code != "NL" || fetched.Data.Countries[1].Nodes != 1 {
		t.Fatalf("unexpected data: %s", out)
	}
	// The fetch stored nothing and touched no config.
	cfgAfter, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Fatal("the config file changed")
	}
	names, err := os.ReadDir(filepath.Join(home, "subscriptions"))
	if err != nil || len(names) != 1 || names[0].Name() != "main.ktav" {
		t.Fatalf("subscription store changed: %v (err %v)", names, err)
	}
	assertNoSecrets(t, out, stderr, url, "main.invalid")
}
