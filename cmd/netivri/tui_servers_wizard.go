package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

const (
	serverParamPrefix = "param:"
	serverGenPrefix   = "gen:"
	serverAutoText    = "generated automatically"
)

type serversDialogResult struct {
	Values   map[string]string
	Canceled bool
}
type serversModalAction struct{ Index int }
type serversAddDone struct {
	result ServerAddResult
	err    error
}
type serversUpdateDone struct {
	name string
	view serverView
	err  error
}
type serversRotateDone struct {
	result ServerRotateResult
	err    error
}
type serversRemoveDone struct {
	result ServerRemoveResult
	err    error
}
type serversLinkDone struct {
	name   string
	link   string
	format string
	err    error
}

type serversDialog struct {
	prompt   *tui.Prompt
	wizard   *tui.Wizard
	modal    *serversMenuModal
	protocol string
	rebuild  func(protocol string, keep map[string]string) *tui.Wizard
	onResult func(map[string]string) tea.Cmd
}

func (d *serversDialog) SetTheme(t tui.Theme) {
	if d.prompt != nil {
		d.prompt.SetTheme(t)
	}
	if d.wizard != nil {
		d.wizard.SetTheme(t)
	}
	if d.modal != nil {
		d.modal.SetTheme(t)
	}
}
func (d *serversDialog) CapturingInput() bool { return true }
func (d *serversDialog) View(w, h int) string {
	if d.modal != nil {
		return d.modal.View(w, h)
	}
	if d.wizard != nil {
		return d.wizard.View(w, h)
	}
	return d.prompt.View(w, h)
}
func (d *serversDialog) Update(msg tea.Msg) (tui.Modal, tea.Cmd) {
	if d.modal != nil {
		m, c := d.modal.Update(msg)
		d.modal = m.(*serversMenuModal)
		return d, c
	}
	if d.wizard != nil {
		return d.updateWizard(msg)
	}
	m, c := d.prompt.Update(msg)
	d.prompt = m.(*tui.Prompt)
	if c != nil {
		return d, func() tea.Msg {
			v := c()
			if r, ok := v.(tui.PromptResultMsg); ok {
				return serversDialogResult{Values: map[string]string{"value": r.Value}, Canceled: r.Canceled}
			}
			return v
		}
	}
	return d, c
}
func (d *serversDialog) updateWizard(msg tea.Msg) (tui.Modal, tea.Cmd) {
	m, c := d.wizard.Update(msg)
	d.wizard = m.(*tui.Wizard)
	var out tea.Cmd
	if c != nil {
		out = func() tea.Msg {
			v := c()
			if r, ok := v.(tui.WizardResultMsg); ok {
				return serversDialogResult{Values: r.Values, Canceled: r.Canceled}
			}
			return v
		}
	}
	if proto, ok := d.wizard.Values["Protocol"]; ok && proto != d.protocol && d.rebuild != nil {
		d.protocol = proto
		nw := d.rebuild(proto, d.wizard.Values)
		nw.Values = serverCarryValues(d.wizard.Values, nw)
		nw.Index = 1
		nw.Buttons = tui.Buttons{Labels: []string{"Back", "Next"}}
		d.wizard = nw
	}
	for i := 0; i <= len(d.wizard.Steps); i++ {
		if d.wizard.Review || d.wizard.Saving || d.wizard.Index < 0 || d.wizard.Index >= len(d.wizard.Steps) {
			break
		}
		if !strings.HasPrefix(d.wizard.Steps[d.wizard.Index].Key, serverGenPrefix) {
			break
		}
		m2, _ := d.wizard.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		d.wizard = m2.(*tui.Wizard)
	}
	return d, out
}

func serverCarryValues(old map[string]string, nw *tui.Wizard) map[string]string {
	out := map[string]string{}
	for _, s := range nw.Steps {
		if v, ok := old[s.Key]; ok {
			out[s.Key] = v
		}
	}
	return out
}

type serversMenuModal struct {
	title      string
	lines      []string
	buttons    tui.Buttons
	onPick     func(int) tea.Cmd
	theme      tui.Theme
	width      int
	height     int
	scrollable bool
	offset     int
}

func newServersMenuModal(theme tui.Theme, title string, lines []string, labels []string, focus int, onPick func(int) tea.Cmd) *serversMenuModal {
	return &serversMenuModal{title: title, lines: lines, buttons: tui.Buttons{Labels: labels, Focus: focus}, onPick: onPick, theme: theme}
}
func newServersLinkModal(theme tui.Theme, name, link, format string) *serversMenuModal {
	lines := []string{"", "The link below contains credentials and is shown in plain text.", "", "Server: " + name}
	for _, l := range strings.Split(link, "\n") {
		lines = append(lines, l)
	}
	lines = append(lines, "", "Clear the link from your scrollback after copying.")
	title := "Client link · " + name
	if format == "mihomo" {
		title += " (mihomo yaml)"
	}
	m := newServersMenuModal(theme, title, lines, []string{"Close"}, 0, nil)
	m.scrollable = true
	return m
}
func (m *serversMenuModal) SetTheme(t tui.Theme) { m.theme = t }
func (m *serversMenuModal) CapturingInput() bool { return true }
func (m *serversMenuModal) close() tea.Cmd {
	return func() tea.Msg { return serversModalAction{Index: -1} }
}
func (m *serversMenuModal) pick(i int) tea.Cmd {
	return func() tea.Msg { return serversModalAction{Index: i} }
}
func (m *serversMenuModal) Update(msg tea.Msg) (tui.Modal, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return m, m.close()
		case "left", "shift+tab":
			m.buttons.Move(-1)
		case "right", "tab":
			m.buttons.Move(1)
		case "enter":
			return m, m.pick(m.buttons.Focus)
		case "up":
			m.scroll(-1)
		case "down":
			m.scroll(1)
		case "pgup":
			m.scroll(-5)
		case "pgdown":
			m.scroll(5)
		case "home":
			m.offset = 0
		case "end":
			m.offset = int(^uint(0) >> 1)
		}
		return m, nil
	}
	if wl, ok := msg.(tea.MouseWheelMsg); ok {
		if wl.Button == tea.MouseWheelUp {
			m.scroll(-1)
		} else if wl.Button == tea.MouseWheelDown {
			m.scroll(1)
		}
		return m, nil
	}
	if c, ok := msg.(tea.MouseClickMsg); ok && c.Button == tea.MouseLeft {
		if i, hit := m.buttons.Update(msg, m.height-1); hit && i >= 0 {
			return m, m.pick(i)
		}
	}
	return m, nil
}
func (m *serversMenuModal) scroll(d int) {
	slots := max(1, m.height-4)
	body := m.wrappedBody(m.width)
	m.offset = max(0, min(max(0, len(body)-slots), m.offset+d))
}
func (m *serversMenuModal) wrappedBody(w int) []string {
	var body []string
	for _, l := range m.lines {
		body = append(body, serversWrapLine(l, w)...)
	}
	return body
}
func (m *serversMenuModal) View(w, h int) string {
	m.width, m.height = w, h
	t := m.theme
	if t.Accent == nil {
		t = tui.NewTheme()
	}
	rows := make([]string, h)
	put := func(y int, s string) {
		if y >= 0 && y < h {
			rows[y] = s
		}
	}
	put(0, ansi.Truncate(m.title, w, ""))
	body := m.wrappedBody(w)
	indicator := h - 2
	if m.scrollable && len(body) > h-4 {
		slots := h - 4
		m.offset = max(0, min(m.offset, len(body)-slots))
		first, last := m.offset+1, min(m.offset+slots, len(body))
		put(indicator, fmt.Sprintf("lines %d-%d of %d · ↑/↓ scroll", first, last, len(body)))
		for i := 0; i < slots; i++ {
			put(2+i, body[m.offset+i])
		}
	} else {
		for i, l := range body {
			if 2+i >= h-2 {
				break
			}
			put(2+i, l)
		}
	}
	put(h-1, m.buttons.View(t))
	return serversBound(rows, w, h)
}
func serversWrapLine(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	var out []string
	cur := ""
	for _, word := range strings.Split(s, " ") {
		for lipgloss.Width(word) > w {
			chunk := ansi.Truncate(word, w, "")
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			out = append(out, chunk)
			word = strings.TrimPrefix(word, chunk)
		}
		switch {
		case cur == "":
			cur = word
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= w:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
	}
	return append(out, cur)
}
func serversBound(rows []string, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	if len(rows) > h {
		rows = rows[:h]
	}
	for i, line := range rows {
		if lipgloss.Width(line) > w {
			line = ansi.Truncate(line, w, "")
		}
		rows[i] = line + strings.Repeat(" ", max(0, w-lipgloss.Width(line)))
	}
	for len(rows) < h {
		rows = append(rows, strings.Repeat(" ", w))
	}
	return strings.Join(rows, "\n")
}

func serverProtocolByName(name string) (ServerProtocol, bool) {
	for _, p := range ServerProtocols() {
		if p.Name == name {
			return p, true
		}
	}
	return ServerProtocol{}, false
}
func serverParamAdvanced(p ServerParameter) bool {
	return strings.HasPrefix(p.Description, "Advanced option;")
}

func serverAutoGenSteps(proto ServerProtocol) []tui.WizardStep {
	steps := []tui.WizardStep{}
	for _, sp := range proto.Parameters {
		if !sp.AutoGenerated {
			continue
		}
		g := tui.NewPrompt(tui.TextPrompt, "Add server · "+sp.Label, sp.Label, "", serverAutoText)
		steps = append(steps, tui.WizardStep{Key: serverGenPrefix + sp.Name, Prompt: g})
	}
	return steps
}

func newServerWizard(s *serversScreen, protocol string, keep map[string]string) *tui.Wizard {
	shared := func(k, def string) string {
		if v, ok := keep[k]; ok && (v != "" || def == "") {
			return v
		}
		return def
	}
	protos := ServerProtocols()
	proto, hasProto := serverProtocolByName(protocol)
	opts := []tui.PromptOption{}
	for _, p := range protos {
		opts = append(opts, tui.PromptOption{Label: p.Label, Value: p.Name, Description: serversSafeDetailValue(p.Description) + " · " + p.Transport})
	}
	p := tui.NewPrompt(tui.ChoicePrompt, "Add server · Protocol", "Protocol", "Choose the server protocol.", protocol)
	p.Options = opts
	steps := []tui.WizardStep{{Key: "Protocol", Prompt: p}}
	name := tui.NewPrompt(tui.TextPrompt, "Add server · Name", "Name", "1-32 letters, digits, dots, underscores or hyphens; leave empty for an automatic name.", shared("Name", ""))
	name.Validate = func(v string) error {
		if v != "" && !serverNameValid(v) {
			return errors.New("name must be 1 to 32 letters, digits, dots, underscores or hyphens")
		}
		return nil
	}
	steps = append(steps, tui.WizardStep{Key: "Name", Prompt: name})
	bind := tui.NewPrompt(tui.TextPrompt, "Add server · Bind", "Bind address", "Address to listen on.", shared("Bind", "0.0.0.0"))
	steps = append(steps, tui.WizardStep{Key: "Bind", Prompt: bind})
	port := tui.NewPrompt(tui.TextPrompt, "Add server · Port", "Port", "Port to listen on; leave empty to let netivri pick one.", shared("Port", ""))
	portValidate := func(v string) error {
		if v == "" {
			return nil
		}
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
		return serverPortConflicts(s.home, protocol, bind.Value, n)
	}
	port.Validate = portValidate
	steps = append(steps, tui.WizardStep{Key: "Port", Prompt: port})
	if !hasProto || proto.PublicHost.Name != "" {
		host := tui.NewPrompt(tui.TextPrompt, "Add server · Public host", "Public host", "Host advertised in generated links; leave empty for none.", shared("PublicHost", ""))
		steps = append(steps, tui.WizardStep{Key: "PublicHost", Prompt: host})
	}
	if hasProto {
		steps = append(steps, serverParamSteps(proto, shared)...)
		steps = append(steps, serverAutoGenSteps(proto)...)
	}
	if hasProto && proto.SingleUser {
		acct := tui.NewPrompt(tui.ChoicePrompt, "Add server · Accounts", "Account", "Choose the account served by this server.", shared("Accounts", ""))
		acct.Options = []tui.PromptOption{}
		for _, l := range s.accountLogins {
			acct.Options = append(acct.Options, tui.PromptOption{Label: l, Value: l})
		}
		if acct.Value == "" && len(s.accountLogins) == 1 {
			acct.Value = s.accountLogins[0]
		}
		steps = append(steps, tui.WizardStep{Key: "Accounts", Prompt: acct})
	} else {
		acct := tui.NewPrompt(tui.MultiChoicePrompt, "Add server · Accounts", "Accounts", "Accounts served by this server; leave all unchecked to serve every account.", "")
		acct.Options = []tui.PromptOption{}
		for _, l := range s.accountLogins {
			acct.Options = append(acct.Options, tui.PromptOption{Label: l, Value: l})
		}
		if saved, ok := keep["Accounts"]; ok {
			acct.Selected = nil
			if saved != "" {
				acct.Selected = strings.Split(saved, ",")
			}
		} else {
			acct.Selected = append([]string(nil), s.accountLogins...)
		}
		steps = append(steps, tui.WizardStep{Key: "Accounts", Prompt: acct})
	}
	return tui.NewWizard("Add server", steps)
}

func serverParamSteps(proto ServerProtocol, shared func(string, string) string) []tui.WizardStep {
	steps := []tui.WizardStep{}
	for _, p := range proto.Parameters {
		if p.AutoGenerated || serverParamAdvanced(p) {
			continue
		}
		steps = append(steps, serverParamStep(p, shared))
	}
	return steps
}
func serverParamStep(p ServerParameter, shared func(string, string) string) tui.WizardStep {
	key := serverParamPrefix + p.Name
	title := "Add server · " + p.Label
	current := shared(key, p.Default)
	switch p.Kind {
	case "choice":
		prompt := tui.NewPrompt(tui.ChoicePrompt, title, p.Label, serversSafeDetailValue(p.Description), current)
		prompt.Options = []tui.PromptOption{}
		for _, c := range p.Choices {
			prompt.Options = append(prompt.Options, tui.PromptOption{Label: c, Value: c})
		}
		return tui.WizardStep{Key: key, Prompt: prompt}
	case "number":
		if current != "" {
			prompt := tui.NewPrompt(tui.NumberPrompt, title, p.Label, serversSafeDetailValue(p.Description), current)
			if p.Min > 0 {
				min := p.Min
				prompt.Min = &min
			}
			if p.Max > 0 {
				max := p.Max
				prompt.Max = &max
			}
			return tui.WizardStep{Key: key, Prompt: prompt}
		}
	}
	kind := tui.TextPrompt
	if p.Secret {
		kind = tui.SecretPrompt
		current = ""
	}
	prompt := tui.NewPrompt(kind, title, p.Label, serversSafeDetailValue(p.Description), current)
	if p.Required {
		prompt.Validate = func(v string) error {
			if strings.TrimSpace(v) == "" {
				return errors.New(p.Label + " is required")
			}
			return nil
		}
	}
	return tui.WizardStep{Key: key, Prompt: prompt}
}

func serverPortConflicts(home, protocol, bind string, port int) error {
	c, e := loadConfig(home)
	if e != nil {
		return nil
	}
	entry := config.Server{Protocol: protocol, Bind: strings.TrimSpace(bind), Port: port}
	if entry.Bind == "" {
		entry.Bind = "0.0.0.0"
	}
	return checkPortConflict(c, entry)
}
func serverPickPortAddress(bind string) string {
	host := strings.TrimSpace(bind)
	if host == "" || host == "0.0.0.0" {
		host = ""
	}
	return net.JoinHostPort(host, "0")
}
func serverPickPort(bind string) (int, error) {
	l, e := net.Listen("tcp", serverPickPortAddress(bind))
	if e != nil {
		return 0, &cli.Error{Code: "CONFIG_INVALID", Message: "could not find a free port", Hint: "set the port manually and try again", Exit: cli.ExitUsage}
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func serverAddOptionsFromWizard(v map[string]string) ServerAddOptions {
	port := 0
	if v["Port"] != "" {
		if n, e := strconv.Atoi(v["Port"]); e == nil {
			port = n
		}
	}
	opts := ServerAddOptions{
		Name:       strings.TrimSpace(v["Name"]),
		Protocol:   v["Protocol"],
		Bind:       strings.TrimSpace(v["Bind"]),
		Port:       port,
		Accounts:   v["Accounts"],
		PublicHost: strings.TrimSpace(v["PublicHost"]),
		Params:     map[string]string{},
	}
	if proto, ok := serverProtocolByName(v["Protocol"]); ok {
		for _, p := range proto.Parameters {
			if val, ok := v[serverParamPrefix+p.Name]; ok && val != "" {
				opts.Params[p.Name] = val
			}
		}
	}
	return opts
}

func serverAddResultLines(r ServerAddResult) []string {
	accts := r.Accounts
	if accts == "*" {
		accts = "all accounts"
	}
	return []string{
		"Name: " + r.Name,
		"Protocol: " + r.Protocol,
		"Bind: " + r.Bind,
		"Port: " + strconv.Itoa(r.Port),
		"Accounts: " + serversSafeDetailValue(accts),
		"",
		"Server secrets were generated and stored securely.",
	}
}

func (s *serversScreen) beginAddWizard() tea.Cmd {
	d := &serversDialog{wizard: newServerWizard(s, "", nil), protocol: "", rebuild: func(protocol string, keep map[string]string) *tui.Wizard {
		return newServerWizard(s, protocol, keep)
	}}
	d.onResult = func(v map[string]string) tea.Cmd {
		if v["Protocol"] == "" {
			v["Protocol"] = d.protocol
		}
		opts := serverAddOptionsFromWizard(v)
		return s.deliver("add server", func() tea.Msg {
			if opts.Port == 0 {
				p, e := serverPickPort(opts.Bind)
				if e != nil {
					return serversAddDone{err: e}
				}
				opts.Port = p
			}
			res, e := ServerAdd(s.home, opts)
			return serversAddDone{result: res, err: e}
		})
	}
	s.dialog = d
	return nil
}

func (s *serversScreen) activateCard() tea.Cmd {
	if s.workspace == nil || s.workspace.Card == nil || len(s.workspace.Card.Lines) == 0 {
		return nil
	}
	l := s.workspace.Card.Lines[s.workspace.Card.Selected]
	if l.Disabled || l.Kind == tui.CardReadOnly {
		return nil
	}
	return s.cardAction(l.ID)
}
func (s *serversScreen) cardAction(id string) tea.Cmd {
	switch id {
	case "add":
		return s.beginAddWizard()
	case "reload":
		if s.detail != nil {
			return s.detailCmd(s.detail.Name)
		}
		return s.detailCmdForSelected()
	case "link":
		if s.detail != nil {
			return s.beginShowLink(s.detail.Name)
		}
	case "rotate":
		return s.beginRotate(s.detail)
	case "remove":
		return s.beginRemove(s.detail)
	default:
		if s.detail != nil && strings.HasPrefix(id, serverParamPrefix) || s.detail != nil && id == "bind" || s.detail != nil && id == "port" || s.detail != nil && id == "publichost" || s.detail != nil && id == "accounts" {
			return s.openServerFieldEdit(s.detail.Name, id)
		}
	}
	return nil
}

func (s *serversScreen) openServerFieldEdit(name, id string) tea.Cmd {
	v := s.detail
	if v == nil || v.Name != name {
		return nil
	}
	title := "Server " + name
	var p *tui.Prompt
	switch {
	case id == "bind":
		p = tui.NewPrompt(tui.TextPrompt, title+" · Bind", "Bind address", "Address to listen on.", v.Bind)
	case id == "port":
		p = tui.NewPrompt(tui.NumberPrompt, title+" · Port", "Port", "Port to listen on.", strconv.Itoa(v.Port))
		min, max := 1, 65535
		p.Min, p.Max = &min, &max
	case id == "publichost":
		p = tui.NewPrompt(tui.TextPrompt, title+" · Public host", "Public host", "Host advertised in generated links; leave empty for none.", v.PublicHost)
	case id == "accounts":
		p = tui.NewPrompt(tui.TextPrompt, title+" · Accounts", "Accounts", "Comma-separated logins, or * for all accounts.", v.Accounts)
		p.Validate = func(x string) error {
			for _, a := range strings.Split(x, ",") {
				if a != "*" && !config.ValidAccountLogin(strings.TrimSpace(a)) {
					return errors.New("invalid account login")
				}
			}
			return nil
		}
	case strings.HasPrefix(id, serverParamPrefix):
		pname := strings.TrimPrefix(id, serverParamPrefix)
		proto, _ := serverProtocolByName(v.Protocol)
		for _, sp := range proto.Parameters {
			if sp.Name != pname {
				continue
			}
			p = serverFieldPrompt(title, sp, s.detailParams[pname])
			break
		}
	}
	if p == nil {
		return nil
	}
	secretParam := strings.HasPrefix(id, serverParamPrefix) && p.Kind == tui.SecretPrompt
	s.dialog = &serversDialog{prompt: p, onResult: func(vv map[string]string) tea.Cmd {
		x := vv["value"]
		if secretParam && x == "" {
			s.dialog = nil
			return nil
		}
		o := ServerUpdateOptions{}
		switch {
		case id == "bind":
			o.Bind = &x
		case id == "port":
			n, e := strconv.Atoi(x)
			if e != nil {
				s.dialog = nil
				return nil
			}
			o.Port = &n
		case id == "publichost":
			o.PublicHost = &x
		case id == "accounts":
			o.Accounts = &x
		default:
			o.Params = map[string]string{strings.TrimPrefix(id, serverParamPrefix): x}
		}
		return s.deliver("update server", func() tea.Msg {
			view, e := ServerUpdate(s.home, name, o)
			return serversUpdateDone{name: name, view: view, err: e}
		})
	}}
	return nil
}

func serverFieldPrompt(title string, sp ServerParameter, current string) *tui.Prompt {
	label := serversSafeDetailValue(sp.Label)
	if sp.Secret {
		p := tui.NewPrompt(tui.SecretPrompt, title+" · "+label, label, "Value is stored securely. Leave empty to keep the current value.", "")
		return p
	}
	switch sp.Kind {
	case "choice":
		p := tui.NewPrompt(tui.ChoicePrompt, title+" · "+label, label, serversSafeDetailValue(sp.Description), current)
		p.Options = []tui.PromptOption{}
		for _, c := range sp.Choices {
			p.Options = append(p.Options, tui.PromptOption{Label: c, Value: c})
		}
		return p
	case "number":
		val := current
		if val == "" {
			val = sp.Default
		}
		if val != "" {
			p := tui.NewPrompt(tui.NumberPrompt, title+" · "+label, label, serversSafeDetailValue(sp.Description), val)
			if sp.Min > 0 {
				min := sp.Min
				p.Min = &min
			}
			if sp.Max > 0 {
				max := sp.Max
				p.Max = &max
			}
			return p
		}
		p := tui.NewPrompt(tui.TextPrompt, title+" · "+label, label, serversSafeDetailValue(sp.Description)+" Leave empty for the protocol default.", "")
		min, mx := sp.Min, sp.Max
		p.Validate = func(v string) error {
			if v == "" {
				return nil
			}
			n, e := strconv.Atoi(v)
			if e != nil {
				return errors.New("enter a whole number")
			}
			if min > 0 && n < min {
				return errors.New("must be at least " + strconv.Itoa(min))
			}
			if mx > 0 && n > mx {
				return errors.New("must be at most " + strconv.Itoa(mx))
			}
			return nil
		}
		return p
	}
	return tui.NewPrompt(tui.TextPrompt, title+" · "+label, label, serversSafeDetailValue(sp.Description), current)
}

func (s *serversScreen) beginShowLink(name string) tea.Cmd {
	if s.detail == nil || s.detail.Name != name {
		s.pendingLink = name
		return s.detailCmd(name)
	}
	return s.showLinkChoice(name)
}
func (s *serversScreen) showLinkChoice(name string) tea.Cmd {
	if s.detail == nil || s.detail.Name != name {
		return nil
	}
	if len(s.detailAccounts) == 0 {
		return s.notice("No accounts served; add an account first.")
	}
	if len(s.detailAccounts) == 1 {
		return s.confirmServerLink(name, s.detailAccounts[0])
	}
	p := tui.NewPrompt(tui.ChoicePrompt, "Show client link · "+name, "Account", "Choose the account the link is for.", "")
	p.Options = []tui.PromptOption{}
	for _, a := range s.detailAccounts {
		p.Options = append(p.Options, tui.PromptOption{Label: a, Value: a})
	}
	s.dialog = &serversDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd {
		return s.confirmServerLink(name, v["value"])
	}}
	return nil
}
func (s *serversScreen) confirmServerLink(name, account string) tea.Cmd {
	p := tui.NewPrompt(tui.ConfirmPrompt, "Show client link", "The client link contains credentials and will be shown in plain text. Anyone who sees it can connect to this server.", "", "")
	p.ConfirmLabel = "Show"
	p.Buttons = tui.Buttons{Labels: []string{"Cancel", "Show"}, Focus: 0}
	s.dialog = &serversDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd {
		return s.deliver("server link", func() tea.Msg {
			format := "uri"
			link, e := ServerLink(s.home, name, ServerLinkOptions{Account: account, Format: format})
			if e != nil {
				if l, e2 := ServerLink(s.home, name, ServerLinkOptions{Account: account, Format: "mihomo"}); e2 == nil {
					link, format, e = l, "mihomo", nil
				}
			}
			return serversLinkDone{name: name, link: link, format: format, err: e}
		})
	}}
	return nil
}

func (s *serversScreen) beginRotate(v *serverView) tea.Cmd {
	if v == nil || s.env.OpenModal == nil {
		return nil
	}
	s.pendingConfirm = "rotate"
	s.pendingServer = v.Name
	return s.env.OpenModal(&tui.Confirm{Prompt: serverRotatePrompt(v, s.detailAccounts)})
}
func (s *serversScreen) beginRemove(v *serverView) tea.Cmd {
	if v == nil || s.env.OpenModal == nil {
		return nil
	}
	s.pendingConfirm = "remove"
	s.pendingServer = v.Name
	return s.env.OpenModal(&tui.Confirm{Prompt: serverRemovePrompt(v, s.detailAccounts)})
}
func serverAccountList(v *serverView, resolved []string) string {
	if v.Accounts == "*" {
		if len(resolved) > 0 {
			return strings.Join(resolved, ", ")
		}
		return "all accounts"
	}
	return v.Accounts
}
func serverRotatePrompt(v *serverView, resolved []string) string {
	accounts := serverAccountList(v, resolved)
	if v.SingleUser {
		return "Rotate keys for server " + v.Name + "? The shared credentials for " + serversSafeDetailValue(accounts) + " change and existing client links stop working."
	}
	return "Rotate keys for server " + v.Name + "? Credentials for " + serversSafeDetailValue(accounts) + " change and existing client links stop working."
}
func serverRemovePrompt(v *serverView, resolved []string) string {
	accounts := serverAccountList(v, resolved)
	return "Remove server " + v.Name + "? Client links for " + serversSafeDetailValue(accounts) + " stop working."
}

func (s *serversScreen) notice(text string) tea.Cmd {
	if s.env.Toast == nil || text == "" {
		return nil
	}
	return s.env.Toast(text)
}
func serversSafePromptError(err error, values ...string) string {
	var ce *cli.Error
	text := "Server operation failed; check the entered values."
	if errors.As(err, &ce) && ce.Message != "" {
		text = ce.Message
		if ce.Hint != "" {
			text += " — " + ce.Hint
		}
	}
	for _, v := range values {
		if v != "" {
			text = strings.ReplaceAll(text, v, "[redacted]")
		}
	}
	return serversSafeDetailValue(text)
}
