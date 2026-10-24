package core

import (
	"errors"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// The first tier that has a member not down wins, however slow it is.
func TestSelectMemberPrefersFirstTier(t *testing.T) {
	views := []memberHealthView{
		{Label: "slow0", Known: true, Score: 900, Tier: 0},
		{Label: "fast1", Known: true, Score: 10, Tier: 1},
	}
	if got := selectMember(views, "alice", "example.com"); got != 0 {
		t.Fatalf("selectMember = %d, want 0 (first tier)", got)
	}
}

// A first-tier member that has no delay yet is not down, so the tier counts.
func TestSelectMemberTierWithoutDelayDataStillWins(t *testing.T) {
	views := []memberHealthView{
		{Label: "new0", Tier: 0},
		{Label: "known1", Known: true, Score: 10, Tier: 1},
	}
	if got := selectMember(views, "alice", "example.com"); got != 0 {
		t.Fatalf("selectMember = %d, want 0", got)
	}
}

func TestSelectMemberFallsBackToNextTierWhenFirstIsDown(t *testing.T) {
	views := []memberHealthView{
		{Label: "a0", Known: true, Score: 10, Tier: 0, Down: true},
		{Label: "b0", Known: true, Score: 10, Tier: 0, Down: true},
		{Label: "c1", Known: true, Score: 500, Tier: 1},
		{Label: "d1", Known: true, Score: 20, Tier: 1},
		{Label: "e2", Known: true, Score: 1, Tier: 2},
	}
	if got := selectMember(views, "alice", "example.com"); got != 3 {
		t.Fatalf("selectMember = %d, want 3 (best of the first live tier)", got)
	}
}

func TestSelectMemberChoosesWithinOneTierByScore(t *testing.T) {
	views := []memberHealthView{
		{Label: "a0", Known: true, Score: 300, Tier: 0},
		{Label: "b0", Known: true, Score: 50, Tier: 0},
		{Label: "c1", Known: true, Score: 1, Tier: 1},
	}
	if got := selectMember(views, "alice", "example.com"); got != 1 {
		t.Fatalf("selectMember = %d, want 1", got)
	}
}

// With every member down the first tier is tried again, never a later one.
func TestSelectMemberAllDownKeepsFirstTier(t *testing.T) {
	views := []memberHealthView{
		{Label: "a0", Known: true, Tier: 0, Down: true},
		{Label: "b1", Known: true, Tier: 1, Down: true},
	}
	if got := selectMember(views, "alice", "example.com"); got != 0 {
		t.Fatalf("selectMember = %d, want 0", got)
	}
}

func TestCheckedTiers(t *testing.T) {
	if got, err := checkedTiers(nil, 3); got != nil || err != nil {
		t.Fatalf("no tiers: %v, %v", got, err)
	}
	in := []int{0, 0, 1, 3}
	got, err := checkedTiers(in, 4)
	if err != nil || len(got) != 4 || got[3] != 3 {
		t.Fatalf("valid tiers: %v, %v", got, err)
	}
	in[0] = 9
	if got[0] != 0 {
		t.Fatal("the result shares memory with the input")
	}
	for name, tiers := range map[string][]int{"length": {0, 1}, "decreasing": {0, 1, 0, 1}} {
		_, err := checkedTiers(tiers, 4)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "NODE_INVALID" {
			t.Errorf("%s: error = %#v", name, err)
		}
	}
}

func TestBuildRoutePlansCarriesTiersAndRejectsBadOnes(t *testing.T) {
	nodes := []map[string]any{
		testRouteNode("a", "a.example", 1080),
		testRouteNode("b", "b.example", 1080),
	}
	options := Options{
		AccountRoute: map[string]string{"alice": "k"},
		RouteNodes:   map[string][]map[string]any{"k": nodes},
		NodeTiers:    map[string][]int{"k": {0, 1}},
	}
	plans, err := buildRoutePlans(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].tiers) != 2 || plans[0].tiers[1] != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	options.NodeTiers = map[string][]int{"k": {1, 0}}
	if _, err := buildRoutePlans(options); err == nil {
		t.Fatal("decreasing tiers were accepted")
	}
	options.NodeTiers = map[string][]int{"k": {0}}
	if _, err := buildRoutePlans(options); err == nil {
		t.Fatal("a tier count that does not match the route was accepted")
	}
	options.NodeTiers = nil
	plans, err = buildRoutePlans(options)
	if err != nil || plans[0].tiers != nil {
		t.Fatalf("without tiers: %+v, %v", plans, err)
	}
}

// The sticky group applies its tiers: the slower first-tier node is chosen
// until it is down, then the second tier takes over; a binding that already
// moved stays there when the first tier recovers, new sites go back.
func TestStickyGroupHonoursTiers(t *testing.T) {
	a := &fakeMember{name: "a"}
	b := &fakeMember{name: "b"}
	g, _, health := newTestGroup(t, []*fakeMember{a, b}, nil)
	g.tiers = []int{0, 1}
	health.recordProbe("a", true, 300*time.Millisecond)
	health.recordProbe("b", true, 10*time.Millisecond)

	if index, _ := g.choose("alice", "one.example"); index != 0 {
		t.Fatalf("first site bound to %d, want 0 (first tier, though slower)", index)
	}
	for i := 0; i < downDialFailures; i++ {
		health.recordDial("a", false, 0)
	}
	if index, _ := g.choose("alice", "one.example"); index != 1 {
		t.Fatalf("the binding of a down node moved to %d, want 1", index)
	}
	if index, _ := g.choose("alice", "two.example"); index != 1 {
		t.Fatalf("new site bound to %d while the first tier is down, want 1", index)
	}

	health.recordProbe("a", true, 300*time.Millisecond)
	if index, _ := g.choose("alice", "one.example"); index != 1 {
		t.Fatalf("existing binding moved to %d on recovery, want it to stay on 1", index)
	}
	if index, _ := g.choose("alice", "three.example"); index != 0 {
		t.Fatalf("new site bound to %d after recovery, want 0", index)
	}
}
