package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/PHPCraftdream/netivri/internal/tui"
)

const goldenProviders80Empty = `Service stopped
[Providers]
+------------------------------------------------------------------------------+
|Providers                                                                     |
|  Name                                                 Nodes     State        |
|A provider is a subscription of proxy nodes. Add one to start.                |
|> + Add provider                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/1                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenProviders80Populated = `Service stopped
[Providers]
+------------------------------------------------------------------------------+
|Providers                                                                     |
|  Name                                                 Nodes     State        |
|> alpha                                                2         ready        |
|  beta                                                 3         ready        |
|  + Add provider                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/3                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenProviders100Empty = `Service stopped
[Providers]
+---------------------------------+ +--------------------------------------------------------------+
|Providers                        | |Provider details                                              |
|  Name            Node  State    | |Select a provider to view details                             |
|A provider is a subscription of p| |  Name                                                        |
|> + Add provider                 | |  Subscription  [edit]                                        |
|                                 | |  Via  [edit]                                                 |
|                                 | |  Nodes                                                       |
|                                 | |  Countries                                                   |
|                                 | |  Updated                                                     |
|                                 | |  Pre-hop  [edit]                                             |
|                                 | |  Post-hop  [edit]                                            |
|                                 | |  Rename                                                      |
|                                 | |  Edit subscription                                           |
|                                 | |  Edit Via                                                    |
|                                 | |  Clear pre-hop  [P]                                          |
|                                 | |  Clear post-hop  [O]                                         |
|                                 | |  Refresh selected  [r]                                       |
|                                 | |  Refresh all  [R]                                            |
|                                 | |  Countries  [c]                                              |
|                                 | |  Nodes...  [n]                                               |
|                                 | |  Diagnose...  [g]                                            |
|                                 | |  Remove  [d]                                                 |
|                                 | |                                                              |
|                                 | |                                                              |
|1/1                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenProviders100Populated = `Service stopped
[Providers]
+---------------------------------+ +--------------------------------------------------------------+
|Providers                        | |alpha                                                         |
|  Name            Node  State    | |State: ready                                                  |
|> alpha           2     ready    | |  Name  alpha                                                 |
|  beta            3     ready    | |  Subscription  Not set  [edit]                               |
|  + Add provider                 | |  Via  Not set  [edit]                                        |
|                                 | |  Nodes  2                                                    |
|                                 | |  Countries                                                   |
|                                 | |  Updated  Never                                              |
|                                 | |  Pre-hop  inherited  [edit]                                  |
|                                 | |  Post-hop  inherited  [edit]                                 |
|                                 | |  Rename                                                      |
|                                 | |  Edit subscription                                           |
|                                 | |  Edit Via                                                    |
|                                 | |  Clear pre-hop  [P]                                          |
|                                 | |  Clear post-hop  [O]                                         |
|                                 | |  Refresh selected  [r]                                       |
|                                 | |  Refresh all  [R]                                            |
|                                 | |  Countries  [c]                                              |
|                                 | |  Nodes...  [n]                                               |
|                                 | |  Diagnose...  [g]                                            |
|                                 | |  Remove  [d]                                                 |
|                                 | |                                                              |
|                                 | |                                                              |
|1/3                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenAccounts80Empty = `Service stopped
[Accounts]
+------------------------------------------------------------------------------+
|Accounts                                                                      |
|  LOGIN           PROVIDER        EXIT    CONN    UP/s    DOWN                |
|An account needs a provider. Add one on the Providers tab, then add the accoun|
|> + Add account [a]                                                           |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/1                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenAccounts80Populated = `Service stopped
[Accounts]
+------------------------------------------------------------------------------+
|Accounts                                                                      |
|  LOGIN          PROVIDER          EXIT    CONN    UP/s    DOWN               |
|> alpha          provider-a        us      -       -       -                  |
|  beta           provider-b        de      -       -       -                  |
|  + Add account [a]                                                           |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/3                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenAccounts100Empty = `Service stopped
[Accounts]
+---------------------------------+ +--------------------------------------------------------------+
|Accounts                         | |Account                                                       |
|  LOGI  PROVID  EX  CO  UP/  DOW | |Select an account to see details.                             |
|An account needs a provider. Add | |  Apply to marked...  [b]                                     |
|> + Add account [a]              | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|1/1                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenAccounts100Populated = `Service stopped
[Accounts]
+---------------------------------+ +--------------------------------------------------------------+
|Accounts                         | |Account alpha                                                 |
|  LOGI  PROVID  EX  CO  UP/  DOW | |Live · Conn - · Up/s - · Down/s -                             |
|> alph  provid  us  -   -    -   | |  Provider  provider-a  [edit]                                |
|  beta  provid  de  -   -    -   | |  Countries  any  [edit]                                      |
|  + Add account [a]              | |  Exit  us  [edit]                                            |
|                                 | |  Allowed clients  any  [edit]                                |
|                                 | |  Pre-hop  none  [edit]                                       |
|                                 | |  Clear pre-hop  [P]                                          |
|                                 | |  Post-hop  none  [edit]                                      |
|                                 | |  Clear post-hop  [O]                                         |
|                                 | |  Password  waiting for the first login                       |
|                                 | |  Clone account  [c]                                          |
|                                 | |  Remove account  [d]                                         |
|                                 | |  Route...  [v]                                               |
|                                 | |  Apply to marked...  [b]                                     |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|1/3                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenServers80Empty = `Service stopped
[Servers]
+------------------------------------------------------------------------------+
|Servers                                                                       |
|  Name                Protocol        Port   State                            |
|No servers configured                                                         |
|> + Add a server [a]                                                          |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/1                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenServers80Populated = `Service stopped
[Servers]
+------------------------------------------------------------------------------+
|Servers                                                                       |
|  Name                Protocol        Port   State                            |
|> alpha               socks5          1080   angel stopped                    |
|  beta                http            8080   angel stopped                    |
|  + Add a server [a]                                                          |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|                                                                              |
|1/3                                                                           |
+------------------------------------------------------------------------------+
                                                                                
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .`

const goldenServers100Empty = `Service stopped
[Servers]
+---------------------------------+ +--------------------------------------------------------------+
|Servers                          | |Server                                                        |
|  Name   Protocol  Port   State  | |No servers configured. Add a server to start.                 |
|No servers configured            | |  Add a server...  [a]                                        |
|> + Add a server [a]             | |  Reload  [r]                                                 |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|1/1                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenServers100Populated = `Service stopped
[Servers]
+---------------------------------+ +--------------------------------------------------------------+
|Servers                          | |Server                                                        |
|  Name   Protocol  Port   State  | |Select a server to see details.                               |
|> alpha  socks5    1080   angel s| |  Add a server...  [a]                                        |
|  beta   http      8080   angel s| |  Reload  [r]                                                 |
|  + Add a server [a]             | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|                                 | |                                                              |
|1/3                              | |                                                              |
+---------------------------------+ +--------------------------------------------------------------+
                                                                                                    
q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .`

const goldenTooSmall = `           
           
           
           
           
           
           
           
           Window too small - enlarge the window
           
           
           
           
           
           
           `

// fullScreenSnapshotCase describes one screen rendered in TestFullScreenSnapshots.
type fullScreenSnapshotCase struct {
	name, title string
	new         func(tui.Env) tui.Screen
	seed        func(*testing.T, tui.Screen, bool)
}

func TestFullScreenSnapshots(t *testing.T) {
	t.Setenv("NETIVRI_ASCII", "1")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("LC_ALL", "C.UTF-8")
	cases := []fullScreenSnapshotCase{
		{"providers", "Providers", func(e tui.Env) tui.Screen { return newProvidersScreenWith("", e, nil, nil) }, seedProvidersSnapshot},
		{"accounts", "Accounts", func(e tui.Env) tui.Screen { return newAccountsScreen("", e) }, seedAccountsSnapshot},
		{"servers", "Servers", func(e tui.Env) tui.Screen { return newServersScreen("", e) }, seedServersSnapshot},
	}
	sizes := [][2]int{{60, 16}, {80, 24}, {100, 30}}

	for _, tc := range cases {
		for _, pop := range []bool{false, true} {
			for _, size := range sizes {
				t.Run(tc.name+map[bool]string{false: "/empty", true: "/populated"}[pop]+"/"+string(rune('0'+size[0]/10)), func(t *testing.T) {
					runFullScreenSnapshotCase(t, tc, pop, size)
				})
			}
		}
	}
}

func seedProvidersSnapshot(t *testing.T, x tui.Screen, p bool) {
	t.Helper()
	two, three := 2, 3
	s := mustAs[*providersScreen](t, x)
	if p {
		s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "alpha", Nodes: &two, State: "ready"}, {Name: "beta", Nodes: &three, State: "ready"}}})
		s.selected = "alpha"
	} else {
		s.setProviders(ProviderListResult{})
	}
	s.updateCard()
}

func seedAccountsSnapshot(t *testing.T, x tui.Screen, p bool) {
	t.Helper()
	s := mustAs[*accountsScreen](t, x)
	if p {
		s.rows = []accountInfo{{Login: "alpha", Provider: "provider-a", Exit: "us"}, {Login: "beta", Provider: "provider-b", Exit: "de"}}
		s.syncRows("alpha")
	} else {
		s.rows = nil
		s.syncRows("")
	}
	s.refreshSelectedCard()
}

func seedServersSnapshot(t *testing.T, x tui.Screen, p bool) {
	t.Helper()
	s := mustAs[*serversScreen](t, x)
	if p {
		s.rows = []serverView{{Name: "alpha", Protocol: "socks5", Port: 1080}, {Name: "beta", Protocol: "http", Port: 8080}}
	} else {
		s.rows = nil
	}
	s.syncWorkspace()
}

// goldenFrameKey identifies a hand-derived frame by screen title, populated flag and width.
type goldenFrameKey struct {
	title     string
	populated bool
	width     int
}

// goldenFrameLiterals: 60 columns always uses goldenTooSmall, see goldenFrameLiteral.
var goldenFrameLiterals = map[goldenFrameKey]string{
	{"Providers", false, 80}:  goldenProviders80Empty,
	{"Providers", false, 100}: goldenProviders100Empty,
	{"Providers", true, 80}:   goldenProviders80Populated,
	{"Providers", true, 100}:  goldenProviders100Populated,
	{"Accounts", false, 80}:   goldenAccounts80Empty,
	{"Accounts", false, 100}:  goldenAccounts100Empty,
	{"Accounts", true, 80}:    goldenAccounts80Populated,
	{"Accounts", true, 100}:   goldenAccounts100Populated,
	{"Servers", false, 80}:    goldenServers80Empty,
	{"Servers", false, 100}:   goldenServers100Empty,
	{"Servers", true, 80}:     goldenServers80Populated,
	{"Servers", true, 100}:    goldenServers100Populated,
}

// goldenFrameLiteral returns the expected frame; every line, including trailing spaces, was traced
// from the frame/list/card renderers (docs/design/tui-ux.md). No production data
// is consulted at runtime.
func goldenFrameLiteral(name string, populated bool, w int) string {
	if w == 60 {
		return goldenTooSmall
	}
	return goldenFrameLiterals[goldenFrameKey{name, populated, w}]
}

func runFullScreenSnapshotCase(t *testing.T, tc fullScreenSnapshotCase, pop bool, size [2]int) {
	t.Helper()
	env := tui.Env{Theme: tui.NewTheme()}
	screen := tc.new(env)
	frame := tui.NewFrame(tui.Options{Env: env, Tabs: []tui.Tab{{Title: tc.title, New: func(tui.Env) tui.Screen { return screen }}}})
	_, _ = frame.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
	tc.seed(t, screen, pop)
	switch s := screen.(type) {
	case *providersScreen:
		s.workspace.SetTheme(tui.NewTheme())
	case *serversScreen:
		s.workspace.SetTheme(tui.NewTheme())
	case *accountsScreen:
		s.workspace.SetTheme(tui.NewTheme())
	}
	got := ansi.Strip(frame.View().Content)
	want := goldenFrameLiteral(tc.title, pop, size[0])
	if lines := strings.Split(want, "\n"); len(lines) != size[1] {
		t.Fatalf("literal lines %d, want %d", len(lines), size[1])
	} else {
		for i, line := range lines {
			if lw := runewidth.StringWidth(line); lw > size[0] {
				t.Fatalf("literal line %d width %d exceeds %d", i, lw, size[0])
			}
		}
	}
	if got != want {
		t.Errorf("%dx%d mismatch | got: %q | want: %q", size[0], size[1], got, want)
	}
}

func TestFullFrameLayoutContracts(t *testing.T) {
	t.Setenv("NETIVRI_ASCII", "1")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("LC_ALL", "C.UTF-8")
	for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		for _, name := range []string{"providers", "accounts", "servers"} {
			t.Run(name+"/layout/"+string(rune('0'+size[0]/10)), func(t *testing.T) {
				env := tui.Env{Theme: tui.NewTheme()}
				var screen tui.Screen
				var title string
				switch name {
				case "providers":
					title = "Providers"
					screen = newProvidersScreenWith("", env, nil, nil)
				case "accounts":
					title = "Accounts"
					screen = newAccountsScreen("", env)
				default:
					title = "Servers"
					screen = newServersScreen("", env)
				}
				frame := tui.NewFrame(tui.Options{Env: env, Tabs: []tui.Tab{{Title: title, New: func(tui.Env) tui.Screen { return screen }}}})
				_, _ = frame.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				lines := strings.Split(ansi.Strip(frame.View().Content), "\n")
				if len(lines) != size[1] {
					t.Errorf("lines %d, want %d", len(lines), size[1])
				}
				for i, line := range lines {
					if w := runewidth.StringWidth(line); w > size[0] {
						t.Errorf("line %d width %d exceeds %d", i, w, size[0])
					}
				}
			})
		}
	}
}
