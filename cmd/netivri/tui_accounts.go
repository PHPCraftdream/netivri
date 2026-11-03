package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

type accountsStatusSource func(string) (angel.Status, error)
type accountsLoaded struct {
	Rows       []accountInfo
	Err        error
	Generation uint64
}
type accountsPollResult struct {
	Status     angel.Status
	Err        error
	At         time.Time
	Generation uint64
}
type accountsChoicesLoaded struct {
	Providers []string
	Countries ProviderCountriesAllResult
	Err       error
}
type accountsDetailLoaded struct {
	Result AccountShowResult
	Err    error
}
type accountsMutationDone struct {
	Rows   []accountInfo
	Err    error
	Notice string
	Values map[string]string
}
type accountsRateTick struct{}
type accountsFormState struct {
	Kind, Login string
	Form        *tui.Form
	Original    accountInfo
}
type accountsFormModal struct {
	screen *accountsScreen
	form   *tui.Form
}

func (m accountsFormModal) CapturingInput() bool { return true }
func (m accountsFormModal) View(w, h int) string { return m.form.View(w, h) }
func (m accountsFormModal) Update(msg tea.Msg) (tui.Modal, tea.Cmd) {
	providerFocus := -1
	for i := range m.form.Fields {
		if m.form.Fields[i].Kind == tui.MultiChoiceField {
			providerFocus = i
			break
		}
	}
	beforeFocus := m.form.Focus
	screen, cmd := m.form.Update(msg)
	m.form = screen.(*tui.Form)
	if k, ok := msg.(tea.KeyPressMsg); ok && beforeFocus == providerFocus && providerFocus >= 0 && (k.String() == "space" || k.String() == " ") {
		p := &m.form.Fields[providerFocus]
		if p.Value == "any" {
			p.Selected = []string{"any"}
		} else {
			p.Selected = removeAccount(p.Selected, "any")
		}
	}
	for i := range m.form.Fields {
		f := &m.form.Fields[i]
		if f.Kind == tui.TextField && strings.HasPrefix(f.Name, "Country") {
			providers := []string{}
			for j := range m.form.Fields {
				p := &m.form.Fields[j]
				if p.Kind == tui.MultiChoiceField {
					providers = append(providers, p.Selected...)
				}
			}
			f.Name = m.screen.countryFieldHint(providers)
			break
		}
	}
	return m, cmd
}
func removeAccount(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

type accountsScreen struct {
	env                      tui.Env
	status                   accountsStatusSource
	table                    *tui.Table
	rows                     []accountInfo
	live                     map[string]angel.AccountActivity
	rates                    map[string][2]string
	previous                 map[string]angel.AccountActivity
	previousAt               time.Time
	providers                []string
	providerCountries        map[string][]string
	loading, detail, polling bool
	detailResult             *AccountShowResult
	message                  string
	statusMessage            string
	width, height            int
	form                     *accountsFormState
	loadGeneration           uint64
	pollGeneration           uint64
	pollWait                 func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
}

func newAccountsScreen(home string, env tui.Env) tui.Screen {
	env.Home = home
	s := &accountsScreen{env: env, live: map[string]angel.AccountActivity{}, rates: map[string][2]string{}, previous: map[string]angel.AccountActivity{}, providerCountries: map[string][]string{}, status: func(h string) (angel.Status, error) {
		ctx, c := context.WithTimeout(context.Background(), angel.ProbeTimeout)
		defer c()
		_, st, e := angelProbeLive(ctx, h)
		return st, e
	}}
	s.table = tui.NewTable([]tui.Column{{Title: "LOGIN"}, {Title: "PROVIDER"}, {Title: "COUNTRY"}, {Title: "EXIT"}, {Title: "CLIENTS"}, {Title: "HOPS"}, {Title: "PASSWORD"}, {Title: "CONN"}, {Title: "UP/s"}, {Title: "DOWN/s"}}, nil)
	return s
}
func (s *accountsScreen) Title() string        { return "Accounts" }
func (s *accountsScreen) CapturingInput() bool { return s.detail }
func (s *accountsScreen) KeyHints() string {
	return "Enter show  a add  e edit  c clone  d delete  r reload  q quit"
}
func (s *accountsScreen) UpdateFormResult(m tui.FormResultMsg) (tui.Screen, tea.Cmd) {
	if m.Canceled {
		s.form = nil
		return s, nil
	}
	if m.Err != nil {
		if s.form != nil && s.form.Form != nil {
			s.message = accountsSafeFormError(m.Err, s.form.Form)
			for i := range s.form.Form.Fields {
				if s.form.Form.Fields[i].Kind == tui.PasswordField {
					s.form.Form.Fields[i].Error = s.message
				}
			}
		} else {
			s.message = accountsErrorText(m.Err)
		}
		if s.form != nil && s.form.Form != nil {
			f := s.form.Form
			f.Fields[0].Error = s.message
			f.Focus = 0
			return s, s.env.OpenModal(accountsFormModal{screen: s, form: f})
		}
		return s, nil
	}
	return s, s.mutate(m.Values)
}
func (s *accountsScreen) Init() tea.Cmd { return tea.Batch(s.reload(), s.loadChoices(), s.rateTick()) }
func (s *accountsScreen) now() time.Time {
	if s.env.Now != nil {
		return s.env.Now()
	}
	return time.Now()
}
func (s *accountsScreen) bg(label string, f func() tea.Msg) tea.Cmd {
	if s.env.Background == nil {
		return nil
	}
	return s.env.Background(label, f)
}
func (s *accountsScreen) reload() tea.Cmd {
	s.loading = true
	s.loadGeneration++
	generation := s.loadGeneration
	return s.bg("accounts", func() tea.Msg {
		r, e := AccountList(s.env.Home)
		return accountsLoaded{Rows: r, Err: e, Generation: generation}
	})
}
func (s *accountsScreen) loadChoices() tea.Cmd {
	return s.bg("account choices", func() tea.Msg {
		pl, e := ProviderList(s.env.Home)
		if e != nil {
			return accountsChoicesLoaded{Err: e}
		}
		co, e := ProviderCountriesAll(s.env.Home)
		if e != nil {
			return accountsChoicesLoaded{Err: e}
		}
		ps := make([]string, 0, len(pl.Providers))
		for _, p := range pl.Providers {
			ps = append(ps, p.Name)
		}
		return accountsChoicesLoaded{ps, co, nil}
	})
}
func (s *accountsScreen) rateTick() tea.Cmd {
	wait := s.pollWait
	if wait == nil {
		wait = tea.Tick
	}
	timer := wait(time.Second, func(time.Time) tea.Msg { return accountsRateTick{} })
	return s.bg("account activity timer", func() tea.Msg { return timer() })
}
func (s *accountsScreen) Update(msg tea.Msg) (tui.Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = max(0, m.Width)
		s.height = max(0, m.Height)
		return s, nil
	case tui.BackgroundMsg:
		return s, s.updateBackground(m.Value)
	case tui.FormResultMsg:
		return s.UpdateFormResult(m)
	case accountsRateTick:
		if s.polling {
			return s, nil
		}
		s.polling = true
		s.pollGeneration++
		generation := s.pollGeneration
		return s, s.bg("account activity", func() tea.Msg {
			st, e := s.status(s.env.Home)
			return accountsPollResult{Status: st, Err: e, At: s.now(), Generation: generation}
		})
	case accountsLoaded:
		if m.Generation != s.loadGeneration {
			return s, nil
		}
		s.loading = false
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			return s, nil
		}
		selected := ""
		if row, ok := s.table.SelectedRow(); ok {
			selected = row.Data.(accountInfo).Login
		}
		s.rows = m.Rows
		s.table.SetRows(s.makeRows())
		if selected != "" {
			for i, row := range s.rows {
				if row.Login == selected {
					s.table.Selected = i
					break
				}
			}
		}
		return s, nil
	case accountsPollResult:
		if m.Generation != s.pollGeneration {
			return s, nil
		}
		s.polling = false
		if m.Err != nil {
			s.applyStatus(angel.Status{}, m.Err, m.At)
		} else {
			s.applyStatus(m.Status, nil, m.At)
		}
		s.table.SetRows(s.makeRows())
		return s, s.rateTick()
	case accountsChoicesLoaded:
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
			return s, nil
		}
		s.providers = m.Providers
		s.providerCountries = map[string][]string{}
		for _, p := range m.Countries.Providers {
			codes := make([]string, 0, len(p.Countries))
			for _, c := range p.Countries {
				codes = append(codes, c.Code)
			}
			s.providerCountries[p.Name] = codes
		}
		return s, nil
	case accountsDetailLoaded:
		if m.Err != nil {
			s.message = accountsErrorText(m.Err)
		} else {
			s.detailResult = &m.Result
			s.detail = true
		}
		return s, nil
	case accountsMutationDone:
		s.loading = false
		if m.Err != nil {
			if s.form == nil || s.form.Form == nil {
				s.message = accountsErrorText(m.Err)
				return s, nil
			}
			s.message = accountsSafeFormError(m.Err, s.form.Form)
			f := s.form.Form
			f.Fields[0].Error = s.message
			f.Focus = 0
			return s, s.env.OpenModal(accountsFormModal{screen: s, form: f})
		}
		s.rows = m.Rows
		s.table.SetRows(s.makeRows())
		s.message = m.Notice
		s.form = nil
		return s, nil
	case tui.ConfirmResultMsg:
		if !m.Confirmed {
			return s, nil
		}
		if r, ok := s.table.SelectedRow(); ok {
			login := r.Data.(accountInfo).Login
			return s, s.bg("remove account", func() tea.Msg {
				e := AccountRemove(s.env.Home, login)
				rows := []accountInfo(nil)
				if e == nil {
					rows, e = AccountList(s.env.Home)
				}
				return accountsMutationDone{Rows: rows, Err: e, Notice: "Account removed"}
			})
		}
		return s, nil
	case tea.KeyPressMsg:
		k := m.String()
		if s.detail {
			if k == "esc" || k == "backspace" {
				s.detail = false
				s.detailResult = nil
			}
			return s, nil
		}
		switch k {
		case "up", "down", "pgup", "pgdown", "home", "end", "j", "k":
			s.table.UpdateKey(k, max(1, s.height-5))
		case "r":
			return s, tea.Batch(s.reload(), s.loadChoices())
		case "a":
			return s, s.openForm("add", nil)
		case "e", "c", "d":
			if r, ok := s.table.SelectedRow(); ok {
				a := r.Data.(accountInfo)
				if k == "d" {
					if s.env.OpenModal != nil {
						return s, s.env.OpenModal(&tui.Confirm{Prompt: "Remove account " + a.Login + "?"})
					}
					return s, nil
				}
				kind := "edit"
				if k == "c" {
					kind = "clone"
				}
				return s, s.openForm(kind, &a)
			}
		case "enter":
			if r, ok := s.table.SelectedRow(); ok {
				login := r.Data.(accountInfo).Login
				return s, s.bg("account detail", func() tea.Msg { x, e := AccountShow(s.env.Home, login); return accountsDetailLoaded{x, e} })
			}
		}
	}
	return s, nil
}
func (s *accountsScreen) updateBackground(v tea.Msg) tea.Cmd {
	switch m := v.(type) {
	case accountsRateTick:
		_, c := s.Update(m)
		return c
	case accountsLoaded:
		if m.Generation == s.loadGeneration {
			_, c := s.Update(m)
			return c
		}
	case accountsPollResult:
		if m.Generation == s.pollGeneration {
			_, c := s.Update(m)
			return c
		}
	case accountsChoicesLoaded:
		_, c := s.Update(m)
		return c
	case accountsDetailLoaded:
		_, c := s.Update(m)
		return c
	case accountsMutationDone:
		_, c := s.Update(m)
		return c
	}
	return nil
}
func (s *accountsScreen) applyStatus(st angel.Status, e error, at time.Time) {
	s.live = map[string]angel.AccountActivity{}
	s.rates = map[string][2]string{}
	if e != nil {
		s.previous = map[string]angel.AccountActivity{}
		s.previousAt = at
		s.statusMessage = "Live status unavailable"
		return
	}
	s.statusMessage = ""
	elapsed := at.Sub(s.previousAt).Seconds()
	for _, a := range st.Activity {
		s.live[a.Login] = a
		up, down := 0.0, 0.0
		if p, ok := s.previous[a.Login]; ok && elapsed > 0 && a.UploadBytes >= p.UploadBytes && a.DownloadBytes >= p.DownloadBytes {
			up = float64(a.UploadBytes-p.UploadBytes) / elapsed
			down = float64(a.DownloadBytes-p.DownloadBytes) / elapsed
		}
		s.rates[a.Login] = [2]string{humanRate(up), humanRate(down)}
	}
	s.previous = accountsActivity(st.Activity)
	s.previousAt = at
}
func accountsActivity(as []angel.AccountActivity) map[string]angel.AccountActivity {
	m := map[string]angel.AccountActivity{}
	for _, a := range as {
		m[a.Login] = a
	}
	return m
}
func (s *accountsScreen) makeRows() []tui.Row {
	out := []tui.Row{}
	for _, a := range s.rows {
		conn, up, down := "", "", ""
		if x, ok := s.live[a.Login]; ok {
			conn = strconv.Itoa(x.Connections)
			r := s.rates[a.Login]
			up, down = r[0], r[1]
		}
		hop := func(x accountChainInfo) string { return accountsHopLabel(x.Source, x.Value) }
		pw := "waiting for first use"
		if a.PasswordSet {
			pw = "set"
		}
		out = append(out, tui.Row{Data: a, Cells: []string{a.Login, a.Provider, a.ExitCountry, a.Exit, allowedClientsLabel(a.AllowedClients), "pre:" + hop(a.PreSOCKS5) + " post:" + hop(a.PostSOCKS5), pw, conn, up, down}})
	}
	return out
}
func accountsHopLabel(source, value string) string {
	if value == "" || value == "none" {
		if source == "" {
			return "none"
		}
		return source + " none"
	}
	if source == "" {
		return "set"
	}
	return source + " set"
}
func accountsFallbackText(v *int) string {
	if v == nil {
		return "unknown"
	}
	return strconv.Itoa(*v)
}
func accountsTiersText(ts []accountShowTier) string {
	if len(ts) == 0 {
		return "  no route tiers"
	}
	lines := []string{}
	for _, t := range ts {
		st := "primary"
		if t.Fallback {
			st = "fallback"
		}
		if t.Skipped {
			st = "skipped"
		}
		nodes := []string{}
		for _, n := range t.Nodes {
			x := n.Name
			if x == "" {
				x = n.Label
			}
			nodes = append(nodes, x)
		}
		line := fmt.Sprintf("  %d %s country=%s providers=%s nodes=%s", t.Tier, st, t.Country, strings.Join(t.Providers, ","), strings.Join(nodes, ","))
		if t.Reason != "" {
			line += " reason=" + t.Reason
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
func (s *accountsScreen) View(w, h int) string {
	w = max(0, w)
	h = max(0, h)
	text := ""
	if s.detail && s.detailResult != nil {
		d := s.detailResult
		text = fmt.Sprintf("Account %s\nProvider: %s\nCountry: %s\nExit: %s\nPre-hop: %s\nPost-hop: %s\nProviders: %s\nCountries: %s\nTiers:\n%s\nLive: %t\nFallback connections: %s\n\nEsc returns", d.Login, d.Provider, d.ExitCountry, d.Exit, accountsHopLabel(d.PreSOCKS5.Source, d.PreSOCKS5.Value), accountsHopLabel(d.PostSOCKS5.Source, d.PostSOCKS5.Value), strings.Join(d.Providers, ", "), strings.Join(d.Countries, ", "), accountsTiersText(d.Tiers), d.Live, accountsFallbackText(d.FallbackConnections))
	} else {
		text = "Accounts"
		if s.loading {
			text += " (loading)"
		}
		text += "\n" + s.table.View(w, max(0, h-2))
		if s.statusMessage != "" {
			text += "\n" + s.statusMessage
		}
		if s.message != "" {
			text += "\n" + s.message
		}
	}
	lines := strings.Split(text, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		r := []rune(l)
		if len(r) > w {
			lines[i] = string(r[:w])
		}
	}
	return strings.Join(lines, "\n")
}
func (s *accountsScreen) countryFieldHint(providers []string) string {
	codes := []string{}
	seen := map[string]bool{}
	if len(providers) == 0 || len(providers) == 1 && providers[0] == "any" {
		providers = s.providers
	}
	for _, p := range providers {
		if p == "any" {
			for _, configured := range s.providers {
				for _, c := range s.providerCountries[configured] {
					if !seen[c] {
						seen[c] = true
						codes = append(codes, c)
					}
				}
			}
			continue
		}
		for _, c := range s.providerCountries[p] {
			if !seen[c] {
				seen[c] = true
				codes = append(codes, c)
			}
		}
	}
	if len(codes) == 0 {
		return "Country (country codes; unavailable)"
	}
	return "Country (available: " + strings.Join(codes, ",") + ")"
}
func (s *accountsScreen) openForm(kind string, src *accountInfo) tea.Cmd {
	if s.env.OpenModal == nil {
		return nil
	}
	opts := append([]string{"any"}, s.providers...)
	fields := []tui.Field{}
	switch kind {
	case "add":
		fields = []tui.Field{{Name: "Login", Kind: tui.TextField}, {Name: "Provider", Kind: tui.MultiChoiceField, Options: opts, Selected: []string{"any"}, Value: "any"}, {Name: s.countryFieldHint([]string{"any"}), Kind: tui.TextField}, {Name: "Exit", Kind: tui.SingleChoiceField, Options: []string{"route", "direct"}, Value: "route"}, {Name: "Allowed clients", Kind: tui.TextField}, {Name: "Pre-hop URL", Kind: tui.PasswordField}, {Name: "Post-hop URL", Kind: tui.PasswordField}, {Name: "Password note", Kind: tui.TextField, Value: "The first proxy password sets the account password; no password setter exists."}}
	case "edit":
		selected := []string{"any"}
		if src.Provider != "" {
			selected = strings.Split(src.Provider, ",")
		}
		fields = []tui.Field{{Name: "Provider", Kind: tui.MultiChoiceField, Options: opts, Selected: selected}, {Name: s.countryFieldHint(selected), Kind: tui.TextField, Value: src.ExitCountry}, {Name: "Exit", Kind: tui.SingleChoiceField, Options: []string{"route", "direct"}, Value: src.Exit}, {Name: "Allowed clients", Kind: tui.TextField, Value: src.AllowedClients}, {Name: "Pre-hop empty unchanged; none clears", Kind: tui.PasswordField}, {Name: "Post-hop empty unchanged; none clears", Kind: tui.PasswordField}}
	case "clone":
		fields = []tui.Field{{Name: "New login", Kind: tui.TextField}, {Name: "Provider override", Kind: tui.MultiChoiceField, Options: opts, Selected: []string{"any"}, Value: "any"}, {Name: s.countryFieldHint(nil), Kind: tui.TextField}, {Name: "Allowed clients override", Kind: tui.TextField}, {Name: "Exit override", Kind: tui.SingleChoiceField, Options: []string{"(unchanged)", "route", "direct"}, Value: "(unchanged)"}}
	}
	f := &tui.Form{TitleText: "Account " + kind, Fields: fields}
	f.Submit = func(v map[string]string) error {
		if kind == "add" && !config.ValidAccountLogin(v["Login"]) {
			return errors.New("invalid account login")
		}
		country := accountsFormValue(v, "Country")
		if country != "" {
			if _, e := parseCountries(country); e != nil {
				return errors.New("invalid country code")
			}
		}
		provider := accountsProviderValue(v, "Provider")
		if kind == "clone" {
			provider = accountsProviderValue(v, "Provider override")
		}
		if provider != "" {
			for _, p := range strings.Split(provider, ",") {
				found := false
				for _, configured := range s.providers {
					if p == configured {
						found = true
						break
					}
				}
				if !found {
					return errors.New("unknown provider")
				}
			}
		}
		return nil
	}
	s.form = &accountsFormState{Kind: kind, Form: f}
	if src != nil {
		s.form.Login, s.form.Original = src.Login, *src
	}
	return s.env.OpenModal(accountsFormModal{screen: s, form: f})
}
func accountsFormValue(v map[string]string, prefix string) string {
	for k, x := range v {
		if k == prefix || strings.HasPrefix(k, prefix+" ") || strings.HasPrefix(k, prefix+" (") {
			return x
		}
	}
	return ""
}
func accountsProviderValue(values map[string]string, field string) string {
	v := values[field]
	if v == "any" {
		return ""
	}
	parts := strings.Split(v, ",")
	out := []string{}
	for _, p := range parts {
		if p != "" && p != "any" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}
func (s *accountsScreen) mutate(v map[string]string) tea.Cmd {
	form := s.form
	kind, login := "", ""
	if form != nil {
		kind, login = form.Kind, form.Login
	}
	snapshot := append([]accountInfo(nil), s.rows...)
	if form != nil && kind == "edit" {
		snapshot = []accountInfo{form.Original}
	}
	s.loading = true
	if form != nil {
		for i := range form.Form.Fields {
			f := &form.Form.Fields[i]
			if f.Kind == tui.MultiChoiceField {
				f.Selected = strings.Split(v[f.Name], ",")
				if f.Name == "Provider" && len(f.Selected) == 0 {
					f.Selected = []string{"any"}
				}
			} else if f.Kind != tui.PasswordField {
				if val, ok := v[f.Name]; ok {
					f.Value = val
				}
			}
		}
	}
	return s.bg("save account", func() tea.Msg {
		var e error
		notice := "Account saved"
		switch kind {
		case "add":
			p := accountsProviderValue(v, "Provider")
			_, e = AccountAdd(s.env.Home, AccountAddOptions{Login: v["Login"], Provider: p, Country: accountsFormValue(v, "Country"), Clients: v["Allowed clients"], PreSOCKS5: v["Pre-hop URL"], PostSOCKS5: v["Post-hop URL"], Exit: v["Exit"], ExitGiven: true})
		case "edit":
			o := AccountUpdateOptions{Login: login}
			for k, d := range map[string]**string{"Country": &o.Country, "Allowed clients": &o.Clients, "Exit": &o.Exit} {
				x := v[k]
				if k == "Country" {
					x = accountsFormValue(v, "Country")
				}
				for _, a := range snapshot {
					old := map[string]string{"Country": a.ExitCountry, "Allowed clients": a.AllowedClients, "Exit": a.Exit}[k]
					if a.Login == login && x != old {
						*d = &x
					}
				}
			}
			provider := accountsProviderValue(v, "Provider")
			oldProvider := ""
			for _, a := range snapshot {
				if a.Login == login {
					oldProvider = a.Provider
				}
			}
			if provider != oldProvider {
				o.Provider = &provider
			}
			if x := v["Pre-hop empty unchanged; none clears"]; x != "" {
				o.PreSOCKS5 = &x
			}
			if x := v["Post-hop empty unchanged; none clears"]; x != "" {
				o.PostSOCKS5 = &x
			}
			_, e = AccountUpdate(s.env.Home, o)
		case "clone":
			p := accountsProviderValue(v, "Provider override")
			o := AccountCloneOptions{Source: login, Login: v["New login"], Provider: p, Country: accountsFormValue(v, "Country"), Clients: v["Allowed clients override"], ClientsGiven: v["Allowed clients override"] != ""}
			if x := v["Exit override"]; x != "(unchanged)" {
				o.Exit = x
				o.ExitGiven = true
			}
			_, e = AccountClone(s.env.Home, o)
			notice = "Account cloned"
		}
		rows := []accountInfo(nil)
		if e == nil {
			rows, e = AccountList(s.env.Home)
		}
		return accountsMutationDone{Rows: rows, Err: e, Notice: notice, Values: func() map[string]string {
			if e != nil && form != nil {
				return v
			}
			return nil
		}()}
	})
}
func accountsSafeFormError(err error, form *tui.Form) string {
	message := accountsErrorText(err)
	if form != nil {
		for _, field := range form.Fields {
			if field.Kind == tui.PasswordField && field.Value != "" {
				message = strings.ReplaceAll(message, field.Value, "[redacted]")
			}
		}
	}
	return message
}
func accountsErrorText(err error) string {
	var ce *cli.Error
	if errors.As(err, &ce) && ce.Message != "" {
		if ce.Hint != "" {
			return ce.Message + " — " + ce.Hint
		}
		return ce.Message
	}
	return "Account operation failed; check the account fields."
}
