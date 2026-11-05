package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// The Settings screen: the keys of config.ktav (listen, auth, fetch, chain),
// edited one value at a time through the same operations as the CLI.

type settingsLoadedMsg struct {
	cfg *config.Config
	err error
}
type settingsSavedMsg struct {
	key string
	res ConfigSetResult
	err error
}

type settingsScreen struct {
	home          string
	env           tui.Env
	theme         tui.Theme
	workspace     *tui.Workspace
	list          *tui.List
	width, height int
	groups        []string
	selected      string
	cfg           *config.Config
	loadErr       error
	prompt        *tui.Prompt
	promptKey     string
	dialog        tui.Modal
	load          func(string) (*config.Config, error)
	set           func(string, string, string) (ConfigSetResult, error)
}

func newSettingsScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	l := tui.NewList([]tui.ListColumn{{Title: "Group", Width: 12, Flex: 1}, {Title: "Keys", Width: 8}}, nil)
	l.Title = "Settings"
	l.AddLabel = ""
	theme := env.Theme
	if theme.Accent == nil {
		theme = tui.NewTheme()
	}
	s := &settingsScreen{home: home, env: env, theme: theme, list: l, load: loadConfig, set: ConfigSet}
	s.groups = settingsGroupIDs()
	if len(s.groups) > 0 {
		s.selected = s.groups[0]
	}
	s.workspace = tui.NewWorkspace(l, s.buildCard(s.selected))
	s.workspace.SetTheme(theme)
	s.syncRows()
	return s
}

func settingsGroupIDs() []string {
	var ids []string
	seen := map[string]bool{}
	for _, k := range ConfigKeys() {
		if !seen[k.Group] {
			seen[k.Group] = true
			ids = append(ids, k.Group)
		}
	}
	return ids
}

func (s *settingsScreen) Title() string        { return "Settings" }
func (s *settingsScreen) CapturingInput() bool { return s.dialog != nil }
func (s *settingsScreen) Breadcrumb() string {
	if s.width < 100 && s.workspace.Drilled {
		return "Settings / " + s.groupLabel(s.selected)
	}
	return ""
}
func (s *settingsScreen) FocusPane(reverse bool) bool {
	if s.dialog != nil || s.width < 100 {
		return false
	}
	if reverse {
		if s.workspace.Focus() == tui.PaneCard {
			s.workspace.SetFocus(tui.PaneList)
			return true
		}
		return false
	}
	if s.workspace.Focus() == tui.PaneList {
		s.workspace.SetFocus(tui.PaneCard)
		return true
	}
	return false
}
func (s *settingsScreen) SetPaneFocus(p int) {
	if s.width >= 100 {
		s.workspace.SetFocus(p)
	}
}
func (s *settingsScreen) Bindings() tui.ActionHints {
	b := append(tui.ActionHints{}, s.list.Bindings()...)
	return append(b, s.workspace.Card.Bindings()...)
}
func (s *settingsScreen) KeyHints() string {
	if s.dialog != nil {
		return "Dialog: confirm or cancel"
	}
	return "↑/↓ select  Enter edit"
}

func (s *settingsScreen) bg(label string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(label, w)
	}
	return func() tea.Msg { return tui.BackgroundMsg{Label: label, Value: w()} }
}

func (s *settingsScreen) loadCmd() tea.Cmd {
	return s.bg("Reading the settings", func() tea.Msg {
		cfg, err := s.load(s.home)
		return settingsLoadedMsg{cfg: cfg, err: err}
	})
}
func (s *settingsScreen) Init() tea.Cmd { return s.loadCmd() }

func (s *settingsScreen) toast(text string) tea.Cmd {
	if text == "" || s.env.Toast == nil {
		return nil
	}
	return s.env.Toast(text)
}

func settingsErrorText(err error) string {
	if ce, ok := err.(*cli.Error); ok {
		if ce.Hint != "" {
			return ce.Message + " (" + ce.Hint + ")"
		}
		return ce.Message
	}
	return "settings operation failed"
}

func (s *settingsScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
	switch x := m.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = x.Width, x.Height
		s.workspace.SetSize(x.Width, x.Height)
		return s, nil
	case tui.ThemeMsg:
		s.theme = x.Theme
		s.workspace.SetTheme(x.Theme)
		if s.prompt != nil {
			s.prompt.SetTheme(x.Theme)
		}
		return s, nil
	case tui.BackgroundMsg:
		return s.Update(x.Value)
	case settingsLoadedMsg:
		if x.err != nil || x.cfg == nil {
			s.cfg, s.loadErr = config.Default(), x.err
		} else {
			s.cfg, s.loadErr = x.cfg, nil
		}
		s.refresh()
		return s, nil
	case settingsSavedMsg:
		if x.err != nil {
			if s.prompt != nil {
				s.prompt.SetSaving(false)
				s.prompt.SetError(errors.New(settingsErrorText(x.err)))
			}
			return s, nil
		}
		s.dialog, s.prompt, s.promptKey = nil, nil, ""
		s.refresh()
		text := "Settings saved: " + x.key
		if x.res.RestartNeeded {
			text += "; restart the angel to apply it"
		}
		return s, tea.Batch(s.toast(text), s.loadCmd())
	case tui.PromptResultMsg:
		if s.dialog == nil || s.prompt == nil {
			return s, nil
		}
		if x.Canceled {
			s.dialog, s.prompt, s.promptKey = nil, nil, ""
			return s, nil
		}
		key, value := s.promptKey, x.Value
		s.prompt.SetSaving(true)
		return s, s.bg("Saving the settings", func() tea.Msg {
			res, err := s.set(s.home, key, value)
			return settingsSavedMsg{key: key, res: res, err: err}
		})
	}
	if s.dialog != nil {
		_, c := s.dialog.Update(m)
		return s, c
	}
	activated := s.workspace.Update(m)
	if row, ok := s.list.SelectedRow(); ok {
		if name, _ := row.Data.(string); name != "" && name != s.selected {
			s.selected = name
			s.refresh()
		}
	}
	if activated && s.workspace.Focus() == tui.PaneCard {
		return s.activateCard()
	}
	return s, nil
}

func (s *settingsScreen) activateCard() (tui.Screen, tea.Cmd) {
	card := s.workspace.Card
	if card == nil || card.Selected < 0 || card.Selected >= len(card.Lines) {
		return s, nil
	}
	if k, ok := ConfigKeyByName(card.Lines[card.Selected].ID); ok {
		s.promptFor(k)
	}
	return s, nil
}

func (s *settingsScreen) promptFor(k ConfigKey) {
	title := "Settings · " + k.Key
	switch k.Kind {
	case ConfigKeyPort:
		min, max := 1, 65535
		s.prompt = tui.NewPrompt(tui.NumberPrompt, title, k.Label, k.Hint, settingsRawValue(s.cfg, k.Key))
		s.prompt.Min, s.prompt.Max = &min, &max
	case ConfigKeyBool:
		s.prompt = tui.NewPrompt(tui.ChoicePrompt, title, k.Label, k.Hint, settingsRawValue(s.cfg, k.Key))
		s.prompt.Options = []tui.PromptOption{{Label: "Yes", Value: "true"}, {Label: "No", Value: "false"}}
	case ConfigKeyPassword:
		hint := k.Hint + "; it is stored as a hash and never shown"
		if raw, ok := ConfigRawValue(s.cfg, k.Key); ok && raw != "" {
			hint += "; none or empty clears"
		}
		s.prompt = tui.NewPrompt(tui.SecretPrompt, title, k.Label, hint, "")
	case ConfigKeyProxy:
		hint := k.Hint
		if pv, ok := s.proxyValue(k.Key); ok {
			hint += "; current: " + settingsProxyText(pv) + " — enter a new URL; none or empty clears"
		} else {
			hint += "; none or empty clears"
		}
		s.prompt = tui.NewPrompt(tui.SecretPrompt, title, k.Label, hint, "")
	default:
		s.prompt = tui.NewPrompt(tui.TextPrompt, title, k.Label, k.Hint, settingsRawValue(s.cfg, k.Key))
	}
	s.prompt.SetTheme(s.theme)
	s.promptKey = k.Key
	s.dialog = s.prompt
}

func settingsRawValue(cfg *config.Config, key string) string {
	v, _ := ConfigRawValue(cfg, key)
	return v
}

func (s *settingsScreen) proxyValue(key string) (configProxyValue, bool) {
	v := ConfigDisplayValue(s.cfg, key)
	pv, ok := v.(configProxyValue)
	return pv, ok
}

// refresh rebuilds the rows and the card of the selected row from the current
// data, keeping the selected line and the scroll position of the card.
func (s *settingsScreen) refresh() {
	s.syncRows()
	old := s.workspace.Card
	card := s.buildCard(s.selected)
	card.SetTheme(s.theme)
	if old != nil && old.Title == card.Title {
		var id string
		if old.Selected >= 0 && old.Selected < len(old.Lines) {
			id = old.Lines[old.Selected].ID
		}
		for i, l := range card.Lines {
			if l.ID == id {
				card.Selected = i
			}
		}
		card.Scroll = old.Scroll
		card.Focused = old.Focused
	}
	s.workspace.Card = card
	if s.width > 0 {
		s.workspace.SetSize(s.width, s.height)
	}
}

func (s *settingsScreen) syncRows() {
	counts := map[string]int{}
	for _, k := range ConfigKeys() {
		counts[k.Group]++
	}
	var rows []tui.Row
	for _, g := range s.groups {
		rows = append(rows, tui.Row{Cells: []string{s.groupLabel(g), fmt.Sprintf("%d keys", counts[g])}, Data: g})
	}
	s.list.SetRows(rows)
	s.list.SelectData(s.selected)
}

func (s *settingsScreen) groupLabel(group string) string {
	for _, k := range ConfigKeys() {
		if k.Group == group && k.GroupLabel != "" {
			return k.GroupLabel
		}
	}
	return group
}

func (s *settingsScreen) buildCard(group string) *tui.Card {
	var lines []tui.CardLine
	for _, k := range ConfigKeys() {
		if k.Group != group {
			continue
		}
		// The value is a safe state label ("set"/"not set", redacted scheme://host:port),
		// never the secret itself — no Secret flag (same convention as tui_accounts.go pre/post lines).
		lines = append(lines, tui.CardLine{ID: k.Key, Label: k.Label, Value: s.displayValue(k),
			Kind: tui.CardField})
	}
	c := tui.NewCard(s.groupLabel(group), lines)
	if s.loadErr != nil {
		c.Info = []string{"The configuration file could not be read."}
	} else {
		c.Info = []string{"Enter edits the value."}
	}
	return c
}

func (s *settingsScreen) displayValue(k ConfigKey) string {
	if s.cfg == nil { // before the first load
		return "-"
	}
	v := ConfigDisplayValue(s.cfg, k.Key)
	if v == nil {
		return "-"
	}
	switch k.Kind {
	case ConfigKeyPort:
		if n, ok := v.(int); ok {
			return fmt.Sprint(n)
		}
	case ConfigKeyBool:
		if b, ok := v.(bool); ok {
			if b {
				return "true"
			}
			return "false"
		}
	case ConfigKeyPassword:
		if str, ok := v.(string); ok {
			return str
		}
	case ConfigKeyProxy:
		if pv, ok := v.(configProxyValue); ok {
			return settingsProxyText(pv)
		}
	}
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprint(v)
}

// settingsProxyText renders a proxy for display without its credentials.
func settingsProxyText(pv configProxyValue) string {
	raw := strings.TrimSpace(pv.Value)
	if raw == "" {
		return "-"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted]"
	}
	secret := u
	secret.User = nil
	return secret.Scheme + "://" + secret.Host
}

// ParityRefs reports what the screen offers: the list and every editable key.
func (s *settingsScreen) ParityRefs() []string {
	var refs []string
	seen := map[string]bool{}
	for _, g := range s.groups {
		for _, r := range parityRefs("settings", s.list, s.buildCard(g)) {
			if !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
	}
	return refs
}

func (s *settingsScreen) View(w, h int) string {
	s.width, s.height = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	if s.dialog != nil {
		return s.dialog.View(w, h)
	}
	return s.workspace.View(w, h)
}
