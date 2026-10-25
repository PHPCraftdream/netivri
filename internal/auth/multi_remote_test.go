package auth

import (
	"net"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func loopbackRules(t *testing.T) config.AllowedClients {
	t.Helper()
	rules, err := config.ParseAllowedClients("loopback")
	if err != nil {
		t.Fatalf("ParseAllowedClients: %v", err)
	}
	return rules
}

func tcpAddr(t *testing.T, ip string) net.Addr {
	t.Helper()
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}
}

func TestVerifyFromAccountRule(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{
			Login:          "alice",
			Password:       InitPlaceholder,
			AllowedClients: loopbackRules(t),
		}},
		PersistAccount: rec.persist,
	})
	const pass = "chosen-pass"
	// The disallowed peer sees the unclaimed init account first: it must
	// fail for any password and never trigger the claim.
	if a.VerifyFrom("alice", "anything", tcpAddr(t, "192.168.1.5")) {
		t.Fatal("refused peer authenticated")
	}
	if a.VerifyFrom("alice", pass, tcpAddr(t, "192.168.1.5")) {
		t.Fatal("refused peer guessed the unclaimed password")
	}
	if rec.count() != 0 {
		t.Fatalf("persist calls before claim = %d, want 0", rec.count())
	}
	// The allowed loopback peer claims the init password exactly once.
	if !a.VerifyFrom("alice", pass, tcpAddr(t, "127.0.0.1")) {
		t.Fatal("allowed peer could not claim the init password")
	}
	if rec.count() != 1 {
		t.Fatalf("persist calls = %d, want 1", rec.count())
	}
	// With the password known, the disallowed peer still fails and the
	// account stays claimed.
	if a.VerifyFrom("alice", pass, tcpAddr(t, "192.168.1.5")) {
		t.Fatal("refused peer authenticated with the right password")
	}
	if rec.count() != 1 {
		t.Fatalf("persist calls after refusal = %d, want 1", rec.count())
	}
	// A wrong password from the allowed peer fails too.
	if a.VerifyFrom("alice", "other-pass", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("a different password was accepted")
	}
}

func TestVerifyFromWithoutRule(t *testing.T) {
	setFastCost(t)
	a := newMultiForTest(t, MultiOptions{
		Accounts:       []AccountSpec{{Login: "alice", Password: InitPlaceholder}},
		PersistAccount: func(string, string) error { return nil },
	})
	if !a.VerifyFrom("alice", "pw", tcpAddr(t, "10.1.2.3")) {
		t.Fatal("account without a rule refused a remote peer")
	}
}

func TestVerifyFromWrongPasswordAllowedPeer(t *testing.T) {
	setFastCost(t)
	h := validHashFor(t, "secret")
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{
			Login:          "alice",
			Password:       h,
			AllowedClients: loopbackRules(t),
		}},
	})
	if a.VerifyFrom("alice", "wrong", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("wrong password accepted from an allowed peer")
	}
}

func TestVerifyFromIPv4MappedPeer(t *testing.T) {
	setFastCost(t)
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{
			Login:          "alice",
			Password:       InitPlaceholder,
			AllowedClients: loopbackRules(t),
		}},
		PersistAccount: func(string, string) error { return nil },
	})
	if !a.VerifyFrom("alice", "pw", tcpAddr(t, "::ffff:127.0.0.1")) {
		t.Fatal("IPv4-mapped loopback peer refused")
	}
}

func TestVerifyFromLegacyFallback(t *testing.T) {
	setFastCost(t)
	h := validHashFor(t, "legacy-pass")
	a := newMultiForTest(t, MultiOptions{
		Legacy:   Options{Login: "legacy", Password: h},
		Accounts: []AccountSpec{{Login: "alice", Password: h}},
	})
	if !a.VerifyFrom("legacy", "legacy-pass", tcpAddr(t, "192.168.1.5")) {
		t.Fatal("legacy credential refused by VerifyFrom")
	}
	if a.VerifyFrom("legacy", "wrong", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("legacy credential accepted a wrong password")
	}
}

// TestVerifyFromNilVersusEmptyMatcher pins the difference between a NIL
// matcher (no rule: any peer is allowed) and a non-nil EMPTY matcher (a rule
// that allows no peer at all). The empty matcher can only be built literally
// as config.AllowedClients{}: ParseAllowedClients fail-closes and never
// returns a zero-prefix set, so going through the parser can never produce
// the case under test.
func TestVerifyFromNilVersusEmptyMatcher(t *testing.T) {
	setFastCost(t)
	empty := config.AllowedClients{} // non-nil, zero prefixes
	h := validHashFor(t, "secret")
	rec := &persistRecorder{}
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{
			{Login: "init-acct", Password: InitPlaceholder, AllowedClients: empty},
			{Login: "hashed-acct", Password: h, AllowedClients: empty},
			{Login: "nil-rule", Password: h},
		},
		PersistAccount: rec.persist,
	})

	// Init account with the empty matcher: even the correct placeholder
	// shape is refused and nothing is persisted (no claim possible).
	if a.VerifyFrom("init-acct", "anything", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("empty matcher accepted a peer for the init account")
	}
	if rec.count() != 0 {
		t.Fatalf("persist calls = %d, want 0", rec.count())
	}

	// Hashed account with the empty matcher: the CORRECT password is still
	// refused from any peer.
	if a.VerifyFrom("hashed-acct", "secret", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("empty matcher accepted the correct password")
	}
	if a.VerifyFrom("hashed-acct", "secret", tcpAddr(t, "10.9.8.7")) {
		t.Fatal("empty matcher accepted a remote peer")
	}

	// Contrast: hashed account with a NIL matcher accepts the correct
	// password from any peer.
	if !a.VerifyFrom("nil-rule", "secret", tcpAddr(t, "10.9.8.7")) {
		t.Fatal("nil matcher refused the correct password")
	}
	if !a.VerifyFrom("nil-rule", "secret", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("nil matcher refused loopback")
	}
}

func TestVerifyFromUnknownLogin(t *testing.T) {
	setFastCost(t)
	h := validHashFor(t, "secret")
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{Login: "alice", Password: h}},
	})
	if a.VerifyFrom("nobody", "secret", tcpAddr(t, "127.0.0.1")) {
		t.Fatal("unknown login accepted")
	}
}
