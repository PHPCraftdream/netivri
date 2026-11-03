package tui

import (
	"charm.land/bubbles/v2/help"
	bubbleskey "charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

type ActionHints []bubbleskey.Binding

var globalHelpBindings = []bubbleskey.Binding{bubbleskey.NewBinding(bubbleskey.WithKeys("q"), bubbleskey.WithHelp("q", "quit")), bubbleskey.NewBinding(bubbleskey.WithKeys("?"), bubbleskey.WithHelp("?", "help"))}
var globalShortBindings = []bubbleskey.Binding{globalHelpBindings[0], globalHelpBindings[1], bubbleskey.NewBinding(bubbleskey.WithKeys("tab"), bubbleskey.WithHelp("tab", "focus")), bubbleskey.NewBinding(bubbleskey.WithKeys("left", "right"), bubbleskey.WithHelp("←/→", "tabs"))}
var _ help.KeyMap = ActionHints{}

func (a ActionHints) ShortHelp() []bubbleskey.Binding { return []bubbleskey.Binding(a) }
func (a ActionHints) FullHelp() [][]bubbleskey.Binding {
	if len(a) == 0 {
		return nil
	}
	return [][]bubbleskey.Binding{[]bubbleskey.Binding(a)}
}
func HintLine(bindings []bubbleskey.Binding, width int) string {
	return themedHintLine(bindings, width, NewTheme())
}
func themedHintLine(bindings []bubbleskey.Binding, width int, theme Theme) string {
	model := help.New()
	model.SetWidth(max(0, width))
	model.Styles = themedHelpStyles(theme)
	model.ShortSeparator = " · "
	if theme.ASCII {
		model.ShortSeparator = " | "
	}
	model.Ellipsis = theme.Ellipsis()
	bindings = append([]bubbleskey.Binding(nil), bindings...)
	if theme.ASCII {
		for i := range bindings {
			h := bindings[i].Help()
			h.Key = strings.NewReplacer("←", "<", "→", ">", "↑", "^", "↓", "v", "›", ">", "…", ".").Replace(h.Key)
			bindings[i].SetHelp(h.Key, h.Desc)
		}
	}
	return ansi.Truncate(model.ShortHelpView(ActionHints(bindings)), width, theme.Ellipsis())
}
func themedHelpStyles(theme Theme) help.Styles {
	styles := help.DefaultStyles(theme.Dark)
	styles.ShortKey = theme.ActiveTab
	styles.ShortDesc = theme.UnfocusedPane
	styles.FullKey = theme.ActiveTab
	styles.FullDesc = theme.UnfocusedPane
	plain := lipgloss.NewStyle()
	styles.ShortSeparator = plain
	styles.Ellipsis = plain
	styles.FullSeparator = plain
	if theme.NoColor {
		styles.ShortKey, styles.ShortDesc = plain, plain
		styles.FullKey, styles.FullDesc = plain, plain
	}
	return styles
}
func legacyHintBindings(text string) []bubbleskey.Binding {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	parts := strings.Fields(text)
	var result []bubbleskey.Binding
	for _, part := range parts {
		result = append(result, bubbleskey.NewBinding(bubbleskey.WithKeys(part), bubbleskey.WithHelp(part, part)))
	}
	return result
}
