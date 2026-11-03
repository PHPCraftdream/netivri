package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestThemeLocaleAndASCII(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		noColor   bool
		wantASCII bool
	}{
		{name: "default utf-8 locale", env: map[string]string{"LANG": "en_US.UTF-8"}},
		{name: "explicit ascii", env: map[string]string{"NETIVRI_ASCII": "1", "LANG": "en_US.UTF-8"}, wantASCII: true},
		{name: "override false is not ascii", env: map[string]string{"NETIVRI_ASCII": "true", "LANG": "en_US.UTF-8"}},
		{name: "LC_ALL precedence", env: map[string]string{"LC_ALL": "C.UTF-8", "LC_CTYPE": "C", "LANG": "C"}},
		{name: "LC_CTYPE precedence", env: map[string]string{"LC_CTYPE": "C.UTF-8", "LANG": "C"}},
		{name: "non utf locale", env: map[string]string{"LANG": "C"}, wantASCII: true},
		{name: "empty locales", env: map[string]string{}},
		{name: "present empty NO_COLOR", env: map[string]string{"NO_COLOR": ""}, noColor: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			get := func(k string) string { return tc.env[k] }
			got := newTheme(true, get, tc.noColor)
			if got.ASCII != tc.wantASCII {
				t.Fatalf("ASCII = %v, want %v", got.ASCII, tc.wantASCII)
			}
			if got.NoColor != tc.noColor {
				t.Fatalf("NoColor = %v, want %v", got.NoColor, tc.noColor)
			}
		})
	}
}

func TestThemeDarkAndLightPalettes(t *testing.T) {
	get := func(string) string { return "" }
	dark, light := newTheme(true, get, false), newTheme(false, get, false)
	if !dark.Dark || light.Dark {
		t.Fatalf("unexpected dark flags: %v, %v", dark.Dark, light.Dark)
	}
	if dark.Accent == light.Accent || dark.Muted == light.Muted || dark.Success == light.Success || dark.Warning == light.Warning || dark.Error == light.Error {
		t.Fatal("dark and light palettes should differ")
	}
	if NewThemeFor(false).Dark {
		t.Fatal("NewThemeFor(false) selected dark mode")
	}
}

func TestThemeSemanticStylesAndNoColor(t *testing.T) {
	get := func(string) string { return "" }
	theme := newTheme(true, get, false)
	if theme.Accent == nil || theme.Muted == nil || theme.Success == nil || theme.Warning == nil || theme.Error == nil {
		t.Fatal("semantic palette contains an empty color")
	}
	for name, style := range map[string]lipgloss.Style{
		"selected row": theme.SelectedRow, "focused pane": theme.FocusedPane,
		"unfocused pane": theme.UnfocusedPane, "button": theme.Button,
		"focused button": theme.FocusedButton, "disabled button": theme.DisabledButton,
		"danger button": theme.DangerButton, "tab": theme.Tab,
		"active tab": theme.ActiveTab, "dialog": theme.Dialog,
	} {
		if strings.TrimSpace(ansi.Strip(style.Render("x"))) != "x" {
			t.Errorf("%s style unexpectedly alters text", name)
		}
	}
	plain := newTheme(true, get, true)
	if !plain.NoColor {
		t.Fatal("NoColor not recorded")
	}
	for name, style := range map[string]lipgloss.Style{
		"selected row": plain.SelectedRow, "focused pane": plain.FocusedPane,
		"unfocused pane": plain.UnfocusedPane, "button": plain.Button,
		"focused button": plain.FocusedButton, "disabled button": plain.DisabledButton,
		"danger button": plain.DangerButton, "tab": plain.Tab,
		"active tab": plain.ActiveTab, "dialog": plain.Dialog,
	} {
		if style.GetForeground() != (lipgloss.NoColor{}) {
			t.Errorf("%s has a foreground color in no-color mode", name)
		}
		if strings.TrimSpace(ansi.Strip(style.Render("x"))) != "x" {
			t.Errorf("%s style changes rendered text", name)
		}
	}
	if !plain.Style(plain.Accent).GetBold() || !plain.Style(plain.Accent).GetReverse() {
		t.Fatal("no-color semantic style must retain visible non-color emphasis")
	}
}

func TestThemeGlyphFallbackWidths(t *testing.T) {
	unicodeTheme := Theme{}
	asciiTheme := Theme{ASCII: true}
	if unicodeTheme.FocusMark() != "›" || asciiTheme.FocusMark() != ">" {
		t.Fatal("unexpected focus mark fallback")
	}
	if unicodeTheme.Ellipsis() != "…" || asciiTheme.Ellipsis() != "." {
		t.Fatal("unexpected ellipsis fallback")
	}
	if len([]rune(unicodeTheme.Ellipsis())) != 1 || len(asciiTheme.Ellipsis()) != 1 {
		t.Fatal("ellipsis markers must occupy one cell")
	}
}
