package hop

import (
	"strings"
	"testing"
)

func TestHopKeyCanonicalIdentity(t *testing.T) {
	cases := []struct {
		name                  string
		first, second         string
		firstWant, secondWant string
	}{
		{name: "scheme and host case", first: "HTTP://Example.COM", second: "http://example.com:080", firstWant: "0ed5ddebcdc99af68c6e5e3803ea0a5e72e3327bde21a912d344245415ca4aff", secondWant: "0ed5ddebcdc99af68c6e5e3803ea0a5e72e3327bde21a912d344245415ca4aff"},
		{name: "default and explicit socks port", first: "socks5://proxy.example", second: "SOCKS5://PROXY.EXAMPLE:01080", firstWant: "1a587fc2df6c07a9619e8c021e3595c51032eb1a6de94be19bdf8464b157e8ef", secondWant: "1a587fc2df6c07a9619e8c021e3595c51032eb1a6de94be19bdf8464b157e8ef"},
		{name: "https default port", first: "https://proxy.example", second: "https://proxy.example:443", firstWant: "01e4af4a6098529fe1a4d53da83de76a597233df20c8f32ec42d3959605dbd6a", secondWant: "01e4af4a6098529fe1a4d53da83de76a597233df20c8f32ec42d3959605dbd6a"},
		{name: "ipv6 canonical compression", first: "http://[2001:0DB8:0:0:0:0:0:1]", second: "http://[2001:db8::1]:80", firstWant: "c7d90feb91c3b9b76d28a7ca8df828574a0232f800f117cd2663280c8c63203b", secondWant: "c7d90feb91c3b9b76d28a7ca8df828574a0232f800f117cd2663280c8c63203b"},
		{name: "ignored credentials path query fragment", first: "http://user:credential-one@proxy.example/a?x=one#first", second: "http://different:credential-two@PROXY.EXAMPLE/b?x=two#second", firstWant: "58d76ab9cb37cab8016ed06c0c3fba8fa4a12fd08db1152c2ad369fdfc44e4ac", secondWant: "58d76ab9cb37cab8016ed06c0c3fba8fa4a12fd08db1152c2ad369fdfc44e4ac"},
		{name: "password variants", first: "socks5://user:first-secret@proxy.example", second: "socks5://user:second-secret@proxy.example", firstWant: "1a587fc2df6c07a9619e8c021e3595c51032eb1a6de94be19bdf8464b157e8ef", secondWant: "1a587fc2df6c07a9619e8c021e3595c51032eb1a6de94be19bdf8464b157e8ef"},
		{name: "different scheme", first: "http://example.com", second: "https://example.com", firstWant: "0ed5ddebcdc99af68c6e5e3803ea0a5e72e3327bde21a912d344245415ca4aff", secondWant: "3276d403185d4a1ac89938f0f94a4b6d040c673655c8e6978435737025d434f1"},
		{name: "different host", first: "http://example.com", second: "http://other.example", firstWant: "0ed5ddebcdc99af68c6e5e3803ea0a5e72e3327bde21a912d344245415ca4aff", secondWant: "91d41a5e1523b19d62d8a8ba8fb30c63707e6cfb6901e39fbfd134b67a952b5f"},
		{name: "different port", first: "http://example.com", second: "http://example.com:81", firstWant: "0ed5ddebcdc99af68c6e5e3803ea0a5e72e3327bde21a912d344245415ca4aff", secondWant: "213abcd89e3f70e89e5e5732134f0e4a4bcc72bca2cd1346ac9f223d92144e7b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, ok1 := Key(tc.first)
			second, ok2 := Key(tc.second)
			if !ok1 || !ok2 || first != tc.firstWant || second != tc.secondWant || !ValidKey(first) || !ValidKey(second) || strings.Contains(first, "first-secret") || strings.Contains(second, "credential-two") {
				t.Fatal("canonical identities did not match expected hashes")
			}
		})
	}
}

func TestHopKeyRejectsInvalidURLs(t *testing.T) {
	invalid := []string{
		"", "none", "garbage", "ftp://proxy.example", "socks5:proxy.example",
		"http:///missing", "https://", "http://:80", "http://proxy.example:",
		"http://proxy.example:0", "http://proxy.example:65536", "http://proxy.example:-1",
		"http://[2001:db8::1", "http://[example.com]", "http://[192.0.2.1]", "http://2001:db8::1:80", "http://bücher.example",
	}
	for _, raw := range invalid {
		if _, ok := Key(raw); ok {
			t.Fatal("invalid proxy URL was accepted")
		}
	}
}

func TestHopKeyAcceptsMappedIPv6(t *testing.T) {
	key, ok := Key("http://[::ffff:192.0.2.1]")
	if !ok || key != "7ca79746435286842b372320e29d143fe5b4505e7d618cbc2d2e9f9a768aaaac" {
		t.Fatal("valid mapped IPv6 proxy address was rejected or miscanonicalized")
	}
}

func TestHopKeyAcceptsUnderscoreHosts(t *testing.T) {
	first, ok1 := Key("socks5://user:secret@my_proxy.example:1080")
	second, ok2 := Key("SOCKS5://MY_PROXY.EXAMPLE:1080")
	if !ok1 || !ok2 || first != second || !ValidKey(first) {
		t.Fatalf("underscore host keys = (%q, %t) and (%q, %t), want one valid key", first, ok1, second, ok2)
	}
	other, ok := Key("socks5://my-proxy.example:1080")
	if !ok || other == first {
		t.Fatal("underscore and hyphen hosts share a key")
	}
}
