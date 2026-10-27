package core

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubErrorKeepsPlainWordsAndDropsAddresses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"unexpected status 502 from upstream", "unexpected status 502 from upstream"},
		{"dial tcp 203.0.113.9:443: bad gateway", "dial tcp <addr> bad gateway"},
		{"read tcp 10.0.0.1:5000->203.0.113.9:443: weird", "read tcp <addr> weird"},
		{"proxy node.example.com refused", "proxy <addr> refused"},
		{"socks5 auth failed for user:secret@host", "<addr> auth failed for <addr>"},
		{"bad [2001:db8::1]:443 here", "bad <addr> here"},
		{"tunnel узел closed", "tunnel <addr> closed"},
		{"server1 said no", "<addr> said no"},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := scrubError(strings.ToLower(c.in)); got != c.want {
			t.Errorf("scrubError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestScrubErrorIsShort(t *testing.T) {
	long := strings.Repeat("alpha beta gamma ", 20)
	got := scrubError(long)
	if len(got) > scrubMaxBytes || len(strings.Fields(got)) > scrubMaxWords {
		t.Errorf("scrubError kept %d bytes, %d words: %q", len(got), len(strings.Fields(got)), got)
	}
}

func TestProbeFailKindOfAnUnknownErrorHasNoAddress(t *testing.T) {
	got := probeFailKind(errors.New("Upstream 203.0.113.9:8443 said something strange"))
	if got != "error: upstream <addr> said something strange" {
		t.Errorf("probeFailKind = %q", got)
	}
	if strings.Contains(got, "203") || strings.Contains(got, "8443") {
		t.Errorf("an address leaked into %q", got)
	}
}

func TestProbeFailKindOfAnEmptyErrorIsPlainError(t *testing.T) {
	if got := probeFailKind(errors.New("")); got != "error" {
		t.Errorf("probeFailKind = %q, want error", got)
	}
}
