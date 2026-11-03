package main

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"errors"
	"fmt"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"net/url"
	"sort"
	"strings"
	"time"
)

type providersFetchFunc func(context.Context, string, fetch.Options) (*fetch.Result, error)

var providersFetch providersFetchFunc = fetch.Get
var providersNow = time.Now

type providerResultMsg struct {
	kind  string
	value any
	err   error
}
type providersScreen struct {
	home                              string
	env                               tui.Env
	get                               providersFetchFunc
	now                               func() time.Time
	table                             *tui.Table
	form                              *tui.Form
	confirm                           *tui.Confirm
	mode, busy, detail, message, hint string
	width, height                     int
	countries                         ProviderCountriesResult
	refreshLines                      []string
	refreshOffset                     int
	countryOffset                     int
}

func newProvidersScreen(h string, e tui.Env) tui.Screen {
	return newProvidersScreenWith(h, e, providersFetch, providersNow)
}
func newProvidersScreenWith(h string, e tui.Env, g providersFetchFunc, n func() time.Time) *providersScreen {
	if e.Home != "" {
		h = e.Home
	}
	if g == nil {
		g = providersFetch
	}
	if n == nil {
		n = providersNow
	}
	return &providersScreen{home: h, env: e, get: g, now: n, table: tui.NewTable([]tui.Column{{Title: "Name", Width: 18}, {Title: "Nodes", Width: 8}, {Title: "Last refresh", Width: 22}, {Title: "State", Width: 12}, {Title: "Countries", Width: 30}}, nil)}
}
func (s *providersScreen) Title() string        { return "Providers" }
func (s *providersScreen) CapturingInput() bool { return s.mode == "add" && s.form != nil }
func countryPageSize(height int) int            { return max(0, height-4) }
func (s *providersScreen) KeyHints() string {
	if s.mode == "countries" {
		return "Up/Down j/k PgUp/PgDn Home/End scroll  Esc back  r reload"
	}
	if s.mode == "add" {
		return s.form.KeyHints()
	}
	return "r reload  a add  u refresh  U refresh all  d remove  Enter countries"
}
func (s *providersScreen) Init() tea.Cmd { s.list(); return nil }
func (s *providersScreen) list() {
	v, e := ProviderList(s.home)
	if e != nil {
		s.setError(e)
	} else {
		s.setProviders(v)
	}
}
func (s *providersScreen) bg(l string, w func() tea.Msg) tea.Cmd {
	if s.env.Background != nil {
		return s.env.Background(l, w)
	}
	return func() tea.Msg { return w() }
}
func (s *providersScreen) load() tea.Cmd {
	s.busy = "Loading providers"
	return s.bg(s.busy, func() tea.Msg { v, e := ProviderList(s.home); return providerResultMsg{"list", v, e} })
}
func (s *providersScreen) fetchSafe(c context.Context, raw string, o fetch.Options) (*fetch.Result, error) {
	r, e := s.get(c, raw, o)
	if e == nil {
		return r, nil
	}
	ce := providerCLIError(e)
	if ce == nil {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "provider fetch failed", Exit: cli.ExitNetwork}
	}
	x := *ce
	x.Message = redactProviderSecrets(x.Message, raw, o.Proxy)
	x.Hint = redactProviderSecrets(x.Hint, raw, o.Proxy)
	return nil, &x
}
func (s *providersScreen) Update(m tea.Msg) (tui.Screen, tea.Cmd) {
	if s.mode == "countries" && s.height > 0 {
		s.countryOffset = min(max(0, s.countryOffset), max(0, len(s.countries.Countries)-countryPageSize(s.height)))
	}
	if x, ok := m.(tea.WindowSizeMsg); ok {
		s.width, s.height = x.Width, x.Height
		s.countryOffset = min(max(0, s.countryOffset), max(0, len(s.countries.Countries)-countryPageSize(x.Height)))
		if s.form != nil {
			s.form.Update(x)
		}
		return s, nil
	}
	if x, ok := m.(tui.BackgroundMsg); ok {
		m = x.Value
	}
	if x, ok := m.(providerResultMsg); ok {
		return s.result(x)
	}
	if x, ok := m.(tui.FormResultMsg); ok {
		if x.Canceled {
			s.form = nil
			s.mode = ""
			return s, nil
		}
		if s.mode == "add" {
			v := x.Values
			name, urlv, via := v["Name"], v["URL"], v["Via"]
			s.mode = "adding"
			s.busy = "Adding provider"
			return s, s.bg(s.busy, func() tea.Msg {
				r, e := ProviderAdd(s.home, ProviderAddOptions{Name: name, URL: urlv, Via: via}, s.fetchSafe, s.now)
				return providerResultMsg{"add", r, e}
			})
		}
		return s, nil
	}
	if x, ok := m.(tui.ConfirmResultMsg); ok {
		s.confirm = nil
		s.mode = ""
		if x.Confirmed && s.detail != "" {
			name := s.detail
			s.busy = "Removing provider"
			return s, s.bg(s.busy, func() tea.Msg { d, e := ProviderRemove(s.home, name); return providerResultMsg{"remove", d, e} })
		}
		s.detail = ""
		return s, nil
	}
	k, ok := m.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	if s.mode == "confirm" && s.confirm != nil {
		_, c := s.confirm.Update(m)
		return s, c
	}
	if s.mode == "add" {
		_, c := s.form.Update(m)
		return s, c
	}
	if s.busy != "" {
		return s, nil
	}
	if k.String() == "esc" && len(s.refreshLines) > 0 && s.mode == "" && s.busy == "" {
		s.refreshLines = nil
		s.refreshOffset = 0
		s.message = ""
		s.hint = ""
		return s, nil
	}
	if len(s.refreshLines) > 0 && s.mode == "" && s.busy == "" {
		step := max(1, s.height-3)
		switch k.String() {
		case "down", "j":
			s.refreshOffset = min(max(0, len(s.refreshLines)-1), s.refreshOffset+1)
		case "pgdown":
			s.refreshOffset = min(max(0, len(s.refreshLines)-1), s.refreshOffset+step)
		case "up", "k":
			s.refreshOffset = max(0, s.refreshOffset-1)
		case "pgup":
			s.refreshOffset = max(0, s.refreshOffset-step)
		case "home":
			s.refreshOffset = 0
		case "end":
			s.refreshOffset = max(0, len(s.refreshLines)-max(1, s.height-2))
		}
		if strings.Contains(" down j pgdown up k pgup home end ", " "+k.String()+" ") {
			return s, nil
		}
	}

	if s.mode == "countries" {
		visible := countryPageSize(s.height)
		maxOffset := max(0, len(s.countries.Countries)-visible)
		s.countryOffset = min(max(0, s.countryOffset), maxOffset)
		switch k.String() {
		case "down", "j":
			s.countryOffset = min(maxOffset, s.countryOffset+1)
		case "up", "k":
			s.countryOffset = max(0, s.countryOffset-1)
		case "pgdown":
			s.countryOffset = min(maxOffset, s.countryOffset+max(1, visible))
		case "pgup":
			s.countryOffset = max(0, s.countryOffset-max(1, visible))
		case "home":
			s.countryOffset = 0
		case "end":
			s.countryOffset = maxOffset
		case "esc":
			s.mode = ""
			s.countryOffset = 0
			s.refreshLines = nil
			s.refreshOffset = 0
			s.message = ""
			s.hint = ""
		case "r":
			s.busy = "Loading countries"
			return s, s.countriesCmd()
		}
		return s, nil
	}
	switch k.String() {
	case "r":
		return s, s.load()
	case "a":
		s.refreshLines = nil
		s.refreshOffset = 0
		s.form = &tui.Form{TitleText: "Add provider", Fields: []tui.Field{{Name: "Name", Kind: tui.TextField}, {Name: "URL", Kind: tui.PasswordField}, {Name: "Via", Kind: tui.PasswordField}}}
		s.mode = "add"
	case "u":
		if r, ok := s.table.SelectedRow(); ok {
			return s, s.refresh(r.Data.(string), false)
		}
	case "U":
		return s, s.refresh("", true)
	case "d":
		s.refreshLines = nil
		s.refreshOffset = 0
		if r, ok := s.table.SelectedRow(); ok {
			name := r.Data.(string)
			s.detail = name
			s.busy = "Checking provider"
			return s, s.bg(s.busy, func() tea.Msg {
				as, e := AccountList(s.home)
				if e != nil {
					return providerResultMsg{"preflight", nil, e}
				}
				var d []string
				for _, a := range as {
					p, _ := config.ParseProviderList(a.Provider)
					if hasProvider(p, name) {
						d = append(d, a.Login)
					}
				}
				return providerResultMsg{"preflight", d, nil}
			})
		}
	case "enter":
		s.refreshLines = nil
		s.refreshOffset = 0
		if r, ok := s.table.SelectedRow(); ok {
			s.detail = r.Data.(string)
			s.countryOffset = 0
			s.mode = "countries"
			s.busy = "Loading countries"
			return s, s.countriesCmd()
		}
	case "up", "down", "j", "k", "pgup", "pgdown", "home", "end":
		s.table.UpdateKey(k.String(), max(1, s.height-2))
	}
	return s, nil
}
func hasProvider(p []string, n string) bool {
	for _, x := range p {
		if x == n {
			return true
		}
	}
	return false
}
func (s *providersScreen) result(r providerResultMsg) (tui.Screen, tea.Cmd) {
	s.busy = ""
	switch r.kind {
	case "list":
		if r.err != nil {
			s.setError(r.err)
		} else {
			s.setProviders(r.value.(ProviderListResult))
		}
		return s, nil
	case "add":
		if r.err != nil {
			s.setError(r.err)
			s.mode = "add"
			for i := range s.form.Fields {
				if s.form.Fields[i].Kind == tui.PasswordField {
					s.form.Fields[i].Error = s.message + hintText(s.hint)
					s.form.Focus = i
					break
				}
			}
			return s, nil
		}
		x := r.value.(ProviderAddResult)
		s.message = fmt.Sprintf("Added %s: %d nodes", x.Name, x.Nodes)
		s.hint = ""
		s.form = nil
		s.mode = ""
		cmd := s.load()
		if s.env.Toast != nil {
			return s, tea.Batch(cmd, s.env.Toast(s.message))
		}
		return s, cmd
	case "refresh":
		rs, _ := r.value.([]ProviderRefreshResult)
		s.refreshLines = nil
		s.refreshOffset = 0
		for _, x := range rs {
			if x.OK {
				s.refreshLines = append(s.refreshLines, fmt.Sprintf("%s: refreshed (%d nodes)", x.Name, ptrInt(x.Nodes)))
			} else {
				m := "refresh failed"
				if x.Error != nil {
					m = x.Error["message"]
				}
				s.refreshLines = append(s.refreshLines, x.Name+": "+redactProviderSecrets(m))
			}
		}
		if r.err != nil {
			s.setError(r.err)
		} else {
			s.message = "Providers refreshed"
			s.hint = ""
		}
		return s, s.load()
	case "remove":
		if r.err != nil {
			s.setError(r.err)
			if ce := providerCLIError(r.err); ce != nil && ce.Code == "SUB_IN_USE" {
				if d, ok := r.value.([]string); ok {
					s.message += "; accounts: " + strings.Join(d, ", ")
				}
			}
		} else {
			s.message = "Provider removed"
			s.hint = ""
			s.detail = ""
		}
		return s, s.load()
	case "preflight":
		if r.err != nil {
			s.setError(r.err)
			return s, nil
		}
		d := r.value.([]string)
		p := "Remove provider " + s.detail + "?"
		if len(d) > 0 {
			p += " (used by accounts: " + strings.Join(d, ", ") + ")"
		}
		s.confirm = &tui.Confirm{Prompt: p}
		s.mode = "confirm"
		return s, nil
	case "countries":
		if r.err != nil {
			s.setError(r.err)
		} else {
			s.countries = r.value.(ProviderCountriesResult)
			s.countryOffset = 0
			s.message = ""
			s.hint = ""
		}
		return s, nil
	}
	return s, nil
}
func providerCLIError(e error) *cli.Error {
	var c *cli.Error
	if errors.As(e, &c) {
		return c
	}
	return nil
}
func ptrInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
func (s *providersScreen) setError(e error) {
	if c := providerCLIError(e); c != nil {
		s.message = s.scrub(c.Message)
		s.hint = s.scrub(c.Hint)
	} else {
		s.message = "Provider operation failed"
		s.hint = ""
	}
}
func (s *providersScreen) scrub(v string) string {
	return redactProviderSecrets(v, s.formValue("URL"), s.formValue("Via"))
}
func (s *providersScreen) formValue(n string) string {
	if s.form != nil {
		for _, f := range s.form.Fields {
			if f.Name == n {
				return f.Value
			}
		}
	}
	return ""
}
func redactProviderSecrets(text string, raws ...string) string {

	var tokens []string
	for _, raw := range raws {
		if raw == "" {
			continue
		}
		tokens = append(tokens, raw)
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if u.User != nil {
			tokens = append(tokens, u.User.String(), u.User.Username())
			if p, ok := u.User.Password(); ok {
				tokens = append(tokens, p)
			}
		}
		tokens = append(tokens, u.Host, u.Hostname(), u.Path, u.RawPath, u.RawQuery, u.RawFragment)
		for _, vs := range u.Query() {
			tokens = append(tokens, vs...)
		}
	}
	sort.Slice(tokens, func(i, j int) bool { return len(tokens[i]) > len(tokens[j]) })
	pairs := make([]string, 0, len(tokens)*2)
	last := ""
	for _, token := range tokens {
		if token != "" && token != last {
			pairs = append(pairs, token, "[redacted]")
			last = token
		}
	}
	if len(pairs) > 0 {
		text = strings.NewReplacer(pairs...).Replace(text)
	}
	return text
}
func hintText(h string) string {
	if h != "" {
		return " — " + h
	}
	return ""
}
func (s *providersScreen) refresh(n string, a bool) tea.Cmd {
	s.busy = "Refreshing providers"
	s.refreshLines = nil
	s.refreshOffset = 0
	return s.bg(s.busy, func() tea.Msg {
		r, e := ProviderRefresh(s.home, n, a, s.fetchSafe, s.now)
		return providerResultMsg{"refresh", r, e}
	})
}
func (s *providersScreen) countriesCmd() tea.Cmd {
	n := s.detail
	return s.bg("Loading countries", func() tea.Msg { r, e := ProviderCountries(s.home, n); return providerResultMsg{"countries", r, e} })
}
func (s *providersScreen) setProviders(v ProviderListResult) {
	rows := make([]tui.Row, 0, len(v.Providers))
	for _, p := range v.Providers {
		n, ts := "—", "—"
		if p.Nodes != nil {
			n = fmt.Sprint(*p.Nodes)
		}
		if p.FetchedAt != nil {
			ts = *p.FetchedAt
		}
		cc := make([]string, 0, len(p.Countries))
		for _, c := range p.Countries {
			cc = append(cc, fmt.Sprintf("%s:%d", c.Code, c.Nodes))
		}
		rows = append(rows, tui.Row{Cells: []string{p.Name, n, ts, p.State, strings.Join(cc, ",")}, Data: p.Name})
	}
	s.table.SetRows(rows)
}
func (s *providersScreen) View(w, h int) string {
	s.width, s.height = w, h
	if w <= 0 || h <= 0 {
		return ""
	}
	if len(s.refreshLines) > 0 && s.mode == "" && s.busy == "" {
		visible := max(1, h-2)
		start := min(s.refreshOffset, max(0, len(s.refreshLines)-visible))
		end := min(len(s.refreshLines), start+visible)
		lines := append([]string(nil), s.refreshLines[start:end]...)
		lines = append(lines, "PgUp/PgDn results  Esc back")
		if len(lines) > h {
			lines = lines[:h]
		}
		for i, line := range lines {
			if lipgloss.Width(line) > w {
				lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(line)
			}
		}
		return strings.Join(lines, "\n")
	}
	var lines []string
	if s.mode == "confirm" && s.confirm != nil {
		lines = strings.Split(s.confirm.View(w, h), "\n")
	} else if s.mode == "add" && s.form != nil {
		lines = strings.Split(s.form.View(w, h), "\n")
	} else if s.mode == "countries" {
		lines = []string{"Countries: " + s.detail}
		if s.busy != "" {
			lines = append(lines, s.busy+"…")
		} else if s.message != "" {
			lines = append(lines, s.message+hintText(s.hint))
		} else if s.countries.Name != s.detail || s.countries.Nodes == nil {
			lines = append(lines, "No stored snapshot")
		} else {
			lines = append(lines, fmt.Sprintf("%d nodes  Unknown: %d", *s.countries.Nodes, ptrInt(s.countries.Unknown)))
			visible := countryPageSize(h)
			maxOffset := max(0, len(s.countries.Countries)-visible)
			s.countryOffset = min(max(0, s.countryOffset), maxOffset)
			start := s.countryOffset
			end := min(len(s.countries.Countries), start+visible)
			for _, c := range s.countries.Countries[start:end] {
				lines = append(lines, fmt.Sprintf("%s: %d", c.Code, c.Nodes))
			}
			if len(s.countries.Countries) > visible && visible > 0 {
				lines = append(lines, fmt.Sprintf("%d-%d of %d", start+1, end, len(s.countries.Countries)))
			}
		}
		lines = append(lines, "Esc back  r reload")
	} else {
		if s.busy != "" {
			lines = append(lines, s.busy+"…")
		}
		if s.message != "" {
			lines = append(lines, s.message+hintText(s.hint))
		}
		lines = append(lines, s.refreshLines...)
		if len(s.refreshLines) > 0 {
			lines = append(lines, "Esc clears refresh results")
		}
		rows := strings.Split(s.table.View(w, max(0, h-len(lines)-1)), "\n")
		lines = append(lines, "NAME  NODES  LAST REFRESH  STATE  COUNTRIES")
		lines = append(lines, rows...)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(l)
		}
	}
	return strings.Join(lines, "\n")
}
