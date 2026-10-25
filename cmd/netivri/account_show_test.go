package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestBuildShowTiersNodesCarryName(t *testing.T) {
	withName := func(id, country, name string) subscription.Node {
		n := node(id, country)
		n.Name = name
		return n
	}
	known := map[string]bool{"main": true}
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(withName("m-de", "DE", "  Germany  1 "), withName("plain", "DE", "")),
	}
	tiers := buildShowTiers(known, snapshots, [][]string{{"main"}}, []string{"DE"})
	if len(tiers) != 1 || len(tiers[0].Nodes) != 2 {
		t.Fatalf("tiers = %+v", tiers)
	}
	if tiers[0].Nodes[0].Name != "Germany 1" || tiers[0].Nodes[1].Name != "" {
		t.Fatalf("names = %q, %q", tiers[0].Nodes[0].Name, tiers[0].Nodes[1].Name)
	}
}

func TestBuildShowTiersTiersInOrder(t *testing.T) {
	known := map[string]bool{"main": true, "backup": true}
	snapshots := map[string]*subscription.Snapshot{
		"main":   snap(node("m-de", "DE"), node("m-nl", "NL")),
		"backup": snap(node("b-de", "DE")),
	}
	tiers := buildShowTiers(known, snapshots, [][]string{{"main"}, {"backup"}}, []string{"DE", "NL"})
	if len(tiers) != 4 {
		t.Fatalf("tiers = %+v", tiers)
	}
	want := []struct {
		tier  int
		cc    string
		label string
		skip  bool
	}{
		{0, "DE", "main/m-de", false},
		{1, "DE", "backup/b-de", false},
		{2, "NL", "main/m-nl", false},
		{3, "NL", "", true},
	}
	for i, w := range want {
		got := tiers[i]
		if got.Tier != w.tier || got.Country != w.cc || got.Skipped != w.skip {
			t.Errorf("tier %d = %+v, want %+v", i, got, w)
		}
		if w.label != "" {
			if len(got.Nodes) != 1 || got.Nodes[0].Label != w.label || got.Nodes[0].Tier != w.tier {
				t.Errorf("tier %d nodes = %+v, want %s", i, got.Nodes, w.label)
			}
		} else if len(got.Nodes) != 0 || got.Reason == "" {
			t.Errorf("tier %d skipped = %+v", i, got)
		}
	}
}

func TestBuildShowTiersAnyCountry(t *testing.T) {
	known := map[string]bool{"main": true}
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE"), node("m-fr", "FR")),
	}
	tiers := buildShowTiers(known, snapshots, [][]string{{"main"}}, []string{"DE", config.AnyCountry})
	if len(tiers) != 2 {
		t.Fatalf("tiers = %+v", tiers)
	}
	if tiers[0].Country != "DE" || len(tiers[0].Nodes) != 1 || tiers[0].Nodes[0].Label != "main/m-de" {
		t.Errorf("tier 0 = %+v", tiers[0])
	}
	if tiers[1].Country != config.AnyCountry || len(tiers[1].Nodes) != 1 || tiers[1].Nodes[0].Label != "main/m-fr" {
		t.Errorf("tier 1 = %+v", tiers[1])
	}
}

func TestBuildShowTiersSkippedDoesNotConsumeIndex(t *testing.T) {
	known := map[string]bool{"main": true}
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE"), node("m-nl", "NL")),
	}
	groups := [][]string{{"main"}, {"ghost"}}
	tiers := buildShowTiers(known, snapshots, groups, []string{"DE", "NL"})
	if len(tiers) != 4 {
		t.Fatalf("tiers = %+v", tiers)
	}
	if tiers[0].Tier != 0 || tiers[0].Country != "DE" || len(tiers[0].Nodes) != 1 {
		t.Errorf("tier 0 = %+v", tiers[0])
	}
	if !tiers[1].Skipped || tiers[1].Tier != 1 || tiers[1].Providers[0] != "ghost" {
		t.Errorf("tier 1 = %+v", tiers[1])
	}
	if got := "provider ghost is not a configured subscription"; tiers[1].Reason != got {
		t.Errorf("reason = %q, want %q", tiers[1].Reason, got)
	}
	if tiers[2].Tier != 1 || tiers[2].Country != "NL" || len(tiers[2].Nodes) != 1 || tiers[2].Nodes[0].Label != "main/m-nl" {
		t.Errorf("tier 2 = %+v", tiers[2])
	}
	if !tiers[3].Skipped || tiers[3].Tier != 2 {
		t.Errorf("tier 3 = %+v", tiers[3])
	}
}

func TestAccountShowErrors(t *testing.T) {
	home := t.TempDir()
	if err := config.Save(config.ConfigPath(home), config.Default()); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (*cli.Error, *bytes.Buffer) {
		var out bytes.Buffer
		err := runAccount(append(args, "--home", home), &out)
		var ce *cli.Error
		if !errors.As(err, &ce) {
			t.Fatalf("account %v: expected *cli.Error, got %v (%s)", args, err, out.String())
		}
		return ce, &out
	}
	if ce, _ := run("show", "ghost"); ce.Code != "ACCOUNT_NOT_FOUND" || ce.Exit != cli.ExitNotFound {
		t.Errorf("ghost = %+v", ce)
	}
	if ce, _ := run("show", "bad!login"); ce.Code != "ACCOUNT_INVALID" || ce.Exit != cli.ExitUsage {
		t.Errorf("bad login = %+v", ce)
	}
	if ce, _ := run("show"); ce.Code != "USAGE" {
		t.Errorf("usage = %+v", ce)
	}
}

func TestAccountShowLiveMerge(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{
			Warm: &angel.WarmInfo{Nodes: []angel.NodeHealth{
				{Name: "netivri-r0-0", Label: "main/m-de", Alive: true, DelayMS: 42},
			}},
			Activity: []angel.AccountActivity{{Login: "alice", Nodes: []angel.NodeUse{
				{Node: "main/m-de", Connections: 3},
			}}},
		}, nil
	}
	known := map[string]bool{"main": true}
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE"), node("m-nl", "NL")),
	}
	tiers := buildShowTiers(known, snapshots, [][]string{{"main"}}, []string{"DE", "NL"})
	if !accountShowLive(context.Background(), "whatever-home", "alice", tiers) {
		t.Fatal("accountShowLive = false, want true")
	}
	de := tiers[0].Nodes[0]
	if de.Alive == nil || !*de.Alive || de.DelayMS == nil || *de.DelayMS != 42 || de.Connections == nil || *de.Connections != 3 {
		t.Errorf("DE node = %+v", de)
	}
	nl := tiers[1].Nodes[0]
	if nl.Alive != nil || nl.DelayMS != nil || nl.Connections == nil || *nl.Connections != 0 {
		t.Errorf("NL node = %+v", nl)
	}
}

func TestAccountShowNoAngel(t *testing.T) {
	old := angelProbeLive
	t.Cleanup(func() { angelProbeLive = old })
	angelProbeLive = func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	}
	known := map[string]bool{"main": true}
	snapshots := map[string]*subscription.Snapshot{
		"main": snap(node("m-de", "DE")),
	}
	tiers := buildShowTiers(known, snapshots, [][]string{{"main"}}, []string{"DE"})
	if accountShowLive(context.Background(), "whatever-home", "alice", tiers) {
		t.Fatal("accountShowLive = true, want false")
	}
	n := tiers[0].Nodes[0]
	if n.Alive != nil || n.DelayMS != nil || n.Connections != nil {
		t.Errorf("node = %+v", n)
	}
}
