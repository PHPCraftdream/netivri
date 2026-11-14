package angel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountInfoHopPoolsAreAdditiveJSON(t *testing.T) {
	raw, err := json.Marshal(AccountInfo{Login: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hop_pools") {
		t.Fatalf("an account without pools carries hop_pools: %s", raw)
	}

	due := int64(42)
	raw, err = json.Marshal(AccountInfo{Login: "alice", HopPools: []HopPoolInfo{{
		Role: "post", HopCount: 2, ActiveCount: 1, QuarantinedCount: 1,
		Hops: []HopInfo{{ID: "1a2b3c4d", State: "active"}, {ID: "5e6f7a8b", State: "quarantined", Trips: 2, NextRevivalSeconds: &due}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `"hop_pools":[{"role":"post","hop_count":2,"active_count":1,"quarantined_count":1,` +
		`"hops":[{"id":"1a2b3c4d","state":"active","trips":0},` +
		`{"id":"5e6f7a8b","state":"quarantined","trips":2,"next_revival_seconds":42}]}]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("JSON = %s; want it to contain %s", raw, want)
	}
}
