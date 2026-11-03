package tui

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme contains the terminal palette, semantic styles, and layout glyphs used by the TUI.
type Theme struct {
	ASCII   bool
	NoColor bool
	Dark    bool

	Accent  color.Color
	Muted   color.Color
	Success color.Color
	Warning color.Color
	Error   color.Color

	SelectedRow    lipgloss.Style
	FocusedPane    lipgloss.Style
	UnfocusedPane  lipgloss.Style
	Button         lipgloss.Style
	FocusedButton  lipgloss.Style
	DisabledButton lipgloss.Style
	DangerButton   lipgloss.Style
	Tab            lipgloss.Style
	ActiveTab      lipgloss.Style
	Dialog         lipgloss.Style
}

// NewTheme reads terminal preferences from the environment and defaults to dark mode.
func NewTheme() Theme { return newTheme(true, envValue, noColorPresent()) }

// NewThemeFor builds a theme for the requested background brightness, retaining terminal preferences.
func NewThemeFor(dark bool) Theme { return newTheme(dark, envValue, noColorPresent()) }

func envValue(key string) string { return os.Getenv(key) }
func noColorPresent() bool       { _, present := os.LookupEnv("NO_COLOR"); return present }

type environment func(string) string

func newTheme(dark bool, getenv environment, noColor bool) Theme {
	ascii := getenv("NETIVRI_ASCII") == "1"
	if !ascii {
		locale := getenv("LC_ALL")
		if locale == "" {
			locale = getenv("LC_CTYPE")
		}
		if locale == "" {
			locale = getenv("LANG")
		}
		if locale != "" && !strings.Contains(strings.ToLower(locale), "utf") {
			ascii = true
		}
	}
	accent, muted := "#5b3fd0", "#666666"
	success, warning, failure := "#16723c", "#855c00", "#a32626"
	if dark {
		accent, muted, success, warning, failure = "#b4a1ff", "#aaaaaa", "#65d68a", "#f0c45b", "#ff7777"
	}
	cAccent, cMuted := lipgloss.Color(accent), lipgloss.Color(muted)
	cSuccess, cWarning, cError := lipgloss.Color(success), lipgloss.Color(warning), lipgloss.Color(failure)
	plain := lipgloss.NewStyle()
	colored := func(c color.Color) lipgloss.Style {
		if noColor {
			return plain
		}
		return plain.Foreground(c)
	}
	return Theme{
		ASCII: ascii, NoColor: noColor, Dark: dark,
		Accent: cAccent, Muted: cMuted, Success: cSuccess, Warning: cWarning, Error: cError,
		SelectedRow: colored(cAccent).Bold(true),
		FocusedPane: colored(cAccent).Bold(true), UnfocusedPane: colored(cMuted),
		Button: plain, FocusedButton: colored(cAccent).Bold(true),
		DisabledButton: colored(cMuted), DangerButton: colored(cError).Bold(true),
		Tab: plain, ActiveTab: colored(cAccent).Bold(true), Dialog: plain,
	}
}

// FocusMark is a visible focus indicator that remains available without color.
func (t Theme) FocusMark() string {
	if t.ASCII {
		return ">"
	}
	return "›"
}

// Ellipsis returns a single-cell truncation marker in either glyph mode.
func (t Theme) Ellipsis() string {
	if t.ASCII {
		return "."
	}
	return "…"
}

// Style applies a semantic foreground color, retaining a non-color focus cue when color is disabled.
func (t Theme) Style(c color.Color) lipgloss.Style {
	if t.NoColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	return lipgloss.NewStyle().Foreground(c)
}
