package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func literalFrame(width, height int, tab, body string, small bool) string {
	lines := make([]string, height)
	if small {
		message := "Window too small - enlarge the window"
		for i := range lines {
			lines[i] = strings.Repeat(" ", (width-len(message))/2)
		}
		lines[height/2] += message
		return strings.Join(lines, "\n")
	}
	lines[0], lines[1] = "Service stopped", "["+tab+"]"
	bodyLines := strings.Split(body, "\n")
	for i := 0; i < height-4 && i < len(bodyLines); i++ {
		lines[i+2] = bodyLines[i]
	}
	lines[height-2] = strings.Repeat(" ", width)
	hints := map[int]string{
		80:  "q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page .",
		100: "q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first .",
		120: "q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first | end last .",
	}
	if width == 120 && tab != "Servers" {
		hints[120] = "q quit | ? help | tab focus | </> tabs | ^/k move | v/j move | pgup page | pgdn page | home first | end last | a add ."
	}
	lines[height-1] = ansi.Truncate(hints[width], width, ".")
	return strings.Join(lines, "\n")
}

// rows are indexed from pane inner row 1; every expected row is independent literal data.
func literalPane(width, height int, rows []string) string {
	lines := make([]string, height)
	lines[0], lines[height-1] = "+"+strings.Repeat("-", width-2)+"+", "+"+strings.Repeat("-", width-2)+"+"
	for i := 1; i < height-1; i++ {
		text := ""
		if i-1 < len(rows) {
			text = rows[i-1]
		}
		if runewidth.StringWidth(text) > width-2 {
			panic("literal pane row exceeds inner width")
		}
		lines[i] = "|" + text + strings.Repeat(" ", width-2-runewidth.StringWidth(text)) + "|"
	}
	return strings.Join(lines, "\n")
}

func goldenCells(widths []int, values ...string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		value = ansi.Truncate(value, widths[i], "")
		parts[i] = value + strings.Repeat(" ", widths[i]-runewidth.StringWidth(value))
	}
	return strings.Join(parts, "  ")
}

func goldenListPane(pw, h int, rows []string, status string) string {
	content := make([]string, h-2)
	copy(content, rows)
	content[h-3] = status
	return literalPane(pw, h, content)
}

func goldenJoin(left, right string) string {
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	rows := make([]string, len(l))
	for i := range rows {
		rows[i] = l[i] + " " + r[i]
	}
	return strings.Join(rows, "\n")
}

func providerGoldenFrame(width, height int, populated bool) string {
	if width <= 60 || height <= 16 {
		return literalFrame(width, height, "Providers", "", true)
	}
	pw := map[int]int{80: 80, 100: 35, 120: 42}[width]
	cols := map[int][]int{80: {51, 8, 12}, 100: {14, 4, 8}, 120: {17, 6, 10}}[width]
	rows := []string{"Providers", "  " + goldenCells(cols, "Name", "Nodes", "State")}
	var status string
	if populated {
		rows = append(rows, "> "+goldenCells(cols, "alpha", "2", "ready"), "  "+goldenCells(cols, "beta", "3", "ready"), "  + Add provider")
		status = "1/3"
	} else {
		rows = append(rows, ansi.Truncate("No providers yet. Add one to get started.", pw-2, ""), "> + Add provider")
		status = "1/1"
	}
	left := goldenListPane(pw, height-4, rows, status)
	if width < 100 {
		return literalFrame(width, height, "Providers", left, false)
	}
	cw := width - pw - 1
	var cardRows []string
	if populated {
		cardRows = []string{"alpha", "State: ready", "  Name  alpha", "  Subscription  ********", "  Via  ********", "  Nodes  2", "  Countries", "  Updated  Never", "  Edit name  renaming cannot safely update account references and stored snapshots", "  Edit subscription", "  Edit Via", "  Refresh selected  [r]", "  Refresh all  [R]", "  Countries  [c]", "  Remove  [d]"}
	} else {
		cardRows = []string{"Provider details", "Select a provider to view details", "  Name", "  Subscription", "  Via", "  Nodes", "  Countries", "  Updated", "  Edit name  renaming cannot safely update account references and stored snapshots", "  Edit subscription", "  Edit Via", "  Refresh selected  [r]", "  Refresh all  [R]", "  Countries  [c]", "  Remove  [d]"}
	}
	for i := range cardRows {
		cardRows[i] = ansi.Truncate(cardRows[i], cw-2, ".")
	}
	card := literalPane(cw, height-4, cardRows)
	return literalFrame(width, height, "Providers", goldenJoin(left, card), false)
}

func serversGoldenFrame(width, height int, populated bool) string {
	if width <= 60 || height <= 16 {
		return literalFrame(width, height, "Servers", "", true)
	}
	pw := map[int]int{80: 80, 100: 35, 120: 42}[width]
	cols := map[int][]int{80: {18, 14, 5, 14}, 100: {5, 8, 5, 8}, 120: {5, 8, 5, 13}}[width]
	if !populated && width == 120 {
		cols = []int{6, 9, 5, 9}
	}
	rows := []string{"Servers", "  " + goldenCells(cols, "Name", "Protocol", "Port", "State")}
	status := ""
	if populated {
		rows = append(rows, "> "+goldenCells(cols, "alpha", "socks5", "1080", "angel stopped"), "  "+goldenCells(cols, "beta", "http", "8080", "angel stopped"))
		status = "1/2"
	} else {
		rows = append(rows, "No servers configured")
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], pw-2, "")
	}
	left := goldenListPane(pw, height-4, rows, status)
	if width < 100 {
		return literalFrame(width, height, "Servers", left, false)
	}
	cw := width - pw - 1
	cardRows := []string{"Server", "Changes: netivri server add · link · rotate · remove", "  Reload  [r]"}
	for i := range cardRows {
		cardRows[i] = ansi.Truncate(cardRows[i], cw-2, ".")
	}
	card := literalPane(cw, height-4, cardRows)
	return literalFrame(width, height, "Servers", goldenJoin(left, card), false)
}

func accountsGoldenFrame(width, height int, populated bool) string {
	if width <= 60 || height <= 16 {
		return literalFrame(width, height, "Accounts", "", true)
	}
	pw := map[int]int{80: 80, 100: 35, 120: 42}[width]
	cols := map[int][]int{80: {13, 16, 6, 6, 6, 5}, 100: {4, 6, 2, 2, 3, 3}, 120: {5, 10, 3, 3, 3, 3}}[width]
	if !populated {
		cols = map[int][]int{80: {14, 14, 6, 6, 6, 5}, 100: {4, 6, 2, 2, 3, 3}, 120: {5, 8, 4, 4, 3, 3}}[width]
	}
	rows := []string{"Accounts", "  " + goldenCells(cols, "LOGIN", "PROVIDER", "EXIT", "CONN", "UP/s", "DOWN")}
	status := ""
	if populated {
		rows = append(rows, "> "+goldenCells(cols, "alpha", "provider-a", "us", "-", "-", "-"), "  "+goldenCells(cols, "beta", "provider-b", "de", "-", "-", "-"), "  + Add account [a]")
		status = "1/3"
	} else {
		rows = append(rows, ansi.Truncate("An account needs a Provider first. Switch to Providers to add one.", pw-2, ""), "> + Add account [a]")
		status = "1/1"
	}
	left := goldenListPane(pw, height-4, rows, status)
	if width < 100 {
		return literalFrame(width, height, "Accounts", left, false)
	}
	cw := width - pw - 1
	var cardRows []string
	if populated {
		cardRows = []string{"Account alpha", "Live " + "·" + " Conn - " + "·" + " Up/s - " + "·" + " Down/s -", "  Provider  provider-a  [edit]", "  Countries  any  [edit]", "  Exit  us  [edit]", "  Allowed clients  any  [edit]", "  Pre-hop  ********  [edit]", "  Clear pre-hop  [P]", "  Post-hop  ********  [edit]", "  Clear post-hop  [O]", "  Password  waiting for the first login", "  Clone account  [c]", "  Remove account  [d]"}
	} else {
		cardRows = []string{"Account", "Select an account to see details."}
	}
	for i := range cardRows {
		cardRows[i] = ansi.Truncate(cardRows[i], cw-2, ".")
	}
	return literalFrame(width, height, "Accounts", goldenJoin(left, literalPane(cw, height-4, cardRows)), false)
}

func TestFullScreenSnapshots(t *testing.T) {
	t.Setenv("NETIVRI_ASCII", "1")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("LC_ALL", "C.UTF-8")
	cases := []struct {
		name, title string
		new         func(tui.Env) tui.Screen
		seed        func(tui.Screen, bool)
		want        func(int, int, bool) string
	}{
		{"providers", "Providers", func(e tui.Env) tui.Screen { return newProvidersScreenWith("", e, nil, nil) }, func(x tui.Screen, p bool) {
			two, three := 2, 3
			s := x.(*providersScreen)
			if p {
				s.setProviders(ProviderListResult{Providers: []ProviderInfo{{Name: "alpha", Nodes: &two, State: "ready"}, {Name: "beta", Nodes: &three, State: "ready"}}})
				s.selected = "alpha"
			} else {
				s.setProviders(ProviderListResult{})
			}
			s.updateCard()
		}, providerGoldenFrame},
		{"accounts", "Accounts", func(e tui.Env) tui.Screen { return newAccountsScreen("", e) }, func(x tui.Screen, p bool) {
			s := x.(*accountsScreen)
			if p {
				s.rows = []accountInfo{{Login: "alpha", Provider: "provider-a", Exit: "us"}, {Login: "beta", Provider: "provider-b", Exit: "de"}}
				s.syncRows("alpha")
			} else {
				s.rows = nil
				s.syncRows("")
			}
			s.refreshSelectedCard(true)
		}, accountsGoldenFrame},
		{"servers", "Servers", func(e tui.Env) tui.Screen { return newServersScreen("", e) }, func(x tui.Screen, p bool) {
			s := x.(*serversScreen)
			if p {
				s.rows = []serverView{{Name: "alpha", Protocol: "socks5", Port: 1080}, {Name: "beta", Protocol: "http", Port: 8080}}
			} else {
				s.rows = nil
			}
			s.syncWorkspace()
		}, serversGoldenFrame},
	}
	for _, tc := range cases {
		for _, pop := range []bool{false, true} {
			for _, size := range [][2]int{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
				t.Run(tc.name+map[bool]string{false: "/empty", true: "/populated"}[pop]+"/"+string(rune('0'+size[0]/10)), func(t *testing.T) {
					env := tui.Env{Theme: tui.NewTheme()}
					screen := tc.new(env)
					frame := tui.NewFrame(tui.Options{Env: env, Tabs: []tui.Tab{{Title: tc.title, New: func(tui.Env) tui.Screen { return screen }}}})
					_, _ = frame.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					tc.seed(screen, pop)
					switch s := screen.(type) {
					case *providersScreen:
						s.workspace.SetTheme(tui.NewTheme())
					case *serversScreen:
						s.workspace.SetTheme(tui.NewTheme())
					case *accountsScreen:
						s.workspace.SetTheme(tui.NewTheme())
					}
					got := ansi.Strip(frame.View().Content)
					want := tc.want(size[0], size[1], pop)
					if got != want {
						t.Errorf("%dx%d mismatch\n got: %q\nwant: %q", size[0], size[1], got, want)
					}
				})
			}
		}
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
				title := name
				if name == "providers" {
					title = "Providers"
					screen = newProvidersScreenWith("", env, nil, nil)
				} else if name == "accounts" {
					title = "Accounts"
					screen = newAccountsScreen("", env)
				} else {
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
