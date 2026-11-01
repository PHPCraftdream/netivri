package servers

import "testing"

// An IP literal is never sent as SNI by a TLS client, so it cannot route a
// link to its server: it must not count as an explicit shared-port key.
func TestMatchKeysIPLiteralSNIIsNotExplicit(t *testing.T) {
	cases := []struct {
		name     string
		protocol Protocol
		params   map[string]string
		explicit bool
	}{
		{"trojan domain", trojanServer{}, map[string]string{"sni": "a.example"}, true},
		{"trojan ipv4", trojanServer{}, map[string]string{"sni": "127.0.0.1"}, false},
		{"trojan ipv6", trojanServer{}, map[string]string{"sni": "::1"}, false},
		{"anytls ipv4", anyTLS{}, map[string]string{"sni": "203.0.113.7"}, false},
		{"trusttunnel ipv4", trusttunnelProtocol{}, map[string]string{"sni": "203.0.113.7"}, false},
		{"reality domain", vlessReality{}, map[string]string{"sni": "www.example.com"}, true},
		{"reality ip dest", vlessReality{}, map[string]string{"dest": "203.0.113.9:443"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sniffer, ok := tc.protocol.(Sniffer)
			if !ok {
				t.Fatalf("%s is not a Sniffer", tc.protocol.Name())
			}
			if _, _, explicit := sniffer.MatchKeys(tc.params); explicit != tc.explicit {
				t.Fatalf("explicit = %v, want %v", explicit, tc.explicit)
			}
		})
	}
}

func TestValidateSharedPortIPLiteralSNIRequiresDefault(t *testing.T) {
	ipSNI := Spec{Name: "ip", Bind: "127.0.0.1", Port: 443, Protocol: "trojan", Params: map[string]string{"sni": "127.0.0.1"}}
	other := Spec{Name: "other", Bind: ipSNI.Bind, Port: ipSNI.Port, Protocol: "anytls", Params: map[string]string{"sni": "any.example"}}
	if err := ValidateSharedPort([]Spec{ipSNI, other}); err == nil {
		t.Fatal("a non-default server whose sni is an IP literal was accepted on a shared port")
	}
	ipSNI.Default = true
	if err := ValidateSharedPort([]Spec{ipSNI, other}); err != nil {
		t.Fatalf("an IP-literal sni on the default server was rejected: %v", err)
	}
}
