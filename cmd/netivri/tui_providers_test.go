package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

func providerEnv(h string) tui.Env {
	return tui.Env{Home: h, Background: func(_ string, w func() tea.Msg) tea.Cmd { return w }}
}
func drainProvider(s *providersScreen, c tea.Cmd) {
	queue := []tea.Cmd{c}
	for steps := 0; len(queue) > 0 && steps < 80; steps++ {
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		m := cmd()
		if batch, ok := m.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		_, next := s.Update(m)
		if next != nil {
			queue = append(queue, next)
		}
	}
	if len(queue) > 0 {
		panic("provider command loop exceeded")
	}
}
func TestProvidersEmptyReloadBounds(t *testing.T) {
	h := providerSeed(t)
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	if s.Init() != nil {
		t.Fatal("init")
	}
	if !strings.Contains(s.View(30, 3), "No rows") {
		t.Fatal("empty")
	}
	s.Update(tea.WindowSizeMsg{Width: 4, Height: 2})
	if s.width != 4 || s.height != 2 {
		t.Fatal("resize")
	}
	if s.View(0, 2) != "" || s.View(3, 0) != "" {
		t.Fatal("bounds")
	}
	_, c := s.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	drainProvider(s, c)
	if s.busy != "" {
		t.Fatal("reload")
	}
}
func TestProviderAddRetainedErrorsSecretsAndPersist(t *testing.T) {
	h := providerSeed(t)
	calls := 0
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		calls++
		if calls == 1 {
			return nil, &cli.Error{Code: "FETCH_FAILED", Message: "failed token", Hint: "retry token"}
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	toast := ""
	env := providerEnv(h)
	env.Toast = func(v string) tea.Cmd { toast = v; return nil }
	s := newProvidersScreenWith(h, env, get, func() time.Time { return testNow })
	s.Update(tea.KeyPressMsg{Text: "a"})
	s.form.Fields[0].Value = "custom"
	s.form.Fields[1].Value = "https://name:token@provider.invalid/path?api_key=token"
	s.form.Fields[2].Value = "http://u:proxypass@proxy.invalid"
	s.form.Focus = 2
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	v := ansi.Strip(s.View(100, 20))
	if !strings.Contains(v, "failed [redacted]") || !strings.Contains(v, "retry [redacted]") {
		t.Fatalf("errors %s", v)
	}
	for _, secret := range []string{"token", "proxypass", "https://name"} {
		if strings.Contains(v, secret) {
			t.Fatalf("leak %s", v)
		}
	}
	if s.form.Fields[1].Value != "https://name:token@provider.invalid/path?api_key=token" || s.form.Fields[2].Value != "http://u:proxypass@proxy.invalid" {
		t.Fatal("form not retained")
	}
	s.form.Focus = 2
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	p, e := ProviderList(h)
	if e != nil || len(p.Providers) != 1 || p.Providers[0].Name != "custom" {
		t.Fatalf("provider %+v %v", p, e)
	}
	if toast == "" || !strings.Contains(toast, "nodes") || s.form != nil || len(s.table.Rows) != 1 || s.table.Rows[0].Data != "custom" {
		t.Fatal("success state")
	}
	snap, e := subscription.NewStore(h + "/subscriptions").Load("custom")
	if e != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("timestamp %v %v", snap.FetchedAt, e)
	}
}
func TestProviderRefreshAllAndSelected(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "one", URL: "https://one.invalid"}, config.Subscription{Name: "two", URL: "https://two.invalid"})
	calls := 0
	get := func(_ context.Context, u string, _ fetch.Options) (*fetch.Result, error) {
		calls++
		if strings.Contains(u, "two") {
			return nil, errors.New("secret detail")
		}
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	drainProvider(s, s.refresh("", true))
	if len(s.refreshLines) != 2 || strings.Contains(strings.Join(s.refreshLines, " "), "secret detail") {
		t.Fatalf("refresh %v", s.refreshLines)
	}
	if _, e := subscription.NewStore(h + "/subscriptions").Load("one"); e != nil {
		t.Fatal(e)
	}
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "one"}, {Name: "two"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	drainProvider(s, c)
	if len(s.refreshLines) != 1 || !strings.HasPrefix(s.refreshLines[0], "one:") || calls != 3 {
		t.Fatalf("selected %v calls %d", s.refreshLines, calls)
	}
}
func TestProviderRemoveActualPreflightConfirmNoAndDependencies(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainProvider(s, c)
	if !strings.Contains(ansi.Strip(s.View(120, 3)), "Remove provider p?") {
		t.Fatal("missing confirmation prompt")
	}
	_, c = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	cfg, err := loadConfig(h)
	if err != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal("Enter changed config")
	}
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "p"}}
	if err = config.Save(config.ConfigPath(h), cfg); err != nil {
		t.Fatal(err)
	}
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c = s.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainProvider(s, c)
	if !strings.Contains(ansi.Strip(s.View(120, 3)), "alice") {
		t.Fatal("dependent account missing from prompt")
	}
	frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Providers", New: func(tui.Env) tui.Screen { return s }}}})
	_, c = frame.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if c == nil {
		t.Fatal("y not forwarded")
	}
	msg := c()
	if msg == nil {
		t.Fatal("y result missing")
	}
	_, c = frame.Update(msg)
	drainProvider(s, c)
	if !strings.Contains(s.message, "alice") {
		t.Fatalf("dependency refusal %q", s.message)
	}
	cfg, err = loadConfig(h)
	if err != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal("dependent provider removed")
	}
	_, c = s.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainProvider(s, c)
	frame = tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Providers", New: func(tui.Env) tui.Screen { return s }}}})
	_, c = frame.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if c == nil {
		t.Fatal("escape not forwarded")
	}
	msg = c()
	if msg == nil {
		t.Fatal("escape result missing")
	}
	frame.Update(msg)
	if s.confirm != nil || s.mode != "" {
		t.Fatal("escape did not clear confirmation")
	}
	cfg, err = loadConfig(h)
	if err != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal("Esc changed config")
	}
}
func TestProviderFrameGlobalHelpQuitAcrossModes(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	for _, mode := range []string{"list", "countries", "results", "confirm", "adding", "add"} {
		t.Run(mode, func(t *testing.T) {
			s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
			s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
			switch mode {
			case "countries":
				s.mode = "countries"
			case "results":
				s.refreshLines = []string{"result"}
			case "confirm":
				s.mode = "confirm"
				s.confirm = &tui.Confirm{Prompt: "Remove p?"}
			case "adding":
				s.mode = "adding"
			case "add":
				s.mode = "add"
				s.form = &tui.Form{TitleText: "Add", Fields: []tui.Field{{Name: "Name", Kind: tui.TextField}}}
			}
			if s.CapturingInput() != (mode == "add") {
				t.Fatalf("capture mode %q: %v", mode, s.CapturingInput())
			}
			frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Providers", New: func(tui.Env) tui.Screen { return s }}}})
			key := func(k string) tea.Cmd {
				var code rune
				if len(k) > 0 {
					code = rune(k[0])
				}
				_, cmd := frame.Update(tea.KeyPressMsg{Text: k, Code: code})
				return cmd
			}
			cmd := key("?")
			if mode == "add" {
				if cmd != nil || strings.Contains(ansi.Strip(frame.View().Content), "Ctrl-C: quit") || s.form.Fields[0].Value != "?" {
					t.Fatal("? not handled as form input")
				}
			} else {
				if cmd != nil || !strings.Contains(ansi.Strip(frame.View().Content), "Ctrl-C: quit") {
					t.Fatal("help did not open")
				}
				key("?")
			}
			cmd = key("q")
			if mode == "add" {
				if cmd != nil || s.form.Fields[0].Value != "?q" {
					t.Fatal("q not handled as form input")
				}
			} else {
				if cmd == nil {
					t.Fatal("q command missing")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatal("q did not return QuitMsg")
				}
			}
		})
	}
}
func TestProviderConfirmNoKeysThroughFrame(t *testing.T) {
	cases := []struct {
		name     string
		key      tea.KeyPressMsg
		canceled bool
	}{{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}, false}, {"n", tea.KeyPressMsg{Code: 'n', Text: "n"}, false}, {"escape", tea.KeyPressMsg{Code: tea.KeyEscape}, true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
			s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
			s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
			_, cmd := s.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
			drainProvider(s, cmd)
			if s.confirm == nil || s.mode != "confirm" {
				t.Fatal("preflight did not start confirmation")
			}
			frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Providers", New: func(tui.Env) tui.Screen { return s }}}})
			_, cmd = frame.Update(tc.key)
			if cmd == nil {
				t.Fatal("confirmation key was not forwarded")
			}
			msg := cmd()
			result, ok := msg.(tui.ConfirmResultMsg)
			if !ok {
				t.Fatalf("result type %T", msg)
			}
			if result.Confirmed || result.Canceled != tc.canceled {
				t.Fatalf("unexpected result %+v", result)
			}
			_, next := frame.Update(result)
			if next != nil || s.confirm != nil || s.mode != "" {
				t.Fatal("confirmation state not cleared")
			}
			cfg, err := loadConfig(h)
			if err != nil || len(cfg.Subscriptions) != 1 {
				t.Fatalf("No changed config: %v", err)
			}
		})
	}
}

func TestProviderRemoveUnusedPersists(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainProvider(s, c)
	_, c = s.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	drainProvider(s, c)
	cfg, e := loadConfig(h)
	if e != nil || len(cfg.Subscriptions) != 0 {
		t.Fatalf("remove %v", e)
	}
}
func providerCountryKey(key string) tea.KeyPressMsg {
	switch key {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}
func TestProviderCountriesPagingExactCountsAndCaptureModes(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	nodes := make([]subscription.Node, 0, 13)
	for _, country := range []struct {
		code  string
		count int
	}{{"DE", 4}, {"NL", 2}, {"US", 7}} {
		for i := 0; i < country.count; i++ {
			nodes = append(nodes, subscription.Node{Country: country.code})
		}
	}
	if err := subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: nodes}); err != nil {
		t.Fatal(err)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	if s.CapturingInput() {
		t.Fatal("list captures")
	}
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if s.CapturingInput() {
		t.Fatal("countries capture")
	}
	view := ansi.Strip(s.View(80, 6))
	for _, part := range []string{"Countries: p", "13 nodes", "DE: 4", "NL: 2", "1-2 of 3", "Esc back"} {
		if !strings.Contains(view, part) {
			t.Fatalf("missing %q: %q", part, view)
		}
	}
	if strings.Contains(view, "US: 7") {
		t.Fatalf("last row initially visible %q", view)
	}
	for _, key := range []string{"down", "j", "pgdown", "end"} {
		s.Update(providerCountryKey(key))
		view = ansi.Strip(s.View(80, 6))
		if !strings.Contains(view, "US: 7") || !strings.Contains(view, "2-3 of 3") || !strings.Contains(view, "Countries: p") {
			t.Fatalf("%s navigation %q", key, view)
		}
	}
	for _, key := range []string{"up", "k", "pgup", "home"} {
		s.Update(providerCountryKey("end"))
		s.Update(providerCountryKey(key))
		view = ansi.Strip(s.View(80, 6))
		if !strings.Contains(view, "DE: 4") || !strings.Contains(view, "Countries: p") {
			t.Fatalf("%s navigation %q", key, view)
		}
	}
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	view = ansi.Strip(s.View(80, 8))
	if !strings.Contains(view, "US: 7") || strings.Contains(view, "of 3") {
		t.Fatalf("resize %q", view)
	}
	_, c = s.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	drainProvider(s, c)
	if s.countryOffset != 0 || !strings.Contains(ansi.Strip(s.View(80, 6)), "DE: 4") || !strings.Contains(ansi.Strip(s.View(80, 6)), "1-2 of 3") {
		t.Fatal("reload did not reset country page")
	}
	for _, height := range []int{0, 1, 2, 3} {
		got := s.View(80, height)
		if height == 0 && got != "" {
			t.Fatalf("zero height %q", got)
		}
		if height > 0 && strings.Count(got, "\n")+1 > height {
			t.Fatalf("height %d overflow %q", height, got)
		}
	}
	s.mode = "adding"
	if s.CapturingInput() {
		t.Fatal("adding captures")
	}
	s.mode = "results"
	if s.CapturingInput() {
		t.Fatal("results captures")
	}
	s.mode = "confirm"
	if s.CapturingInput() {
		t.Fatal("confirm captures")
	}
	s.mode = "add"
	s.form = &tui.Form{TitleText: "Add", Fields: []tui.Field{{Name: "Name", Kind: tui.TextField}}}
	if !s.CapturingInput() {
		t.Fatal("active add form does not capture")
	}
	s.form = nil
	if s.CapturingInput() {
		t.Fatal("add without form captures")
	}
	s.mode = ""
}
func TestProviderErrorRedactionURLParts(t *testing.T) {
	got := redactProviderSecrets("u token host.example key=token", "https://u:token@host.example/path?key=token")
	for _, x := range []string{"token", "host.example", "u"} {
		if strings.Contains(got, x) {
			t.Fatalf("leak %s: %s", x, got)
		}
	}
}
func TestProviderPersistedList(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://user:credential@example.invalid/path?token=abc"})
	parsed, e := subscription.ParseDetailed([]byte(testBody))
	if e != nil {
		t.Fatal(e)
	}
	if e = subscription.NewStore(h+"/subscriptions").Save("p", subscription.Snapshot{Nodes: parsed.Nodes, FetchedAt: testNow}); e != nil {
		t.Fatal(e)
	}
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.Init()
	view := ansi.Strip(s.View(140, 10))
	for _, want := range []string{"p", "stored", "DE", testNow.Format(time.RFC3339)} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "https://") || strings.Contains(view, "credential") {
		t.Fatalf("URL leaked %s", view)
	}
	list, e := ProviderList(h)
	if e != nil || len(list.Providers) != 1 || list.Providers[0].Nodes == nil || *list.Providers[0].Nodes != len(parsed.Nodes) {
		t.Fatalf("nodes %+v %v", list, e)
	}
}
func TestProviderDefaultsCaptured(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	oldFetch, oldNow := providersFetch, providersNow
	called := false
	providersFetch = func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		called = true
		return &fetch.Result{Body: []byte(testBody)}, nil
	}
	providersNow = func() time.Time { return testNow }
	t.Cleanup(func() { providersFetch, providersNow = oldFetch, oldNow })
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	providersFetch = fetch.Get
	drainProvider(s, s.refresh("p", false))
	if !called {
		t.Fatal("fetcher not captured")
	}
	snap, e := subscription.NewStore(h + "/subscriptions").Load("p")
	if e != nil || !snap.FetchedAt.Equal(testNow) {
		t.Fatalf("timestamp %v %v", snap.FetchedAt, e)
	}
}
func TestProviderRefreshResultsPaginate(t *testing.T) {
	h := t.TempDir()
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	for i := 0; i < 8; i++ {
		s.refreshLines = append(s.refreshLines, fmt.Sprintf("outcome-%d", i+1))
	}
	v := s.View(60, 4)
	if !strings.Contains(v, "outcome-1") || strings.Contains(v, "outcome-8") {
		t.Fatalf("first page %q", v)
	}
	for i := 0; i < 8; i++ {
		s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	v = s.View(60, 4)
	if !strings.Contains(v, "outcome-8") {
		t.Fatalf("last outcome unreachable %q", v)
	}
	s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(s.refreshLines) != 0 || !strings.Contains(s.View(60, 4), "NAME") {
		t.Fatalf("table not restored %q", s.View(60, 4))
	}
}
func TestProviderRefreshViewModeSwitchAndHeight(t *testing.T) {
	h := t.TempDir()
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.refreshLines = []string{"result"}
	s.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !strings.Contains(ansi.Strip(s.View(60, 10)), "Add provider") {
		t.Fatal("add hidden by results")
	}
	s.mode = ""
	s.form = nil
	s.refreshLines = []string{"result"}
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if !strings.Contains(ansi.Strip(s.View(60, 10)), "Countries: p") {
		t.Fatal("detail hidden by results")
	}
	if n := strings.Count(s.View(60, 1), "\n") + 1; n > 1 {
		t.Fatalf("height overflow: %q", s.View(60, 1))
	}
}
func TestProviderCountriesMissingSnapshot(t *testing.T) {
	h := providerSeed(t, config.Subscription{Name: "p", URL: "https://p.invalid"})
	s := newProvidersScreen(h, providerEnv(h)).(*providersScreen)
	s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "p"}}})
	_, c := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainProvider(s, c)
	if !strings.Contains(ansi.Strip(s.View(80, 10)), "No stored snapshot") {
		t.Fatal("missing snapshot")
	}
}
