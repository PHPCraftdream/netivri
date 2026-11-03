package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

func serversFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	c := config.Default()
	c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$fake-hash", PreSOCKS5: "socks5://user:fake-chain-password@127.0.0.1:1080"}}
	c.Servers = []config.Server{{Name: "ss", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 1234, Accounts: "alice", PublicHost: "pub.test", Params: map[string]string{"cipher": "aes-128-gcm"}}, {Name: "vless", Protocol: "vless-reality", Bind: "127.0.0.1", Port: 2345, Accounts: "alice", Params: map[string]string{"sni": "secret-reality-sni.example"}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if err := config.Save(config.ConfigPath(home), c); err != nil {
		t.Fatal(err)
	}
	// Materialize server secrets so link/rotate/remove paths are real:
	// ServerLink and ServerRotate fail ("server secrets not found",
	// internal/servers/store.go Apply) against an empty store.
	specs, err := c.ResolveServers()
	if err != nil {
		t.Fatalf("resolve fixture servers: %v", err)
	}
	store := servers.NewStore(home)
	for _, spec := range specs {
		if _, err := store.EnsureSecrets(spec); err != nil {
			t.Fatalf("ensure secrets for %s: %v", spec.Name, err)
		}
	}
	return home
}
func newServersTestScreen(t *testing.T, home string) *serversScreen {
	t.Helper()
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	var st angel.Status
	statusErr := angel.ErrNotRunning
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) { return angel.State{}, st, statusErr }
	s := newServersScreen(home, tui.Env{Home: home, Background: func(label string, work func() tea.Msg) tea.Cmd {
		return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
	}}).(*serversScreen)
	s.status = func(ctx context.Context, h string) (angel.State, angel.Status, error) { return angelProbeLive(ctx, h) }
	return s
}
func serversRunOne(t *testing.T, s *serversScreen, c tea.Cmd) (tea.Msg, tea.Cmd) {
	t.Helper()
	if c == nil {
		return nil, nil
	}
	msg := c()
	_, next := s.Update(msg)
	return msg, next
}
func serversRun(t *testing.T, s *serversScreen, c tea.Cmd) tea.Cmd {
	t.Helper()
	for n := 0; c != nil && n < 20; n++ {
		_, c = serversRunOne(t, s, c)
	}
	if c != nil {
		t.Fatal("server commands did not settle")
	}
	return nil
}
func serversKey(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}
func serversLoadDetail(t *testing.T, s *serversScreen) {
	t.Helper()
	if s.workspace != nil {
		s.workspace.SetSize(180, 30)
		s.workspace.SetFocus(tui.PaneList)
	}
	_, cmd := s.Update(serversKey(tea.KeyEnter, ""))
	if cmd == nil {
		t.Fatal("enter did not schedule detail")
	}
	serversRun(t, s, cmd)
}

func TestServersTUIFramePaneFocus(t *testing.T) {
	screen := newServersScreen(t.TempDir(), tui.Env{}).(*serversScreen)
	frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: "Servers", New: func(tui.Env) tui.Screen { return screen }}}})
	frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	frame.Init()
	if screen.workspace.Focus() != tui.PaneList {
		t.Fatal("expected list pane focus initially")
	}
	frame.Update(serversKey(tea.KeyTab, ""))
	if screen.workspace.Focus() != tui.PaneCard {
		t.Fatal("Tab did not focus card pane")
	}
	// Second Tab leaves the card at its boundary and moves frame focus to the
	// tab strip; the workspace keeps the card pane focused.
	frame.Update(serversKey(tea.KeyTab, ""))
	if screen.workspace.Focus() != tui.PaneCard {
		t.Fatal("Tab to tab strip moved workspace focus out of card pane")
	}
	// Third Tab returns from the tab strip to the list pane.
	frame.Update(serversKey(tea.KeyTab, ""))
	if screen.workspace.Focus() != tui.PaneList {
		t.Fatal("Tab from tab strip did not return focus to list pane")
	}
	// Shift+Tab from the list enters the tab strip (workspace stays on list),
	// then goes back through the card, then to the list.
	frame.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if screen.workspace.Focus() != tui.PaneList {
		t.Fatal("Shift+Tab to tab strip changed workspace focus")
	}
	frame.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if screen.workspace.Focus() != tui.PaneCard {
		t.Fatal("Shift+Tab from tab strip did not focus card pane")
	}
	frame.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if screen.workspace.Focus() != tui.PaneList {
		t.Fatal("Shift+Tab from card did not return focus to list pane")
	}
}

func TestServersTUIFramePaneFocusBindings(t *testing.T) {
	s := newServersScreen(t.TempDir(), tui.Env{}).(*serversScreen)
	s.workspace.SetSize(120, 40)
	hints := s.Bindings()
	if len(hints) < 2 {
		t.Fatal("list bindings and reload hint missing")
	}
	s.workspace.SetFocus(tui.PaneCard)
	hints = s.Bindings()
	if len(hints) == 0 {
		t.Fatal("card bindings missing")
	}
}

func TestServersTUIListDetailsReloadReadOnly(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	for _, w := range []string{"Name", "Protocol", "shadowsocks", "vless-reality", "1234", "angel stopped"} {
		if !strings.Contains(s.View(180, 30), w) {
			t.Errorf("list missing %q", w)
		}
	}
	for _, secret := range []string{"fake-hash", "fake-chain-password", "aes-128-gcm", "secret-reality-sni.example"} {
		if strings.Contains(s.View(180, 30), secret) {
			t.Errorf("list leaked %q", secret)
		}
	}
	serversLoadDetail(t, s)
	for _, w := range []string{"ss", "shadowsocks", "127.0.0.1", "1234", "alice", "pub.test", "Parameters: cipher", "State: angel stopped"} {
		if !strings.Contains(s.View(180, 30), w) {
			t.Errorf("detail missing %q", w)
		}
	}
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	_, _ = s.Update(serversKey(tea.KeyDown, ""))
	serversLoadDetail(t, s)
	if !strings.Contains(s.View(180, 30), "Parameters: sni") || strings.Contains(s.View(180, 30), "secret-reality-sni.example") {
		t.Fatal("VLESS detail exposed parameter value")
	}
	for _, secret := range []string{"fake-hash", "fake-chain-password", "aes-128-gcm", "secret-reality-sni.example"} {
		if strings.Contains(s.View(180, 30), secret) {
			t.Errorf("detail leaked %q", secret)
		}
	}
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	_, _ = s.Update(serversKey(tea.KeyUp, ""))
	serversLoadDetail(t, s)
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0].PublicHost = "changed.test"
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	_, cmd := s.Update(serversKey('r', "r"))
	if cmd == nil {
		t.Fatal("detail reload missing")
	}
	serversRun(t, s, cmd)
	if !strings.Contains(s.View(180, 30), "changed.test") {
		t.Fatal("detail reload stale")
	}
	cfg.Servers[0].PublicHost = "pub.test"
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	keys := []tea.KeyPressMsg{serversKey(tea.KeyUp, ""), serversKey(tea.KeyDown, ""), serversKey(tea.KeyPgUp, ""), serversKey(tea.KeyPgDown, ""), serversKey(tea.KeyHome, ""), serversKey(tea.KeyEnd, ""), serversKey(tea.KeyEsc, ""), serversKey('r', "r"), serversKey('q', "q"), serversKey(tea.KeyTab, ""), serversKey(tea.KeySpace, " "), serversKey(tea.KeyBackspace, ""), serversKey('a', "a"), serversKey('e', "e"), serversKey('d', "d"), serversKey('c', "c"), serversKey('l', "l"), serversKey('x', "x"), serversKey(tea.KeyEnter, "")}
	for _, k := range keys {
		_, cmd = s.Update(k)
		serversRun(t, s, cmd)
		after, e := os.ReadFile(config.ConfigPath(home))
		if e != nil || string(after) != string(before) {
			t.Fatalf("key %q modified config", k.String())
		}
	}
	_, cmd = s.Update(serversKey(tea.KeyEsc, ""))
	if cmd != nil {
		t.Fatal("escape scheduled work")
	}
	_, cmd = s.Update(serversKey(tea.KeyEnter, ""))
	if cmd == nil {
		t.Fatal("enter did not schedule stale detail")
	}
	stale := cmd()
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	_, _ = s.Update(stale)
	if s.detail != nil {
		t.Fatal("stale detail reopened")
	}
}

func TestServersTUIStatusAndDetailCounters(t *testing.T) {
	s := newServersTestScreen(t, serversFixture(t))
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{Servers: []angel.ServerInfo{{Name: "ss"}}}, nil
	}
	serversRun(t, s, s.Init())
	rows := serverRows(s.rows, s.angelRunning, s.serverRunning)
	if rows[0].Cells[5] != "running" || rows[0].Cells[6] != "u:0 s:0 r:0 a:0" || rows[1].Cells[5] != "not applied" || rows[1].Cells[6] != "" {
		t.Fatalf("listed zero counters/absent state mismatch: %#v", rows)
	}
	serversLoadDetail(t, s)
	if !strings.Contains(s.View(180, 20), "State: running") || !strings.Contains(s.View(180, 20), "Counters: u:0") {
		t.Fatal("zero-counter running detail missing")
	}
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{Servers: []angel.ServerInfo{{Name: "ss", Unclassified: 4, SlowFirstByte: 3, Replayed: 2, Ambiguous: 1}}}, nil
	}
	_, cmd := s.Update(serversKey('r', "r"))
	if cmd == nil {
		t.Fatal("nonzero detail reload missing")
	}
	serversRun(t, s, cmd)
	for _, x := range []string{"State: running", "Counters: u:4 s:3 r:2 a:1"} {
		if !strings.Contains(s.View(180, 20), x) {
			t.Errorf("nonzero running detail missing %q", x)
		}
	}
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	_, _ = s.Update(serversKey(tea.KeyDown, ""))
	serversLoadDetail(t, s)
	if !strings.Contains(s.View(180, 20), "State: not applied") || strings.Contains(s.View(180, 20), "Counters:") {
		t.Fatal("absent server detail should be not applied without counters")
	}
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	_, _ = s.Update(serversKey(tea.KeyUp, ""))
	serversLoadDetail(t, s)
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	_, cmd = s.Update(serversKey('r', "r"))
	if cmd == nil {
		t.Fatal("stopped reload missing")
	}
	serversRun(t, s, cmd)
	if !strings.Contains(s.View(180, 20), "State: angel stopped") || strings.Contains(s.View(180, 20), "Counters:") {
		t.Fatal("angel-stopped detail state/counters incorrect")
	}
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, context.DeadlineExceeded
	}
	_, cmd = s.Update(serversKey('r', "r"))
	serversRun(t, s, cmd)
	if strings.Contains(s.View(180, 20), "deadline") {
		t.Fatal("raw status error exposed")
	}
}

func TestServersTUIListReloadEmptyErrorsResizeBoundsAndStale(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	_, oldCmd := s.Update(serversKey('r', "r"))
	oldMsg := oldCmd()
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0].Name = "renamed"
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	_, newCmd := s.Update(serversKey('r', "r"))
	newMsg := newCmd()
	_, _ = s.Update(newMsg)
	_, _ = s.Update(oldMsg)
	if !strings.Contains(s.View(180, 20), "renamed") || len(s.rows) != 2 || s.rows[0].Name != "renamed" {
		t.Fatal("older ss result replaced newer renamed list")
	}
	empty := newServersTestScreen(t, t.TempDir())
	serversRun(t, empty, empty.Init())
	v := empty.View(180, 12)
	for _, x := range []string{"No servers configured", "Changes: netivri server add", "link", "rotate", "remove"} {
		if !strings.Contains(v, x) {
			t.Errorf("empty/footer missing %q", x)
		}
	}
	bad := t.TempDir()
	secret := "malformed-secret-value"
	if err = os.WriteFile(config.ConfigPath(bad), []byte("malformed: "+secret+"\nservers: ["), 0600); err != nil {
		t.Fatal(err)
	}
	broken := newServersTestScreen(t, bad)
	serversRun(t, broken, broken.Init())
	if strings.Contains(broken.View(80, 8), secret) {
		t.Fatal("parser secret leaked")
	}
	for _, z := range [][2]int{{0, 0}, {8, 2}, {40, 5}, {180, 24}} {
		_, _ = s.Update(tea.WindowSizeMsg{Width: z[0], Height: z[1]})
		v = s.View(z[0], z[1])
		if z[1] > 0 && len(strings.Split(v, "\n")) > z[1] {
			t.Fatalf("height exceeded %v", z)
		}
		for _, line := range strings.Split(v, "\n") {
			if lipgloss.Width(line) > z[0] {
				t.Fatalf("width exceeded %v", z)
			}
			if !utf8.ValidString(line) {
				t.Fatal("invalid utf8")
			}
		}
	}
}

func TestServersTUIDetailNotFoundRecovery(t *testing.T) {
	home := serversFixture(t)
	s := newServersTestScreen(t, home)
	serversRun(t, s, s.Init())
	serversLoadDetail(t, s)
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers = cfg.Servers[1:]
	if err = config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	s.workspace.SetFocus(tui.PaneCard)
	_, cmd := s.Update(serversKey('r', "r"))
	if cmd == nil {
		t.Fatal("detail reload missing")
	}
	msg, next := serversRunOne(t, s, cmd)
	if _, ok := msg.(tui.BackgroundMsg); !ok {
		t.Fatalf("expected background not-found result, got %T", msg)
	}
	if next == nil || !strings.Contains(s.View(180, 20), "server no longer exists") {
		t.Fatal("not-found did not show error and schedule reload")
	}
	if strings.Contains(s.View(180, 20), "Changes: netivri server add") {
		t.Fatal("not-found returned before detail error was visible")
	}
	_, _ = s.Update(serversKey(tea.KeyEsc, ""))
	if s.errText != "" || strings.Contains(s.View(180, 20), "server no longer exists") {
		t.Fatal("escape did not clear the visible not-found error")
	}
	serversRun(t, s, next)
	if s.detail == nil || s.detail.Name != "vless" || s.errText != "" || len(s.rows) != 1 || s.rows[0].Name != "vless" || strings.Contains(s.View(180, 20), "shadowsocks") || strings.Contains(s.View(180, 20), "1234") {
		t.Fatalf("removed row remained: rows=%+v view=%s", s.rows, s.View(180, 20))
	}
}

func TestServersTUISchedulesStatusInBackground(t *testing.T) {
	home := serversFixture(t)
	oldProbe := angelProbeLive
	t.Cleanup(func() { angelProbeLive = oldProbe })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	calls, scheduled := 0, 0
	s := newServersScreen(home, tui.Env{Home: home, Background: func(label string, work func() tea.Msg) tea.Cmd {
		scheduled++
		return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: work()} }
	}}).(*serversScreen)
	s.status = func(context.Context, string) (angel.State, angel.Status, error) {
		calls++
		return angel.State{}, angel.Status{Servers: []angel.ServerInfo{{Name: "ss"}}}, nil
	}
	cmd := s.Init()
	if calls != 0 || scheduled != 1 {
		t.Fatalf("status ran inline calls=%d scheduled=%d", calls, scheduled)
	}
	msg := cmd()
	if calls != 1 {
		t.Fatalf("background calls=%d", calls)
	}
	_, _ = s.Update(msg)
	if !strings.Contains(s.View(180, 20), "running") {
		t.Fatal("injectable status source was not applied")
	}
}
