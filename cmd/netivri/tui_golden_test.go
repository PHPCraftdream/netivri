package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

// expectedPanel is a standalone geometry oracle for the list pane. Row indexes
// mirror the list contract: title, header, fixture rows or empty state, then
// the position status on the final interior row.
func expectedPanel(width, height int, title, header, empty string, populated bool) string {
	lines := make([]string, height)
	lines[0] = "+" + strings.Repeat("-", width-2) + "+"
	interior := func(s string) string {
		if len(s) > width-2 {
			s = s[:width-2]
		}
		return "|" + s + strings.Repeat(" ", width-2-len(s)) + "|"
	}
	lines[1] = interior(title)
	lines[2] = interior(header)
	if populated {
		lines[3] = interior("> alpha")
		lines[4] = interior("  beta")
		lines[height-2] = interior("1/2")
	} else {
		lines[3] = interior(empty)
	}
	for i := 1; i < height-1; i++ {
		if lines[i] == "" {
			lines[i] = interior("")
		}
	}
	lines[height-1] = "+" + strings.Repeat("-", width-2) + "+"
	return strings.Join(lines, "\n")
}

func TestScreenListLayoutGoldens(t *testing.T) {
	t.Setenv("NETIVRI_ASCII", "1")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("LC_ALL", "C.UTF-8")

	cases := []struct {
		name  string
		new   func(tui.Env) tui.Screen
		list  func(tui.Screen) *tui.List
		empty string
		seed  func(tui.Screen, bool)
	}{
		{"providers", func(e tui.Env) tui.Screen { return newProvidersScreenWith("", e, nil, nil) }, func(s tui.Screen) *tui.List { return s.(*providersScreen).workspace.List }, "A provider is a subscription of proxy nodes. Add one to start.", func(screen tui.Screen, populated bool) {
			s := screen.(*providersScreen)
			if populated {
				s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "alpha"}, {Name: "beta"}}})
			} else {
				s.setProviders(ProviderListResult{})
			}
			s.list.Columns = []tui.ListColumn{{Title: "Name", Width: 8}}
			s.list.AddLabel = ""
			s.list.Title = screen.Title()
			s.card = s.workspace.Card
		}},
		{"accounts", func(e tui.Env) tui.Screen { return newAccountsScreen("", e) }, func(s tui.Screen) *tui.List { return s.(*accountsScreen).workspace.List }, "An account needs a provider. Add one on the Providers tab, then add the account here.", func(screen tui.Screen, populated bool) {
			s := screen.(*accountsScreen)
			if populated {
				s.rows = []accountInfo{{Login: "alpha"}, {Login: "beta"}}
			} else {
				s.rows = nil
			}
			s.syncRows("")
			s.workspace.List.Columns = []tui.ListColumn{{Title: "Name", Width: 8}}
			s.workspace.List.AddLabel = ""
		}},
		{"servers", func(e tui.Env) tui.Screen { return newServersScreen("", e) }, func(s tui.Screen) *tui.List { return s.(*serversScreen).workspace.List }, "No servers configured", func(screen tui.Screen, populated bool) {
			s := screen.(*serversScreen)
			if populated {
				s.rows = []serverView{{Name: "alpha", Protocol: "test", Port: 1234}, {Name: "beta", Protocol: "test", Port: 1234}}
			} else {
				s.rows = nil
			}
			s.syncWorkspace()
			s.workspace.List.Columns = []tui.ListColumn{{Title: "Name", Width: 8}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, populated := range []bool{false, true} {
				t.Run(map[bool]string{false: "empty", true: "two-rows"}[populated], func(t *testing.T) {
					screen := tc.new(tui.Env{Theme: tui.NewTheme()})
					tc.seed(screen, populated)
					tc.list(screen).SetTheme(tui.NewTheme())
					tc.list(screen).Selected = 0
					tc.list(screen).Focused = true
					for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}} {
						sizew, sizeh := size[0], size[1]
						pane := sizew
						if sizew >= 100 {
							pane = sizew * 35 / 100
						}
						rendered := strings.Split(ansi.Strip(screen.View(sizew, sizeh)), "\n")
						for i := range rendered {
							rendered[i] = ansi.Cut(rendered[i], 0, pane)
						}
						got := strings.Join(rendered, "\n")
						want := expectedPanel(pane, sizeh, screen.Title(), "  Name", tc.empty, populated)
						if got != want {
							t.Errorf("%dx%d list layout mismatch\n got: %q\nwant: %q", sizew, sizeh, got, want)
						}
					}
				})
			}
		})
	}
}

func TestScreenFrameTooSmallGoldens(t *testing.T) {
	cases := []struct {
		name string
		new  func(tui.Env) tui.Screen
	}{
		{"providers", func(e tui.Env) tui.Screen { return newProvidersScreenWith("", e, nil, nil) }},
		{"accounts", func(e tui.Env) tui.Screen { return newAccountsScreen("", e) }},
		{"servers", func(e tui.Env) tui.Screen { return newServersScreen("", e) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: tc.name, New: tc.new}}})
			_, _ = f.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
			got := ansi.Strip(f.View().Content)
			msg := "Window too small - enlarge the window"
			blank := strings.Repeat(" ", (60-len(msg))/2)
			want := strings.Repeat(blank+"\n", 8) + blank + msg + "\n" + strings.Repeat(blank+"\n", 6) + blank
			if got != want {
				t.Errorf("60x16 frame mismatch\n got: %q\nwant: %q", got, want)
			}
		})
	}
}
