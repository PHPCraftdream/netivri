package core

import (
	"errors"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestServerSetPrepareSharedPortValidation(t *testing.T) {
	p := registerUnitServerProtocol(t)
	cases := []struct {
		name  string
		specs []servers.Spec
		code  string
	}{
		{
			name: "non sniffable overlap",
			specs: []servers.Spec{
				unitServerSpec("first", p.name, 19001),
				unitServerSpec("second", p.name, 19001),
			},
			code: "PORT_IN_USE",
		},
		{
			name: "ambiguous sniff overlap",
			specs: []servers.Spec{
				{Name: "first", Protocol: "trojan", Bind: "127.0.0.1", Port: 19002, Accounts: []string{"alice"}, Params: map[string]string{"sni": "example.test"}},
				{Name: "second", Protocol: "trojan", Bind: "127.0.0.1", Port: 19002, Accounts: []string{"bob"}, Params: map[string]string{"sni": "example.test"}},
			},
			code: "SERVER_PORT_AMBIGUOUS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := &serverSet{broker: newSocketBroker()}
			t.Cleanup(set.broker.close)
			_, err := set.prepare(tc.specs, nil, nil, nil)
			if err == nil {
				t.Fatal("prepare unexpectedly succeeded")
			}
			var cliErr *cli.Error
			if !errors.As(err, &cliErr) || cliErr.Code != tc.code {
				t.Fatalf("prepare error = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestServerSetInfoSharedEndpointAndUDPIsolation(t *testing.T) {
	broker := newSocketBroker()
	set := &serverSet{broker: broker, items: map[string]*serverEntry{}}
	defer broker.close()
	tcp := servers.Spec{Name: "tcp", Protocol: "trojan", Bind: "127.0.0.1", Port: 19003, Default: true, Accounts: []string{"alice"}}
	udp := servers.Spec{Name: "udp", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 19003, Accounts: []string{"bob"}}
	set.items[tcp.Name] = &serverEntry{spec: tcp, running: servers.Running{Name: tcp.Name, Protocol: tcp.Protocol, Listen: tcp.ListenAddress(), Accounts: tcp.Accounts}, counters: &serverCounters{}}
	set.items[udp.Name] = &serverEntry{spec: udp, running: servers.Running{Name: udp.Name, Protocol: udp.Protocol, Listen: udp.ListenAddress(), Accounts: udp.Accounts}, counters: &serverCounters{}}
	endpoint := &socketEndpoint{broker: broker, key: endpointKey{"tcp", tcp.ListenAddress()}, members: map[string]*socketLease{"tcp": {}}, changed: make(chan struct{})}
	endpoint.unclassified.Store(7)
	endpoint.slowFirstByte.Store(3)
	broker.endpoints[endpoint.key] = endpoint
	got := set.info()
	if len(got) != 2 {
		t.Fatalf("info entries = %d, want 2", len(got))
	}
	for _, info := range got {
		switch info.Name {
		case "tcp":
			if !info.Default || len(info.Members) != 1 || info.Members[0] != "tcp" || info.Unclassified != 7 || info.SlowFirstByte != 3 {
				t.Fatalf("TCP info = %#v", info)
			}
		case "udp":
			if len(info.Members) != 0 || info.Unclassified != 0 || info.SlowFirstByte != 0 {
				t.Fatalf("UDP inherited TCP endpoint stats: %#v", info)
			}
		}
	}
}
