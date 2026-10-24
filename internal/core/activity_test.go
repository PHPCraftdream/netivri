package core

import (
	"reflect"
	"testing"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

func activityTracker(id, login string, chain C.Chain, up, down int64) *fakeTracker {
	info := &statistic.TrackerInfo{Metadata: &C.Metadata{InUser: login}, Chain: chain}
	info.UploadTotal.Store(up)
	info.DownloadTotal.Store(down)
	return &fakeTracker{id: id, info: info}
}

var activityLabels = map[string]string{
	"netivri-r0-0": "main/a",
	"netivri-r0-1": "main/b",
	"netivri-r1-0": "alt/c",
}

func TestCollectActivityGroupsByAccountAndNode(t *testing.T) {
	source := &fakeTrackerSource{trackers: []throughputTracker{
		activityTracker("1", "bob", C.Chain{"netivri-r1-0", "group"}, 10, 20),
		activityTracker("2", "alice", C.Chain{"netivri-r0-0", "group"}, 1, 2),
		activityTracker("3", "alice", C.Chain{"netivri-r0-0", "group"}, 3, 4),
		activityTracker("4", "alice", C.Chain{"netivri-r0-1", "group"}, 100, 200),
		activityTracker("5", "alice", C.Chain{"netivri-r0-1", "group"}, 5, 6),
		activityTracker("6", "alice", C.Chain{"netivri-r0-1", "group"}, 7, 8),
	}}
	got := collectActivity(source, activityLabels)
	want := []AccountActivity{
		{
			Login: "alice", Connections: 5, Upload: 116, Download: 220,
			Nodes: []NodeActivity{
				{Label: "main/b", Connections: 3, Upload: 112, Download: 214},
				{Label: "main/a", Connections: 2, Upload: 4, Download: 6},
			},
		},
		{
			Login: "bob", Connections: 1, Upload: 10, Download: 20,
			Nodes: []NodeActivity{{Label: "alt/c", Connections: 1, Upload: 10, Download: 20}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activity =\n%+v\nwant\n%+v", got, want)
	}
}

func TestCollectActivityTiesSortByLabel(t *testing.T) {
	source := &fakeTrackerSource{trackers: []throughputTracker{
		activityTracker("1", "alice", C.Chain{"netivri-r0-1"}, 0, 0),
		activityTracker("2", "alice", C.Chain{"netivri-r0-0"}, 0, 0),
	}}
	got := collectActivity(source, activityLabels)
	if len(got) != 1 || len(got[0].Nodes) != 2 || got[0].Nodes[0].Label != "main/a" || got[0].Nodes[1].Label != "main/b" {
		t.Fatalf("activity = %+v", got)
	}
}

func TestCollectActivityDirectAndUnroutedNodes(t *testing.T) {
	source := &fakeTrackerSource{trackers: []throughputTracker{
		activityTracker("1", "carol", C.Chain{"DIRECT"}, 1, 1),
		activityTracker("2", "carol", C.Chain{"some-global-node"}, 1, 1),
		activityTracker("3", "carol", C.Chain{}, 1, 1),
	}}
	got := collectActivity(source, nil)
	if len(got) != 1 || got[0].Connections != 3 {
		t.Fatalf("activity = %+v", got)
	}
	labels := map[string]int{}
	for _, n := range got[0].Nodes {
		labels[n.Label] = n.Connections
	}
	if labels[ActivityDirect] != 1 || labels[ActivityNode] != 2 || len(labels) != 2 {
		t.Fatalf("labels = %v", labels)
	}
}

func TestCollectActivitySkipsConnectionsWithoutLogin(t *testing.T) {
	noMeta := &fakeTracker{id: "1", info: &statistic.TrackerInfo{Chain: C.Chain{"netivri-r0-0"}}}
	noInfo := &fakeTracker{id: "2"}
	anonymous := activityTracker("3", "", C.Chain{"netivri-r0-0"}, 1, 1)
	source := &fakeTrackerSource{trackers: []throughputTracker{noMeta, noInfo, anonymous}}
	if got := collectActivity(source, activityLabels); len(got) != 0 {
		t.Fatalf("activity = %+v, want none", got)
	}
}

func TestCollectActivityEmpty(t *testing.T) {
	got := collectActivity(&fakeTrackerSource{}, activityLabels)
	if got == nil || len(got) != 0 {
		t.Fatalf("activity = %#v, want an empty list", got)
	}
}

func TestActivityLabelPrefersMemberOverDirect(t *testing.T) {
	// A member that dials through DIRECT-named hops still counts as the member.
	got := activityLabel(C.Chain{"DIRECT", "netivri-r1-0"}, activityLabels)
	if got != "alt/c" {
		t.Fatalf("label = %q", got)
	}
}
