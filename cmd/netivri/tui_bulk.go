package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// Bulk edit on the Accounts screen: the shell side of the comma lists and
// --all of account set-country, set-provider, set-clients, set-exit and
// set-socks5. The screen marks accounts (a "*" gutter on the list rows),
// picks one field and a value, confirms, and then calls the same shared
// operations the CLI commands call with the marked logins as the target
// list — one invalid account or value changes nothing, exactly as on the
// command line.

// bulkRequest is a confirmed field/value pair waiting for the user decision.
type bulkRequest struct {
	field, value string
}

// accountsBulkDone reports a finished bulk application.
type accountsBulkDone struct {
	Rows    []accountInfo
	Changed int
	Err     error
	Notice  string
}

// bulkFieldLabel names a bulk field the way the screen labels it.
func bulkFieldLabel(field string) string {
	switch field {
	case "provider":
		return "provider"
	case "country":
		return "countries"
	case "exit":
		return "exit"
	case "clients":
		return "allowed clients"
	case "pre":
		return "pre-hop"
	case "post":
		return "post-hop"
	}
	return field
}

// markedLogins lists the marked logins in list order, without repeats.
func (s *accountsScreen) markedLogins() []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range s.rows {
		if s.marked[a.Login] && !seen[a.Login] {
			seen[a.Login] = true
			out = append(out, a.Login)
		}
	}
	return out
}

// markAll marks every listed account (A).
func (s *accountsScreen) markAll() tea.Cmd {
	for _, a := range s.rows {
		s.marked[a.Login] = true
	}
	s.syncRows(s.selectedLogin())
	s.updateCard()
	return nil
}

// toggleMark marks or unmarks the selected account (Space or m).
func (s *accountsScreen) toggleMark() tea.Cmd {
	login := s.selectedLogin()
	if login == "" {
		return nil
	}
	if s.marked[login] {
		delete(s.marked, login)
	} else {
		s.marked[login] = true
	}
	s.syncRows(s.selectedLogin())
	s.updateCard()
	return nil
}

// clearMarks forgets every mark (M).
func (s *accountsScreen) clearMarks() tea.Cmd {
	if len(s.marked) == 0 {
		return nil
	}
	s.marked = map[string]bool{}
	s.syncRows(s.selectedLogin())
	s.updateCard()
	return nil
}

// pruneMarks drops the marks of accounts that disappeared in a reload.
func (s *accountsScreen) pruneMarks() {
	live := map[string]bool{}
	for _, a := range s.rows {
		live[a.Login] = true
	}
	for login := range s.marked {
		if !live[login] {
			delete(s.marked, login)
		}
	}
}

// accountBulkApply applies field=value to every marked login through the
// operations of the account set-* commands; every account is checked before
// anything is written, so a rejected account leaves the config untouched.
func accountBulkApply(home, field string, logins []string, value string) ([]accountInfo, int, error) {
	if len(logins) == 0 {
		return nil, 0, &cli.Error{Code: "ACCOUNT_NOT_FOUND", Message: "no accounts are marked", Exit: cli.ExitNotFound}
	}
	c, err := loadConfig(home)
	if err != nil {
		return nil, 0, err
	}
	spec := strings.Join(logins, ",")
	targets, err := accountTargets(c, spec, false)
	if err != nil {
		return nil, 0, err
	}
	save := func() error { return saveAccountConfig(home, c) }
	switch field {
	case "provider", "country", "clients":
		if value == "any" || value == "none" {
			value = ""
		}
		if field == "clients" && strings.EqualFold(strings.TrimSpace(value), "everyone") {
			value = "*"
		}
		out, err := updateAccountTargets(home, c, field, targets, spec, false, value, save)
		return out, len(out), err
	case "exit":
		out, err := mutateAccountExit(home, c, targets, value, save)
		return out, len(out), err
	case "pre", "post":
		pre, post := "", ""
		if field == "pre" {
			pre = value
		} else {
			post = value
		}
		out, err := mutateAccountSOCKS5(c, targets, field == "pre", field == "post", pre, post, save)
		return out, len(out), err
	}
	return nil, 0, cli.Usage("bulk: unknown field %q", field)
}

// beginBulk opens the field picker for the marked accounts.
func (s *accountsScreen) beginBulk() tea.Cmd {
	if len(s.markedLogins()) == 0 {
		return nil
	}
	p := prompt(tui.ChoicePrompt, "Apply to marked accounts", "Field", "Change one field on every marked account at once; one invalid account or value changes nothing.", "provider")
	p.Options = []tui.PromptOption{
		{Label: "Provider", Value: "provider", Description: "Priority order; any or none clears it"},
		{Label: "Countries", Value: "country", Description: "Priority order; any or none clears them"},
		{Label: "Exit", Value: "exit", Description: "Route or direct"},
		{Label: "Allowed clients", Value: "clients", Description: "everyone, loopback or CIDRs; none clears"},
		{Label: "Pre-hop", Value: "pre", Description: "URL, none or inherit"},
		{Label: "Post-hop", Value: "post", Description: "URL, none or inherit"},
	}
	s.dialog = &accountsDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd {
		return s.beginBulkValue(v["value"])
	}}
	return nil
}

// beginBulkValue asks for the value with the same prompt the single edit
// uses; an empty answer leaves pre-hops and post-hops unchanged.
func (s *accountsScreen) beginBulkValue(field string) tea.Cmd {
	f := map[string]struct {
		label, hint, value string
		kind               tui.PromptKind
	}{
		"provider": {"Provider", "Comma-separated providers in priority order; any or none clears them.", "", tui.TextPrompt},
		"country":  {"Countries", s.countryFieldHint(nil) + "; any or none clears them.", "", tui.TextPrompt},
		"exit":     {"Exit", "Route or direct.", "route", tui.ChoicePrompt},
		"clients":  {"Allowed clients", "Everyone, loopback or custom CIDR ranges; none clears them.", "", tui.TextPrompt},
		"pre":      {"Pre-hop", "Proxy URL; none cancels an inherited hop; inherit clears the account's own; an empty answer leaves hops unchanged.", "", tui.SecretPrompt},
		"post":     {"Post-hop", "Proxy URL; none cancels an inherited hop; inherit clears the account's own; an empty answer leaves hops unchanged.", "", tui.SecretPrompt},
	}[field]
	if f.label == "" {
		return nil
	}
	p := prompt(f.kind, "Bulk accounts · "+f.label, f.label, f.hint, f.value)
	if field == "exit" {
		p.Options = []tui.PromptOption{{Label: "Route", Value: "route"}, {Label: "Direct", Value: "direct"}}
	}
	s.dialog = &accountsDialog{prompt: p, onResult: func(v map[string]string) tea.Cmd {
		if (field == "pre" || field == "post") && v["value"] == "" {
			s.dialog = nil
			return nil
		}
		return s.confirmBulk(field, v["value"])
	}}
	return nil
}

// confirmBulk asks the user to name how many marked accounts will change.
func (s *accountsScreen) confirmBulk(field, value string) tea.Cmd {
	n := len(s.markedLogins())
	if n == 0 {
		s.dialog = nil
		return nil
	}
	s.dialog = nil
	if s.env.OpenModal == nil {
		return s.applyBulk(field, value)
	}
	accounts := "accounts"
	if n == 1 {
		accounts = "account"
	}
	s.pendingBulk = &bulkRequest{field: field, value: value}
	return s.env.OpenModal(&tui.Confirm{Prompt: fmt.Sprintf("Set %s on %d marked %s?", bulkFieldLabel(field), n, accounts), ConfirmLabel: "Apply"})
}

// bulkConfirmResult applies a decided bulk request; handled=false leaves the
// remove-confirmation path to the caller.
func (s *accountsScreen) bulkConfirmResult(m tui.ConfirmResultMsg) (tea.Cmd, bool) {
	if s.pendingBulk == nil {
		return nil, false
	}
	b := s.pendingBulk
	s.pendingBulk = nil
	if !m.Confirmed || m.Canceled {
		return nil, true
	}
	return s.applyBulk(b.field, b.value), true
}

// applyBulk runs the shared operations over the marked logins.
func (s *accountsScreen) applyBulk(field, value string) tea.Cmd {
	logins := s.markedLogins()
	return s.bg("bulk accounts", func() tea.Msg {
		_, n, e := accountBulkApply(s.env.Home, field, logins, value)
		if e != nil {
			return accountsBulkDone{Err: e}
		}
		rows, e := AccountList(s.env.Home)
		notice := fmt.Sprintf("%d accounts updated", n)
		if n == 1 {
			notice = "1 account updated"
		}
		return accountsBulkDone{Rows: rows, Changed: n, Err: e, Notice: notice}
	})
}

// applyBulkDone reports an error by message and hint only, or reloads the
// rows and says how many accounts changed.
func (s *accountsScreen) applyBulkDone(m accountsBulkDone) tea.Cmd {
	if m.Err != nil {
		s.message = accountsErrorText(m.Err)
		return s.notice(s.message)
	}
	s.rows = m.Rows
	s.pruneMarks()
	s.message = m.Notice
	s.syncRows(s.selectedLogin())
	s.updateCard()
	return tea.Batch(s.loadDetail(s.selectedLogin()), s.notice(s.message))
}
