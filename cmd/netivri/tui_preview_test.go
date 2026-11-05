package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/PHPCraftdream/netivri/internal/tui"
	"github.com/charmbracelet/x/ansi"
)

const previewBodyFixture = `proxies:
  - {name: "[DE] One", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "[DE] Two", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "[NL] Three", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
  - {name: "no proxy 5", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}
`

func TestProviderPreviewStepShowsCountriesAndUnknown(t *testing.T) {
	h := providerSeed(t)
	var via string
	get := func(_ context.Context, _ string, o fetch.Options) (*fetch.Result, error) {
		via = o.Proxy
		return &fetch.Result{Body: []byte(previewBodyFixture)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["url"] = "https://user:preview-secret@sub.invalid/link?token=preview-secret"
	s.wizard.Values["via"] = "http://viauser:viasecret@proxy.invalid:8080"
	s.wizard.Index = s.previewIndex()
	c := s.syncPreview()
	if c == nil || s.previewState != "loading" || s.wizard.Busy != true {
		t.Fatalf("preview fetch did not start: c=%v state=%q busy=%v", c, s.previewState, s.wizard.Busy)
	}
	if v := ansi.Strip(s.View(80, 24)); !strings.Contains(v, "Fetching subscription") {
		t.Fatalf("busy state not visible: %q", v)
	}
	m := c()
	pm, ok := m.(providerResultMsg)
	if !ok || pm.kind != "preview" || pm.err != nil {
		t.Fatalf("preview result: %#v", m)
	}
	_, _ = s.Update(pm)
	if s.previewState != "done" || s.wizard.Busy {
		t.Fatalf("preview result not applied: state=%q busy=%v", s.previewState, s.wizard.Busy)
	}
	if via != "http://viauser:viasecret@proxy.invalid:8080" {
		t.Fatalf("the fetch did not use the via proxy: %q", via)
	}
	v := ansi.Strip(s.View(100, 30))
	for _, want := range []string{"4 nodes", "1 with no country", "DE — 2 nodes", "NL — 1 node"} {
		if !strings.Contains(v, want) {
			t.Fatalf("preview view missing %q: %s", want, v)
		}
	}
	for _, secret := range []string{"preview-secret", "viasecret", "viauser", "sub.invalid", "proxy.invalid", "token"} {
		if strings.Contains(v, secret) {
			t.Fatalf("preview view leaked %q: %s", secret, v)
		}
	}
}

func TestProviderPreviewEscCancelsTheFetch(t *testing.T) {
	h := providerSeed(t)
	var got context.Context
	get := func(ctx context.Context, _ string, _ fetch.Options) (*fetch.Result, error) {
		got = ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["url"] = "https://sub.invalid/link"
	s.wizard.Index = s.previewIndex()
	c := s.syncPreview()
	if c == nil {
		t.Fatal("preview fetch did not start")
	}
	// Esc on the busy wizard cancels the fetch, not the wizard.
	_, esc := s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if esc == nil {
		t.Fatal("esc produced no command")
	}
	wm, ok := esc().(tui.WizardResultMsg)
	if !ok || !wm.Canceled {
		t.Fatalf("esc did not ask to cancel: %#v", esc())
	}
	_, _ = s.Update(wm)
	if s.wizard == nil || !s.wizard.Busy {
		t.Fatal("the cancel request did not keep the wizard")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case m := <-done:
		pm, ok := m.(providerResultMsg)
		if !ok || pm.kind != "preview" || pm.err == nil {
			t.Fatalf("the canceled fetch did not report a failure: %#v", m)
		}
		if got == nil || got.Err() != context.Canceled {
			t.Fatalf("the cancel did not reach the fetch context: %v", got)
		}
		_, _ = s.Update(pm)
		if s.previewState != "canceled" || s.wizard.Busy {
			t.Fatalf("canceled preview state: %q busy=%v", s.previewState, s.wizard.Busy)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the fetch did not observe the cancel")
	}
	if v := ansi.Strip(s.View(80, 24)); !strings.Contains(v, "Preview canceled") {
		t.Fatalf("canceled state not shown: %q", v)
	}
	// Next continues the add after a canceled preview.
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.wizard == nil || !s.wizard.Review {
		t.Fatal("enter after a canceled preview did not reach the review")
	}
}

func TestProviderPreviewFailureShowsMessageAndHintOnly(t *testing.T) {
	h := providerSeed(t)
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "fetch of https://user:preview-secret@sub.invalid/link?token=preview-secret failed", Hint: "check https://user:preview-secret@sub.invalid/link or the proxy http://viauser:viasecret@proxy.invalid:8080"}
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["url"] = "https://user:preview-secret@sub.invalid/link?token=preview-secret"
	s.wizard.Values["via"] = "http://viauser:viasecret@proxy.invalid:8080"
	s.wizard.Index = s.previewIndex()
	drainProvider(s, s.syncPreview())
	if s.previewState != "failed" {
		t.Fatalf("failed preview state: %q", s.previewState)
	}
	v := ansi.Strip(s.View(100, 24))
	if !strings.Contains(v, "Error:") || !strings.Contains(v, "[redacted]") {
		t.Fatalf("the failure is not shown by message and hint: %q", v)
	}
	for _, secret := range []string{"preview-secret", "viasecret", "viauser", "sub.invalid", "proxy.invalid", "token=", "https://"} {
		if strings.Contains(v, secret) {
			t.Fatalf("the failure view leaked %q: %s", secret, v)
		}
	}
	// A failed preview does not block adding: Next reaches the review.
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.wizard == nil || !s.wizard.Review {
		t.Fatal("enter after a failed preview did not reach the review")
	}
}

func TestProviderPreviewBackAndRefetch(t *testing.T) {
	h := providerSeed(t)
	calls := 0
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		calls++
		return &fetch.Result{Body: []byte(previewBodyFixture)}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["name"] = "p"
	s.wizard.Values["url"] = "https://sub.invalid/one"
	s.wizard.Values["via"] = ""
	s.wizard.Index = s.previewIndex()
	drainProvider(s, s.syncPreview())
	if calls != 1 || s.previewState != "done" {
		t.Fatalf("first preview: calls=%d state=%q", calls, s.previewState)
	}
	if c := s.syncPreview(); c != nil {
		t.Fatal("the same values were fetched again")
	}
	// Tab then Enter picks the focused Back button: from the preview to the
	// Via step, with the entered values kept.
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.wizard.Index != s.previewIndex()-1 || s.wizard.Review {
		t.Fatalf("back did not land on the via step: index=%d review=%v", s.wizard.Index, s.wizard.Review)
	}
	if s.wizard.Values["url"] != "https://sub.invalid/one" {
		t.Fatalf("back lost the entered values: %v", s.wizard.Values)
	}
	// A changed URL is fetched again when the step is entered.
	s.wizard.Values["url"] = "https://sub.invalid/two"
	s.wizard.Index = s.previewIndex()
	drainProvider(s, s.syncPreview())
	if calls != 2 || s.previewState != "done" {
		t.Fatalf("second preview: calls=%d state=%q", calls, s.previewState)
	}
}

func TestProviderPreviewWizardFrameContract(t *testing.T) {
	h := providerSeed(t)
	codes := []string{"AD", "AE", "AF", "AG", "AI", "AL", "AM", "AO", "AQ", "AR", "AS", "AT", "AU"}
	lines := []string{"proxies:"}
	for i, code := range codes {
		lines = append(lines, fmt.Sprintf("  - {name: \"[%s] node %d\", type: ss, server: example.invalid, port: 8388, cipher: aes-128-gcm, password: pass}", code, i))
	}
	get := func(context.Context, string, fetch.Options) (*fetch.Result, error) {
		return &fetch.Result{Body: []byte(strings.Join(lines, "\n"))}, nil
	}
	s := newProvidersScreenWith(h, providerEnv(h), get, func() time.Time { return testNow })
	s.startWizard()
	s.wizard.Values["url"] = "https://sub.invalid/many"
	s.wizard.Index = s.previewIndex()
	drainProvider(s, s.syncPreview())
	if s.previewState != "done" {
		t.Fatalf("preview state %q", s.previewState)
	}
	for _, z := range []struct{ w, hgt int }{{60, 16}, {80, 24}, {100, 30}, {120, 40}} {
		v := ansi.Strip(s.View(z.w, z.hgt))
		if strings.Count(v, "\n")+1 != z.hgt {
			t.Fatalf("the view has %d lines, want %d", strings.Count(v, "\n")+1, z.hgt)
		}
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > z.w {
				t.Fatalf("a line is wider than %d: %q", z.w, line)
			}
		}
	}
	// Thirteen countries do not fit at 60x16: the step names the rest.
	v := ansi.Strip(s.View(60, 16))
	if !strings.Contains(v, "more countries") || !strings.Contains(v, "13 nodes") {
		t.Fatalf("truncated preview: %q", v)
	}
}

func TestProvidersCoverPreviewBodyStates(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	if lines := s.previewBody(60, 16); len(lines) != 1 || !strings.Contains(lines[0], "not been fetched yet") {
		t.Fatalf("empty preview body: %v", lines)
	}
	s.previewState = "loading"
	if lines := s.previewBody(60, 16); len(lines) != 1 || !strings.Contains(lines[0], "Esc cancels") {
		t.Fatalf("loading preview body: %v", lines)
	}
	s.previewState = "failed"
	if lines := s.previewBody(60, 16); len(lines) != 1 || !strings.Contains(lines[0], "adds the provider anyway") {
		t.Fatalf("failed preview body: %v", lines)
	}
	s.previewState = "canceled"
	if lines := s.previewBody(60, 16); len(lines) != 2 || !strings.Contains(lines[1], "Back returns to the Via step") {
		t.Fatalf("canceled preview body: %v", lines)
	}
	s.previewState = "done"
	s.preview = previewData{nodes: 1, counts: []subscription.CountryCount{{Code: "DE", Nodes: 1}, {Code: "NL", Nodes: 2}}, unknown: 1}
	lines := s.previewBody(60, 16)
	if len(lines) != 3 || !strings.Contains(lines[0], "1 node; 1 with no country") || !strings.Contains(lines[1], "DE — 1 node") || !strings.Contains(lines[2], "NL — 2 nodes") {
		t.Fatalf("done preview body: %v", lines)
	}
	s.preview = previewData{nodes: 20, counts: make([]subscription.CountryCount, 20), unknown: 0}
	for i := range s.preview.counts {
		s.preview.counts[i] = subscription.CountryCount{Code: "AA", Nodes: 1}
	}
	if lines := s.previewBody(60, 16); len(lines) != 11 || !strings.Contains(lines[10], "more countries") {
		t.Fatalf("truncated preview body: %v", lines)
	}
}

func TestProviderParityRefsIncludePreview(t *testing.T) {
	s := newProvidersScreen(t.TempDir(), tui.Env{}).(*providersScreen)
	found := 0
	for _, r := range s.ParityRefs() {
		if r == "providers/preview" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("providers/preview appears %d times in the parity refs", found)
	}
}
