package config

import (
	"strings"
	"testing"
)

func TestSOCKS5ChainValidation(t *testing.T) {
	valid := []string{"", "none", "NONE", "SoCkS5://proxy.example:1080", "socks5://user:p%40ss@[2001:db8::1]:1080"}
	for _, value := range valid {
		c := Default()
		c.Chain = Chain{PreSOCKS5: value, PostSOCKS5: value}
		c.Accounts = []Account{{Login: "alice", Password: "init", PreSOCKS5: value, PostSOCKS5: value}}
		if err := c.Validate(); err != nil {
			t.Errorf("accepted %q: %v", value, err)
		}
	}
	for _, value := range []string{"http://proxy.example", "socks5h://proxy.example:1080", "socks5://", "socks5://proxy.example", "socks5://proxy.example:1080/path", "socks5://proxy.example:1080?token=x", "socks5://proxy.example:1080#fragment", "socks5://proxy.example:bad", "socks5://user:%ZZ@proxy.example:1080", " socks5://proxy.example"} {
		c := Default()
		c.Chain.PreSOCKS5 = value
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %q", value)
		} else if strings.Contains(err.Error(), value) {
			t.Errorf("error leaked value %q: %v", value, err)
		}
	}
}

func TestSOCKS5ChainMarshalRoundTrip(t *testing.T) {
	c := Default()
	c.Chain = Chain{PreSOCKS5: "socks5://user:p%40ss@[::1]:1080", PostSOCKS5: "none"}
	c.Accounts = []Account{{Login: "alice", Password: "init", PreSOCKS5: "none", PostSOCKS5: "socks5://proxy.example:1080"}}
	doc, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "pre_socks5") || !strings.Contains(doc, "post_socks5") {
		t.Fatal("chain keys not rendered")
	}
	if !strings.Contains(doc, "pre_socks5: none") {
		t.Fatal("account none was not preserved in output")
	}
	if c.Accounts[0].PreSOCKS5 != "none" {
		t.Fatal("Marshal mutated the source account")
	}
	roundTrip := Default()
	if err := ktavLoadsInto(doc, roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Chain != c.Chain || roundTrip.Accounts[0].PreSOCKS5 != c.Accounts[0].PreSOCKS5 || roundTrip.Accounts[0].PostSOCKS5 != c.Accounts[0].PostSOCKS5 {
		t.Fatal("chain values did not round-trip")
	}
}
