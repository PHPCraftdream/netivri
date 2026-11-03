package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

type contractHeaderSource struct{ value tui.Header }

func (s *contractHeaderSource) Header() (tui.Header, error) { return s.value, nil }

func contractFrame(t *testing.T, selected int) (*tui.Frame, tui.Env, *contractHeaderSource, func() tui.Screen) {
	t.Helper()
	home := t.TempDir()
	var env tui.Env
	var screen tui.Screen
	header := &contractHeaderSource{value: tui.Header{Running: true, Listen: "127.0.0.1:1", Uptime: 9 * time.Second, Version: "test"}}
	frame := tui.NewFrame(tui.Options{
		Start:  selected,
		Home:   home,
		Header: header,
		Tabs: []tui.Tab{
			{Title: "Providers", New: func(e tui.Env) tui.Screen {
				env = e
				screen = newProvidersScreenWith(e.Home, e, nil, func() time.Time { return time.Unix(1, 0) })
				return screen
			}},
			{Title: "Accounts", New: func(e tui.Env) tui.Screen { env = e; screen = newAccountsScreen(e.Home, e); return screen }},
			{Title: "Servers", New: func(e tui.Env) tui.Screen { env = e; screen = newServersScreen(e.Home, e); return screen }},
		},
	})
	return frame, env, header, func() tui.Screen { return screen }
}

func contractRows(screen tui.Screen) {
	switch s := screen.(type) {
	case *providersScreen:
		s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "fixture"}}})
	case *accountsScreen:
		s.rows = []accountInfo{{Login: "fixture"}}
		s.syncRows("fixture")
	case *serversScreen:
		s.rows = []serverView{{Name: "fixture", Protocol: "test", Port: 1234}}
		s.syncWorkspace()
	}
}

func TestTUIFrameASCIIAndNoColorPreservePositions(t *testing.T) {
	originalNoColor, hadNoColor := os.LookupEnv("NO_COLOR")
	t.Cleanup(func() {
		if hadNoColor {
			_ = os.Setenv("NO_COLOR", originalNoColor)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})
	for _, tc := range []struct {
		name    string
		ascii   string
		noColor bool
	}{{"unicode-color", "0", false}, {"ascii-color", "1", false}, {"unicode-no-color", "0", true}, {"ascii-no-color", "1", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NETIVRI_ASCII", tc.ascii)
			if tc.noColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				if err := os.Unsetenv("NO_COLOR"); err != nil {
					t.Fatal(err)
				}
				if _, present := os.LookupEnv("NO_COLOR"); present {
					t.Fatal("NO_COLOR must be absent for color rendering")
				}
			}
			for _, screenCase := range []struct {
				name  string
				index int
				label string
			}{{"providers", 0, "fixture"}, {"accounts", 1, "fixture"}, {"servers", 2, "fixture"}} {
				t.Run(screenCase.name, func(t *testing.T) {
					frame, _, _, screen := contractFrame(t, screenCase.index)
					frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
					contractRows(screen())
					frame.Update(tui.ThemeMsg{Theme: tui.NewThemeFor(true)})
					lines := strings.Split(ansi.Strip(frame.View().Content), "\n")
					row, col, found := -1, -1, false
					for i, line := range lines {
						if at := strings.Index(line, screenCase.label); at >= 0 {
							row, col, found = i, ansi.StringWidth(line[:at]), true
							break
						}
					}
					if !found {
						t.Fatalf("fixture label %q missing", screenCase.label)
					}
					if row != 5 || col != 3 {
						t.Errorf("fixture label at row %d column %d, want row 5 column 3", row, col)
					}
				})
			}
		})
	}
}

func TestTUIFrameVolatileRegionsPreserveBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
	}{{"providers", 0}, {"accounts", 1}, {"servers", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			frame, env, header, screen := contractFrame(t, tc.index)
			initial := frame.Init()
			if initial == nil {
				t.Fatal("frame init omitted commands")
			}
			initialMessage := initial()
			if batch, ok := initialMessage.(tea.BatchMsg); ok {
				if len(batch) == 0 || batch[len(batch)-1] == nil {
					t.Fatal("header poll missing from initial batch")
				}
				frame.Update(batch[len(batch)-1]())
			} else {
				frame.Update(initialMessage)
			}
			frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			contractRows(screen())
			baseline := strings.Split(ansi.Strip(frame.View().Content), "\n")
			if len(baseline) != 40 {
				t.Fatalf("baseline lines = %d, want 40", len(baseline))
			}
			frame.Update(tui.ToastMsg("safe long-lived notice"))
			afterToast := strings.Split(ansi.Strip(frame.View().Content), "\n")
			assertContractStableBody(t, baseline, afterToast, 40)
			if !strings.Contains(afterToast[38], "safe long-lived notice") {
				t.Fatalf("toast absent from reserved row: %q", afterToast[38])
			}
			// Register and display activity without executing its worker or spinner tick.
			start := env.Background("test", func() tea.Msg { return nil })()
			frame.Update(start)
			busy := strings.Split(ansi.Strip(frame.View().Content), "\n")
			assertContractStableBody(t, baseline, busy, 40)

			// Repeated Init now contains only the header poll; no screen initialization runs.
			header.value.Uptime = 10 * time.Second
			poll := frame.Init()
			if poll == nil {
				t.Fatal("frame init omitted header poll")
			}
			message := poll()
			if batch, ok := message.(tea.BatchMsg); ok {
				if len(batch) == 0 || batch[len(batch)-1] == nil {
					t.Fatal("header poll missing from init batch")
				}
				frame.Update(batch[len(batch)-1]())
			} else {
				frame.Update(message)
			}
			updated := strings.Split(ansi.Strip(frame.View().Content), "\n")
			assertContractStableBody(t, baseline, updated, 40)
			if updated[0] == baseline[0] {
				t.Fatal("header uptime change did not update header row")
			}
		})
	}
}

func testContractWorkspaceFocus(t *testing.T, frame *tui.Frame, workspace *tui.Workspace) {
	t.Helper()
	workspace.SetFocus(tui.PaneList)
	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if workspace.Focus() != tui.PaneCard {
		t.Fatal("Tab from list did not focus card")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if workspace.Focus() != tui.PaneCard {
		t.Fatal("Tab from card changed workspace focus")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if workspace.Focus() != tui.PaneList {
		t.Fatal("Tab from tabs did not return to list")
	}

	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if workspace.Focus() != tui.PaneList {
		t.Fatal("Shift-Tab from list should enter tabs without changing pane")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if workspace.Focus() != tui.PaneCard {
		t.Fatal("Shift-Tab from tabs did not focus card")
	}
	frame.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if workspace.Focus() != tui.PaneList {
		t.Fatal("Shift-Tab from card did not return list")
	}
}

func assertContractStableBody(t *testing.T, before, after []string, height int) {
	t.Helper()
	if len(after) != height {
		t.Fatalf("rendered lines = %d, want %d", len(after), height)
	}
	for row := 2; row < height-2; row++ {
		if before[row] != after[row] {
			t.Errorf("volatile frame region changed body row %d\nbefore %q\nafter  %q", row, before[row], after[row])
		}
	}
}

func TestTUIFrameScreenFocusAndSizeContracts(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(tui.Env) tui.Screen
	}{{"providers", func(e tui.Env) tui.Screen { return newProvidersScreenWith(e.Home, e, nil, nil) }}, {"accounts", func(e tui.Env) tui.Screen { return newAccountsScreen(e.Home, e) }}, {"servers", func(e tui.Env) tui.Screen { return newServersScreen(e.Home, e) }}} {
		t.Run(tc.name, func(t *testing.T) {
			screen := tc.new(tui.Env{Home: t.TempDir(), Now: func() time.Time { return time.Unix(1, 0) }})
			frame := tui.NewFrame(tui.Options{Tabs: []tui.Tab{{Title: tc.name, New: func(tui.Env) tui.Screen { return screen }}}})
			frame.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			switch s := screen.(type) {
			case *providersScreen:
				testContractWorkspaceFocus(t, frame, s.workspace)
			case *accountsScreen:
				testContractWorkspaceFocus(t, frame, s.workspace)
			case *serversScreen:
				testContractWorkspaceFocus(t, frame, s.workspace)
			}
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
				frame.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := ansi.Strip(frame.View().Content)
				lines := strings.Split(view, "\n")
				if len(lines) != size[1] {
					t.Errorf("%dx%d has %d rows", size[0], size[1], len(lines))
				}
				for i, line := range lines {
					if got := ansi.StringWidth(line); got > size[0] {
						t.Errorf("%dx%d row %d width %d", size[0], size[1], i, got)
					}
				}
			}
		})
	}
}
