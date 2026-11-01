package angel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusServersJSON(t *testing.T) {
	st := Status{Servers: []ServerInfo{{Name: "edge", Protocol: "ss", Listen: "127.0.0.1:9", Accounts: []string{"alice"}, Connections: 2, DroppedClients: 3}}}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	want := `"servers":[{"name":"edge","protocol":"ss","listen":"127.0.0.1:9","accounts":["alice"],"connections":2,"dropped_clients":3}]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("status JSON %s lacks %s", raw, want)
	}
	var got Status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 || got.Servers[0].Connections != 2 {
		t.Fatalf("round trip: %#v", got.Servers)
	}
	empty, err := json.Marshal(Status{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), `"servers"`) {
		t.Fatalf("empty servers should be omitted: %s", empty)
	}
}
