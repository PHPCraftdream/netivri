package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/tui"
)

// Parity of the CLI and the interactive shell, see docs/design/parity.md.

var parityGlobalFlags = map[string]bool{
	"--home": true, "--local": true, "--json": true, "--quiet": true,
	"--no-color": true, "--help": true, "--interactive": true,
}

// parityCLIRefs lists every leaf command and every flag it documents.
func parityCLIRefs() map[string]bool {
	refs := map[string]bool{}
	for _, tp := range helpTopics {
		first := strings.Fields(tp.Name)[0]
		if _, ok := commands[first]; !ok || len(subTopics(tp.Name)) > 0 {
			continue
		}
		refs[tp.Name] = true
		for _, f := range tp.Flags {
			for _, part := range strings.Split(f.Name, ",") {
				fields := strings.Fields(part)
				if len(fields) == 0 || !strings.HasPrefix(fields[0], "--") || parityGlobalFlags[fields[0]] {
					continue
				}
				refs[tp.Name+" "+fields[0]] = true
			}
		}
	}
	return refs
}

func parityRun(c tea.Cmd) tea.Msg {
	if c == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case m := <-ch:
		return m
	case <-time.After(150 * time.Millisecond):
		return nil // a timer (tick) command: nothing to deliver
	}
}

func parityDrain(s tui.Screen, c tea.Cmd) tui.Screen {
	queue := []tea.Cmd{c}
	for i := 0; len(queue) > 0 && i < 200; i++ {
		x := queue[0]
		queue = queue[1:]
		m := parityRun(x)
		if m == nil {
			continue
		}
		if b, ok := m.(tea.BatchMsg); ok {
			queue = append(queue, b...)
			continue
		}
		var n tea.Cmd
		s, n = s.Update(m)
		queue = append(queue, n)
	}
	return s
}

func parityFixtureHome(t *testing.T) string {
	t.Helper()
	home := serversFixture(t)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Subscriptions = append(cfg.Subscriptions, config.Subscription{Name: "p", URL: "https://p.invalid/sub"})
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	return home
}

// parityTUIRefs builds the shell's screens on a small fixture (one item of
// each kind, so the item cards exist) and reads what they offer.
func parityTUIRefs(t *testing.T) map[string]bool {
	t.Helper()
	home := parityFixtureHome(t)
	env := tui.Env{Home: home, Background: func(_ string, w func() tea.Msg) tea.Cmd { return w }}
	refs := map[string]bool{}
	for _, tab := range interactiveOptions(nil, home, 0).Tabs {
		s := tab.New(env)
		s = parityDrain(s, s.Init())
		ps, ok := s.(parityScreen)
		if !ok {
			t.Fatalf("tab %q: %T does not implement ParityRefs", tab.Title, s)
		}
		got := ps.ParityRefs()
		screen := strings.ToLower(tab.Title)
		anchor := map[string]string{"providers": "providers/remove", "accounts": "accounts/remove", "servers": "servers/remove", "service": "service/start", "logs": "logs/list", "stats": "stats/list", "settings": "settings/listen.bind"}[screen]
		if !contains(got, anchor) {
			t.Fatalf("tab %q: the fixture produced no item card (refs %v)", tab.Title, got)
		}
		for _, r := range got {
			refs[r] = true
		}
	}
	for _, r := range parityFrameRefs {
		refs[r] = true
	}
	return refs
}

func paritySortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestParityCatalogIsWellFormed(t *testing.T) {
	ids := map[string]bool{}
	owner := map[string]string{}
	for _, c := range parityCatalog {
		if c.ID == "" || ids[c.ID] {
			t.Errorf("capability id %q is empty or duplicated", c.ID)
		}
		ids[c.ID] = true
		for _, ref := range append(append([]string(nil), c.CLI...), c.TUI...) {
			if prev, dup := owner[ref]; dup {
				t.Errorf("ref %q is claimed by %s and %s", ref, prev, c.ID)
			}
			owner[ref] = c.ID
		}
		exempt := map[string]exemption{}
		for _, e := range c.Exempt {
			if e.Side != "cli" && e.Side != "tui" {
				t.Errorf("%s: exemption side %q", c.ID, e.Side)
			}
			if strings.TrimSpace(e.Reason) == "" {
				t.Errorf("%s: exemption for %s has no reason", c.ID, e.Side)
			}
			if _, dup := exempt[e.Side]; dup {
				t.Errorf("%s: two exemptions for %s", c.ID, e.Side)
			}
			exempt[e.Side] = e
		}
		for side, refs := range map[string][]string{"cli": c.CLI, "tui": c.TUI} {
			_, isExempt := exempt[side]
			switch {
			case len(refs) == 0 && !isExempt:
				t.Errorf("%s: no %s refs and no exemption", c.ID, side)
			case len(refs) > 0 && isExempt:
				t.Errorf("%s: %s is exempted but has refs %v (stale exemption)", c.ID, side, refs)
			}
		}
	}
}

func TestParityEveryCLICommandAndFlagIsInTheCatalog(t *testing.T) {
	inCatalog := map[string]bool{}
	for _, c := range parityCatalog {
		for _, r := range c.CLI {
			inCatalog[r] = true
		}
	}
	real := parityCLIRefs()
	for _, ref := range paritySortedKeys(real) {
		if !inCatalog[ref] {
			t.Errorf("CLI %q is not in the parity catalog (add a capability, or a CLI ref to one)", ref)
		}
	}
	for _, ref := range paritySortedKeys(inCatalog) {
		if !real[ref] {
			t.Errorf("the catalog names CLI %q, which does not exist (renamed or removed?)", ref)
		}
	}
}

func TestParityEveryShellActionIsInTheCatalog(t *testing.T) {
	inCatalog := map[string]bool{}
	for _, c := range parityCatalog {
		for _, r := range c.TUI {
			inCatalog[r] = true
		}
	}
	real := parityTUIRefs(t)
	for _, ref := range paritySortedKeys(real) {
		if !inCatalog[ref] {
			t.Errorf("shell %q is not in the parity catalog (add a capability, or a TUI ref to one)", ref)
		}
	}
	for _, ref := range paritySortedKeys(inCatalog) {
		if !real[ref] {
			t.Errorf("the catalog names shell %q, which does not exist (renamed or removed?)", ref)
		}
	}
}

// The gaps are the backlog of the parity work. The budget may only go down.
func TestParityGapBudget(t *testing.T) {
	var gaps []string
	for _, c := range parityCatalog {
		for _, e := range c.Exempt {
			if e.Gap != "" {
				gaps = append(gaps, fmt.Sprintf("%s (%s side, task %s): %s", c.ID, e.Side, e.Gap, e.Reason))
			}
		}
	}
	for _, g := range gaps {
		t.Log("gap:", g)
	}
	switch {
	case len(gaps) > parityGapBudget:
		t.Errorf("%d gaps, budget %d: a new gap needs a task and an explicit decision", len(gaps), parityGapBudget)
	case len(gaps) < parityGapBudget:
		t.Errorf("%d gaps left, budget %d: lower parityGapBudget to %d", len(gaps), parityGapBudget, len(gaps))
	}
}
