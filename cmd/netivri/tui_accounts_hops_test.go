package main

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

// hopCardLine returns the card line with the given ID and its index.
func hopCardLine(s *accountsScreen, id string) (tui.CardLine, int) {
	for i, l := range s.workspace.Card.Lines {
		if l.ID == id {
			return l, i
		}
	}
	return tui.CardLine{}, -1
}

func hopScreen(t *testing.T, pre bool) (*accountsScreen, string) {
	t.Helper()
	h := accountProviderHome(t)
	opts := AccountAddOptions{Login: "alice", Provider: "p1", Country: "DE", Exit: "route", ExitGiven: true}
	if pre {
		opts.PreSOCKS5 = config.HopList{cloneHopURL}
	}
	if _, e := AccountAdd(h, opts); e != nil {
		t.Fatal(e)
	}
	s := newAccountsScreen(h, tui.Env{Background: accountsSyncBackground}).(*accountsScreen)
	_, _ = s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	rows, e := AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	s.rows = rows
	s.syncRows("alice")
	s.workspace.SetFocus(tui.PaneCard)
	s.refreshSelectedCard(true)
	return s, h
}

func TestAccountsHopPickerAddRemoveCancelAndSecretSafety(t *testing.T) {
	s, h := hopScreen(t, false)
	a, ok := s.selectedAccount()
	if !ok {
		t.Fatal("missing selected account")
	}
	if s.openEdit(a, "pre") != nil || s.dialog == nil || s.dialog.prompt == nil {
		t.Fatal("pre-hop picker did not open")
	}
	if s.dialog.prompt.Value != "" || strings.Contains(s.dialog.View(100, 20), "socks5://") {
		t.Fatal("picker prefilled or disclosed a raw URL")
	}
	// Canceling the operation picker changes no config.
	_, cancel := s.Update(accountsDialogResult{Canceled: true})
	if cancel != nil || s.dialog != nil {
		t.Fatal("cancel did not close the picker without an operation")
	}
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil || len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatalf("cancel changed config: err=%v", err)
	}

	// Empty secret input reports validation failure and does not update the
	// stored configuration.
	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	a, _ = s.selectedAccount()
	_ = s.openEdit(a, "pre")
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "add"}})
	_, emptyCmd := s.Update(accountsDialogResult{Values: map[string]string{"value": ""}})
	if emptyCmd == nil {
		t.Fatal("empty add did not produce its validation result")
	}
	runAccountCmd(s, emptyCmd)
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil || len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatalf("empty add changed configuration: err=%v", err)
	}
	if s.dialog == nil || s.dialog.prompt.Error == nil {
		t.Fatal("empty add did not keep the secret prompt with an error")
	}
	_, _ = s.Update(accountsDialogResult{Canceled: true})

	s.syncRows("alice")
	a, _ = s.selectedAccount()
	_ = s.openEdit(a, "pre")
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "add"}})
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.SecretPrompt || s.dialog.prompt.Value != "" {
		t.Fatal("add did not open a blank SecretPrompt")
	}
	secret := "socks5://u:hop-secret@pre.example:1080"
	s.dialog.prompt.Value = secret
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": secret}})
	if cmd == nil {
		t.Fatal("secret result did not start add")
	}
	runAccountCmd(s, cmd)
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil || !reflect.DeepEqual(cfg.Accounts[0].PreSOCKS5, config.HopList{secret}) {
		t.Fatalf("added hop did not persist: err=%v", err)
	}
	if strings.Contains(s.View(100, 20), "hop-secret") || strings.Contains(s.message, "hop-secret") {
		t.Fatal("screen exposed the newly saved secret")
	}

	s.rows, _ = AccountList(h)
	s.syncRows("alice")
	a, _ = s.selectedAccount()
	_ = s.openEdit(a, "pre")
	var removeValue string
	for _, option := range s.dialog.prompt.Options {
		if strings.HasPrefix(option.Value, "remove:") {
			removeValue = option.Value
			if strings.Contains(option.Label, "hop-secret") || !strings.Contains(option.Label, "pre.example:1080") || !strings.Contains(option.Label, "Remove entry 1") {
				t.Fatal("remove label is unsafe or incomplete")
			}
		}
	}
	if removeValue != "remove:1" {
		t.Fatalf("remove option = %q, want remove:1", removeValue)
	}
	_, cmd = s.Update(accountsDialogResult{Values: map[string]string{"value": removeValue}})
	if cmd == nil {
		t.Fatal("remove selection did not start mutation")
	}
	runAccountCmd(s, cmd)
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil || len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatalf("remove did not clear hop: err=%v", err)
	}
}

func TestAccountsHopPickerReplacementUsesAccountUpdate(t *testing.T) {
	// Replacing an existing override must replace the complete stored list
	// rather than writing the redacted display value back.
	s, h := hopScreen(t, true)
	a, _ := s.selectedAccount()
	_ = s.openEdit(a, "pre")
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "replace"}})
	replacement := "https://new-user:new-secret@replacement.example:443"
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": replacement}})
	if cmd == nil {
		t.Fatal("replacement did not start AccountUpdate")
	}
	runAccountCmd(s, cmd)
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil || !reflect.DeepEqual(cfg.Accounts[0].PreSOCKS5, config.HopList{replacement}) {
		t.Fatalf("replacement did not persist: err=%v", err)
	}
	if strings.Contains(s.View(100, 20), "new-secret") || strings.Contains(s.message, "new-secret") || s.dialog != nil {
		t.Fatal("replacement exposed entered URL")
	}
}

func TestAccountsHopPickerOperationSentinelsAndDirectPreSafety(t *testing.T) {
	s, h := hopScreen(t, false)
	a, _ := s.selectedAccount()
	_ = s.openEdit(a, "pre")
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "replace"}})
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Kind != tui.SecretPrompt || s.dialog.prompt.Value != "" {
		t.Fatal("replace did not open a blank secret prompt")
	}
	_, sentinelCmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "inherit"}})
	if sentinelCmd == nil {
		t.Fatal("typed inherit sentinel did not produce validation")
	}
	runAccountCmd(s, sentinelCmd)
	if s.dialog == nil || s.dialog.prompt.Error == nil {
		t.Fatal("typed inherit sentinel was accepted as a URL")
	}
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil || len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatalf("rejected sentinel changed config: err=%v", err)
	}
	_, _ = s.Update(accountsDialogResult{Canceled: true})
	_ = s.openEdit(a, "pre")
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": "none"}})
	if cmd == nil {
		t.Fatal("None selection did not start an account edit")
	}
	runAccountCmd(s, cmd)
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil || !reflect.DeepEqual(cfg.Accounts[0].PreSOCKS5, config.HopList{"none"}) {
		t.Fatalf("None selection did not persist a singleton sentinel: err=%v", err)
	}
	_ = s.openEdit(a, "pre")
	for _, option := range s.dialog.prompt.Options {
		if option.Value == "add" {
			t.Fatal("Add was offered for an explicitly disabled list")
		}
	}

	directHome := accountsHome(t)
	if _, err := AccountAdd(directHome, AccountAddOptions{Login: "direct", Exit: "direct", ExitGiven: true}); err != nil {
		t.Fatal(err)
	}
	direct := newAccountsScreen(directHome, tui.Env{}).(*accountsScreen)
	direct.rows, _ = AccountList(directHome)
	direct.syncRows("direct")
	directAccount, _ := direct.selectedAccount()
	if direct.openEdit(directAccount, "pre") != nil || direct.dialog != nil {
		t.Fatal("direct pre-hop editor did not reject the unsupported edit")
	}
	if !strings.Contains(direct.message, "Pre-hop is unavailable") {
		t.Fatalf("direct pre-hop rejection message was unexpected")
	}
}

func TestAccountsHopPickerInvalidAndProductionLimitKeepSecretDialog(t *testing.T) {
	s, h := hopScreen(t, true)
	a, _ := s.selectedAccount()
	_ = s.openEdit(a, "pre")
	_, _ = s.Update(accountsDialogResult{Values: map[string]string{"value": "add"}})
	secret := "http://u:second-secret@second.example:3128"
	s.dialog.prompt.Value = secret
	_, cmd := s.Update(accountsDialogResult{Values: map[string]string{"value": secret}})
	if cmd == nil {
		t.Fatal("add at production cap did not attempt update")
	}
	runAccountCmd(s, cmd)
	if s.dialog == nil || s.dialog.prompt == nil || s.dialog.prompt.Error == nil {
		t.Fatal("production multi-hop limit did not retain the secret prompt with error")
	}
	if strings.Contains(s.View(100, 20), "second-secret") || strings.Contains(s.message, "second-secret") || strings.Contains(s.dialog.prompt.Error.Error(), "second-secret") {
		t.Fatal("failed add leaked entered URL")
	}
	cfg, err := config.Load(config.ConfigPath(h))
	if err != nil || !reflect.DeepEqual(cfg.Accounts[0].PreSOCKS5, config.HopList{cloneHopURL}) {
		t.Fatal("failed add changed existing list")
	}

	for _, tc := range []struct {
		name, operation, value string
		position               int
	}{
		{"invalid position", "remove", "", 0},
		{"none rejected as URL", "add", "none", 0},
		{"inherit rejected as URL", "replace", "inherit", 0},
		{"empty URL", "replace", "", 0},
		{"invalid operation", "bad", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := accountHopEdit(tc.operation, tc.value, tc.position); err == nil {
				t.Fatal("invalid hop operation was accepted")
			}
		})
	}
	// Out-of-range indices reach AccountUpdate's original-list validation;
	// they cannot silently remove or rewrite an entry.
	before := s.dialog
	badRemove := s.mutateHopList("alice", "pre", "remove", "", 2)
	runAccountCmd(s, badRemove)
	if s.dialog != before || s.dialog.prompt.Error == nil {
		t.Fatal("out-of-range remove did not preserve the dialog with an error")
	}
	cfg, err = config.Load(config.ConfigPath(h))
	if err != nil || !reflect.DeepEqual(cfg.Accounts[0].PreSOCKS5, config.HopList{cloneHopURL}) {
		t.Fatal("out-of-range remove changed list")
	}
}

func TestAccountsCardHopListsShowSafeStateAndAlternativesCount(t *testing.T) {
	pre := []string{"socks5://a:secret-one@first.example:1080", "http://b:secret-two@second.example:3128"}
	s := newAccountsScreen("", tui.Env{}).(*accountsScreen)
	s.rows = []accountInfo{{Login: "alice", PreSOCKS5: accountChainInfo{Source: "account", Value: pre}}}
	s.syncRows("alice")
	line, _ := hopCardLine(s, "pre")
	for _, want := range []string{"account 2 alternatives: [1 set, 2 set]", "1 set", "2 set"} {
		if !strings.Contains(line.Value, want) {
			t.Fatalf("pre-hop card value missing expected safe label %q", want)
		}
	}
	if strings.Contains(line.Value, "first.example") || strings.Contains(line.Value, "second.example") || strings.Contains(line.Value, "secret-one") || strings.Contains(line.Value, "secret-two") {
		t.Fatal("card disclosed hop location or credentials")
	}
	if strings.Index(line.Value, "1 set") > strings.Index(line.Value, "2 set") {
		t.Fatal("card reordered hop positions")
	}
}
func TestAccountsInheritPreHopEndToEnd(t *testing.T) {
	s, h := hopScreen(t, true)
	line, i := hopCardLine(s, "clear-pre")
	if line.ID == "" {
		t.Fatal("card has no inherit pre-hop action")
	}
	if line.Disabled {
		t.Fatal("inherit pre-hop is disabled although the override is set")
	}
	for n := 0; n < i; n++ {
		_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on inherit pre-hop did not start the update")
	}
	runAccountCmd(s, cmd)
	if s.dialog != nil {
		t.Fatal("inherit pre-hop opened a dialog")
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := c.Account("alice")
	if len(alice.PreSOCKS5) != 0 {
		t.Fatal("pre-hop override was not removed")
	}
	s.refreshSelectedCard(true)
	line, _ = hopCardLine(s, "clear-pre")
	if !line.Disabled {
		t.Fatal("inherit pre-hop stayed enabled after inheriting")
	}
	if strings.Contains(s.View(120, 30), "fake-hop") {
		t.Fatal("inherited card disclosed the URL")
	}
}

// TestAccountsClearHopDisabledWithoutHop checks that with no hop set the clear
// action is grey and Enter on it does nothing.
func TestAccountsClearHopDisabledWithoutHop(t *testing.T) {
	s, h := hopScreen(t, false)
	prev := s.workspace.Card.Selected
	for _, id := range []string{"clear-pre", "clear-post"} {
		line, i := hopCardLine(s, id)
		if line.ID == "" || !line.Disabled {
			t.Fatalf("%s = %#v, want a disabled action", id, line)
		}
		// Card.Update never leaves the selection on a disabled line, so force
		// it there: Enter must report no activation and produce no command.
		s.workspace.Card.Selected = i
		if s.workspace.Card.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) {
			t.Fatalf("Enter on disabled %s reported activation", id)
		}
		_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil {
			t.Fatalf("Enter on disabled %s produced a command", id)
		}
		if s.dialog != nil {
			t.Fatalf("Enter on disabled %s opened a dialog", id)
		}
	}
	s.workspace.Card.Selected = prev
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	alice, _ := c.Account("alice")
	if len(alice.PreSOCKS5) != 0 || len(alice.PostSOCKS5) != 0 {
		t.Fatal("disabled clear action changed the config")
	}
}

// TestAccountsHopLinesShowStateOnly checks the card reports hop state without
// exposing addresses or credentials, and the password line's state.
func TestAccountsHopLinesShowStateOnly(t *testing.T) {
	s, h := hopScreen(t, true)
	line, _ := hopCardLine(s, "pre")
	if line.Value == "" || strings.Contains(line.Value, "fake-hop") || strings.Contains(line.Value, "hop.example") || strings.Contains(line.Value, "socks5://") {
		t.Fatal("pre-hop line disclosed hop details")
	}
	pw, _ := hopCardLine(s, "password")
	if pw.Value != "waiting for the first login" {
		t.Fatalf("password line = %q, want the waiting state", pw.Value)
	}
	// The state label must be VISIBLE in the rendered card: Secret is false on
	// hop lines, so the renderer must not mask the value with "********".
	for _, l := range []tui.CardLine{line} {
		if l.Secret {
			t.Fatalf("hop line is Secret; the label must render as text")
		}
	}
	v := ansi.Strip(s.View(120, 30))
	if !strings.Contains(v, "set") || strings.Contains(v, "hop.example") || strings.Contains(v, "fake-hop") || strings.Contains(v, "socks5://") || strings.Contains(v, "********") {
		t.Fatal("rendered card did not show safe hop state")
	}
	post, _ := hopCardLine(s, "post")
	if !strings.Contains(post.Value, "none") {
		t.Fatalf("post-hop line = %q, want none", post.Value)
	}
	c, e := config.Load(config.ConfigPath(h))
	if e != nil {
		t.Fatal(e)
	}
	hash, e := auth.Hash(cloneSourcePassword)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := c.Account("alice")
	a.Password = hash
	if e = config.Save(config.ConfigPath(h), c); e != nil {
		t.Fatal(e)
	}
	s.rows, e = AccountList(h)
	if e != nil {
		t.Fatal(e)
	}
	s.syncRows("alice")
	s.refreshSelectedCard(true)
	pw, _ = hopCardLine(s, "password")
	if pw.Value != "set" {
		t.Fatalf("password line after setting one = %q, want set", pw.Value)
	}
	s2, _ := hopScreen(t, false)
	line2, _ := hopCardLine(s2, "pre")
	if !strings.Contains(line2.Value, "none") {
		t.Fatal("unset pre-hop line did not show None")
	}
	pw2, _ := hopCardLine(s2, "password")
	if pw2.Value != "waiting for the first login" {
		t.Fatalf("password line = %q, want the waiting state", pw2.Value)
	}
	v2 := ansi.Strip(s2.View(120, 30))
	if !strings.Contains(v2, "none") {
		t.Fatal("rendered card does not show the hop none label")
	}
	v1 := s.View(120, 30)
	if strings.Contains(ansi.Strip(v1), "fake-hop") || strings.Contains(ansi.Strip(v1), "hop.example") || strings.Contains(ansi.Strip(v1), "socks5://") {
		t.Fatal("card view disclosed the hop URL")
	}
}
