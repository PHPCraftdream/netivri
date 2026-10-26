package main

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountStatusHashedRouteKeyCountry(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{Login: "a"}}}
	got := accountStatus(cfg, map[string]string{"a": "provider~DE~0123456789abcdef"})
	if len(got) != 1 {
		t.Fatalf("account count = %d, want 1", len(got))
	}
	if got[0].ExitCountry != "DE" {
		t.Fatalf("exit country = %q, want DE", got[0].ExitCountry)
	}
}
