package core

import (
	"net/netip"
	"testing"
)

func TestSiteKey(t *testing.T) {
	tests := []struct {
		name string
		host string
		ip   netip.Addr
		want string
	}{
		{name: "www", host: "www.example.com", want: "example.com"},
		{name: "deep subdomain", host: "a.b.example.com", want: "example.com"},
		{name: "multi-label suffix", host: "foo.example.co.uk", want: "example.co.uk"},
		{name: "trailing dot", host: "www.example.com.", want: "example.com"},
		{name: "upper case", host: "WWW.Example.COM", want: "example.com"},
		{name: " registrable alone", host: "example.com", want: "example.com"},
		{name: "public suffix as is", host: "co.uk", want: "co.uk"},
		{name: "dotless as is", host: "localhost", want: "localhost"},
		{name: "ipv4 literal", host: "1.2.3.4", want: "1.2.3.4"},
		{name: "ipv6 literal", host: "2001:DB8::1", want: "2001:db8::1"},
		{name: "empty host with ip", ip: netip.MustParseAddr("10.0.0.1"), want: "10.0.0.1"},
		{name: "empty host with ipv6 ip", ip: netip.MustParseAddr("2001:db8::1"), want: "2001:db8::1"},
		{name: "empty host without ip", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := siteKey(test.host, test.ip); got != test.want {
				t.Fatalf("siteKey(%q) = %q, want %q", test.host, got, test.want)
			}
		})
	}
}
