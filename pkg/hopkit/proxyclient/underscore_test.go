package proxyclient

import (
	"testing"
)

func TestParseAcceptsUnderscoreHostsAndRejectsNonASCII(t *testing.T) {
	p, err := Parse("socks5://user:secret@my_proxy.example:1080")
	if err != nil || p.Host != "my_proxy.example" || p.Port != 1080 {
		t.Fatalf("Parse underscore host = (%+v, %v), want host my_proxy.example", p, err)
	}
	for _, raw := range []string{"socks5://bücher.example:1080", "http://-x.example:80"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q) accepted an invalid host", raw)
		}
	}
}

func TestValidateTargetAcceptsUnderscoreSites(t *testing.T) {
	for _, target := range []string{"my_site.example.com:443", "_dmarc.example.com:443", "a_b:80"} {
		if _, _, err := validateTarget(target); err != nil {
			t.Fatalf("validateTarget(%q) = %v, want accepted", target, err)
		}
	}
	for _, target := range []string{"bücher.example:443", "-bad.example:443", "a..b:443", "space here:443"} {
		if _, _, err := validateTarget(target); err == nil {
			t.Fatalf("validateTarget(%q) accepted an invalid host", target)
		}
	}
}

func TestSOCKS5SendsUnderscoreDomainVerbatim(t *testing.T) {
	client, server := pipePair(t)
	peer := peerResult()
	const host = "my_site.example"
	go func() {
		_, err := readExact(server, 3)
		if err == nil {
			_, err = server.Write([]byte{5, 0})
		}
		var request []byte
		if err == nil {
			request, err = readExact(server, 5+len(host)+2)
		}
		if err == nil {
			checkWire(t, request, append(append([]byte{5, 1, 0, 3, byte(len(host))}, host...), 1, 187))
			_, err = server.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		}
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	conn, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, host+":443")
	if err != nil || conn == nil {
		t.Fatalf("underscore target handshake = (%v, %v)", conn, err)
	}
	waitErr(t, peer)
}
