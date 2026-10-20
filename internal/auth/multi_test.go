package auth

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func newMultiForTest(t *testing.T, o MultiOptions) *multiAuthenticator {
	t.Helper()
	a, err := NewMulti(o)
	if err != nil {
		t.Fatalf("NewMulti: %v", err)
	}
	return a.(*multiAuthenticator)
}

func requireConfigInvalid(t *testing.T, o MultiOptions, secrets ...string) {
	t.Helper()
	a, err := NewMulti(o)
	if a != nil {
		t.Fatal("invalid config returned authenticator")
	}
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "CONFIG_INVALID" {
		t.Fatalf("got %T %v, want CONFIG_INVALID", err, err)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked a credential")
		}
	}
}

func TestNewMultiValidation(t *testing.T) {
	setFastCost(t)
	h := validHashFor(t, "secret")
	cases := []struct {
		name, login, password string
	}{
		{"empty", "", h},
		{"slash", "bad/name", h},
		{"space", "bad name", h},
		{"comma", "bad,name", h},
		{"unicode", "álîce", h},
		{"control", "ali\nce", h},
		{"too long", strings.Repeat("a", 65), h},
		{"init login", InitPlaceholder, h},
		{"empty password", "alice", ""},
		{"plaintext", "alice", "plain"},
		{"init without callback", "alice", InitPlaceholder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := MultiOptions{
				Accounts: []AccountSpec{{Login: tc.login, Password: tc.password}},
			}
			requireConfigInvalid(t, o, h, tc.password)
		})
	}
	for _, login := range []string{"A", "z9._@-", strings.Repeat("x", 64)} {
		o := MultiOptions{
			Accounts:       []AccountSpec{{Login: login, Password: InitPlaceholder}},
			PersistAccount: func(string, string) error { return nil },
		}
		if _, err := NewMulti(o); err != nil {
			t.Fatalf("valid login %q rejected: %v", login, err)
		}
	}
	for _, o := range []MultiOptions{
		{Accounts: []AccountSpec{
			{Login: "alice", Password: h},
			{Login: "alice", Password: h},
		}},
		{
			Legacy:   Options{Login: "alice", Password: h},
			Accounts: []AccountSpec{{Login: "alice", Password: h}},
		},
		{Legacy: Options{Login: "legacy"}},
		{Legacy: Options{Password: h}},
	} {
		requireConfigInvalid(t, o, h, "secret")
	}
}

func TestNewMultiVerificationAndUnknownNoArgon(t *testing.T) {
	setFastCost(t)
	tests := []struct {
		name string
		user string
		pass string
	}{
		{name: "empty user", user: "", pass: "shared"},
		{name: "empty password", user: "alice", pass: ""},
		{name: "oversized password", user: "alice", pass: strings.Repeat("x", maxPasswordLen+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newMultiForTest(t, MultiOptions{
				Accounts:       []AccountSpec{{Login: "alice", Password: InitPlaceholder}},
				PersistAccount: func(string, string) error { return nil },
			})
			if a.Verify(tc.user, tc.pass) {
				t.Fatal("invalid credential accepted")
			}
		})
	}
	h := validHashFor(t, "shared")
	a := newMultiForTest(t, MultiOptions{
		Legacy:   Options{Login: "legacy", Password: h},
		Accounts: []AccountSpec{{Login: "alice", Password: h}},
	})
	account := a.accounts["alice"]
	if !a.Verify("alice", "shared") {
		t.Fatal("correct account password rejected")
	}
	if account.cached != "shared" {
		t.Fatal("successful password not cached")
	}
	if a.Verify("alice", "wrong") {
		t.Fatal("wrong cached password accepted")
	}
	unknown := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{Login: "alice", Password: h}},
	})
	for i := 0; i < cap(unknown.slots); i++ {
		unknown.slots <- struct{}{}
	}
	released := false
	releaseSlots := func() {
		if released {
			return
		}
		released = true
		for i := 0; i < cap(unknown.slots); i++ {
			<-unknown.slots
		}
	}
	defer releaseSlots()
	done := make(chan bool, 1)
	go func() {
		done <- unknown.Verify("unlisted", "shared")
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("unknown user accepted")
		}
	case <-time.After(time.Second):
		releaseSlots()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("unknown-user verification did not exit")
		}
		t.Fatal("unknown-user lookup waited for Argon2 semaphore")
	}
}

func TestNewMultiWrongPasswordDoesNotFallBackToLegacy(t *testing.T) {
	setFastCost(t)
	persisted := 0
	legacyHash := validHashFor(t, "legacy-secret")
	a := newMultiForTest(t, MultiOptions{
		Legacy: Options{
			Login:    InitPlaceholder,
			Password: legacyHash,
			Persist: func(string, string) error {
				persisted++
				return nil
			},
		},
		Accounts: []AccountSpec{{Login: "alice", Password: validHashFor(t, "account-secret")}},
	})
	if a.Verify("alice", "legacy-secret") {
		t.Fatal("account miss fell through to matching legacy password")
	}
	if persisted != 0 {
		t.Fatal("account miss claimed legacy login")
	}
	if !a.Verify("alice", "account-secret") {
		t.Fatal("account password rejected")
	}
	if !a.Verify("legacy-user", "legacy-secret") {
		t.Fatal("legacy login claim failed")
	}
	if persisted != 1 {
		t.Fatalf("legacy persisted %d times", persisted)
	}
}

func TestNewMultiSamePasswordInitRace(t *testing.T) {
	setFastCost(t)
	var calls int32
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{Login: "alice", Password: InitPlaceholder}},
		PersistAccount: func(string, string) error {
			atomic.AddInt32(&calls, 1)
			return nil
		},
	})
	const n = 32
	var accepted int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if a.Verify("alice", "same-secret") {
				atomic.AddInt32(&accepted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := atomic.LoadInt32(&accepted); got != n {
		t.Fatalf("accepted=%d, want %d", got, n)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("persist calls=%d, want 1", got)
	}
}

func TestNewMultiDistinctInitRace(t *testing.T) {
	setFastCost(t)
	var calls int32
	var winner string
	var mu sync.Mutex
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{{Login: "alice", Password: InitPlaceholder}},
		PersistAccount: func(_, h string) error {
			atomic.AddInt32(&calls, 1)
			mu.Lock()
			defer mu.Unlock()
			for _, p := range []string{"first", "second"} {
				if ok, _ := VerifyHash(h, p); ok {
					winner = p
					return nil
				}
			}
			return errors.New("unexpected hash")
		},
	})
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, pass := range []string{"first", "second"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			<-start
			results <- a.Verify("alice", p)
		}(pass)
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for ok := range results {
		if ok {
			accepted++
		}
	}
	if accepted != 1 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("accepted=%d persists=%d", accepted, calls)
	}
	mu.Lock()
	winning := winner
	mu.Unlock()
	if winning == "" || !a.Verify("alice", winning) {
		t.Fatal("winner credentials were not retained")
	}
}

func TestNewMultiInitRetryIsolationAndUsers(t *testing.T) {
	setFastCost(t)
	failed := true
	a := newMultiForTest(t, MultiOptions{
		Accounts: []AccountSpec{
			{Login: "alice", Password: InitPlaceholder},
			{Login: "bob", Password: InitPlaceholder},
		},
		PersistAccount: func(login, _ string) error {
			if login == "alice" && failed {
				return errors.New("disk")
			}
			return nil
		},
	})
	if a.Verify("alice", "first") {
		t.Fatal("failed persistence accepted")
	}
	failed = false
	if !a.Verify("alice", "different-retry") || a.Verify("alice", "first") {
		t.Fatal("failed init did not retry with a different password")
	}
	if !a.Verify("bob", "bob-pass") {
		t.Fatal("failure affected another account")
	}
	if !reflect.DeepEqual(a.Users(), []string{"alice", "bob"}) {
		t.Fatalf("Users=%v", a.Users())
	}
}

func TestNewMultiLegacyInitCollisionCannotClaimAccountLogin(t *testing.T) {
	setFastCost(t)
	for _, mode := range []string{"password-init", "login-init-hash"} {
		t.Run(mode, func(t *testing.T) {
			persisted := 0
			legacyPass := "legacy-secret"
			legacy := Options{
				Login:    InitPlaceholder,
				Password: InitPlaceholder,
				Persist: func(string, string) error {
					persisted++
					return nil
				},
			}
			if mode == "login-init-hash" {
				legacyPass = "known"
				legacy = Options{
					Login:    InitPlaceholder,
					Password: validHashFor(t, legacyPass),
					Persist: func(string, string) error {
						persisted++
						return nil
					},
				}
			}
			a := newMultiForTest(t, MultiOptions{
				Legacy:   legacy,
				Accounts: []AccountSpec{{Login: "alice", Password: validHashFor(t, "account")}},
			})
			if !a.Verify("alice", "account") {
				t.Fatal("account-first auth rejected")
			}
			if a.Verify("alice", legacyPass) {
				t.Fatal("legacy credential authenticated as account")
			}
			if legacyAuth, ok := a.legacy.(*authenticator); ok {
				if mode == "password-init" {
					legacyAuth.mu.Lock()
					stillInit := legacyAuth.hashIsInit
					legacyAuth.mu.Unlock()
					if !stillInit {
						t.Fatal("legacy init was consumed by account login")
					}
				} else {
					legacyAuth.mu.Lock()
					stillUnclaimed := legacyAuth.loginIsInit
					legacyAuth.mu.Unlock()
					if !stillUnclaimed {
						t.Fatal("legacy login was claimed by account login")
					}
				}
			}
			if persisted != 0 {
				t.Fatalf("legacy claim persisted %d times", persisted)
			}
			if !a.Verify("legacy-user", legacyPass) {
				t.Fatal("separate legacy login could not claim credential")
			}

		})
	}
}

func TestNewMultiLegacyInitSharesSemaphore(t *testing.T) {
	setFastCost(t)
	m := newMultiForTest(t, MultiOptions{
		Legacy: Options{
			Login: "legacy", Password: InitPlaceholder,
			Persist: func(string, string) error { return nil },
		},
		Accounts:       []AccountSpec{{Login: "alice", Password: InitPlaceholder}},
		PersistAccount: func(string, string) error { return nil },
	})
	legacy := m.legacy.(*authenticator)
	if legacy.sharedSlots != m.slots {
		t.Fatal("legacy authenticator does not share Multi semaphore")
	}
	standalone, err := New(Options{
		Login: "legacy", Password: InitPlaceholder,
		Persist: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if standalone.(*authenticator).sharedSlots != nil {
		t.Fatal("standalone New unexpectedly has a Multi semaphore")
	}
	if !m.Verify("legacy", "pw") {
		t.Fatal("legacy init failed")
	}
}

func TestNewMultiUsersWithUnknownLegacyAndAccounts(t *testing.T) {
	setFastCost(t)
	a := newMultiForTest(t, MultiOptions{
		Legacy: Options{
			Login: InitPlaceholder, Password: validHashFor(t, "legacy"),
			Persist: func(string, string) error { return nil },
		},
		Accounts: []AccountSpec{{Login: "alice", Password: validHashFor(t, "account")}},
	})
	if got := a.Users(); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Fatalf("Users=%v", got)
	}
}

func TestNewMultiMixedKnownLegacyUsers(t *testing.T) {
	setFastCost(t)
	a := newMultiForTest(t, MultiOptions{
		Legacy:   Options{Login: "legacy", Password: validHashFor(t, "legacy")},
		Accounts: []AccountSpec{{Login: "alice", Password: validHashFor(t, "account")}},
	})
	if got := a.Users(); !reflect.DeepEqual(got, []string{"alice", "legacy"}) {
		t.Fatalf("Users=%v", got)
	}
}

func TestNewMultiUsersEmptyForUnknownLegacyLogin(t *testing.T) {
	setFastCost(t)
	a := newMultiForTest(t, MultiOptions{
		Legacy: Options{
			Login: InitPlaceholder, Password: validHashFor(t, "pw"),
			Persist: func(string, string) error { return nil },
		},
	})
	if got := a.Users(); got == nil || len(got) != 0 {
		t.Fatalf("Users=%v, want non-nil empty", got)
	}
}

func TestNewMultiNoAccountsAndStandaloneNewCompatibility(t *testing.T) {
	if a, err := NewMulti(MultiOptions{}); err != nil || a != nil {
		t.Fatalf("empty NewMulti: %v %v", a, err)
	}
	setFastCost(t)
	a, err := New(Options{Login: "user", Password: validHashFor(t, "pw")})
	if err != nil || !a.Verify("user", "pw") || a.Verify("other", "pw") {
		t.Fatalf("standalone New compatibility: %v", err)
	}
}
