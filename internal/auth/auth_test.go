package auth

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mihauth "github.com/metacubex/mihomo/component/auth"
)

// persistRecorder is a thread-safe Persist callback.
type persistRecorder struct {
	mu     sync.Mutex
	calls  [][2]string
	err    error
	callsN int32
}

func (p *persistRecorder) persist(login, hash string) error {
	atomic.AddInt32(&p.callsN, 1)
	p.mu.Lock()
	p.calls = append(p.calls, [2]string{login, hash})
	p.mu.Unlock()
	return p.err
}

func (p *persistRecorder) count() int { return int(atomic.LoadInt32(&p.callsN)) }

func (p *persistRecorder) last() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return "", ""
	}
	c := p.calls[len(p.calls)-1]
	return c[0], c[1]
}

func mustNew(t *testing.T, o Options) *authenticator {
	t.Helper()
	a, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil authenticator")
	}
	return a.(*authenticator)
}

func TestNewDisabledReturnsTrueNil(t *testing.T) {
	a, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a != nil {
		t.Fatal("New with empty options must return a true nil interface")
	}
	var i mihauth.Authenticator = a
	if i != nil {
		t.Fatal("typed nil leaked into the interface")
	}
	// reflect.ValueOf on a nil interface yields the zero Value: it is
	// NOT valid, and calling IsNil on it would panic.
	if v := reflect.ValueOf(a); v.IsValid() && v.IsNil() {
		t.Fatal("unexpected non-nil reflect value")
	}
	if reflect.ValueOf(a).IsValid() {
		t.Fatal("reflect value of a nil interface must be invalid")
	}
}

func TestNewInvalidCombinations(t *testing.T) {
	setFastCost(t)
	hash := validHashFor(t, "hunter2")
	cases := []struct {
		name    string
		o       Options
		wantErr bool
	}{
		{"password without login", Options{Password: hash}, true},
		{"login without password", Options{Login: "user"}, true},
		{"plaintext password", Options{Login: "user", Password: "hunter2"}, true},
		{"malformed hash", Options{Login: "user", Password: "$argon2id$v=19$m=8,t=1,p=1$short$alsoshort"}, true},
		{"init without persist", Options{Login: "user", Password: InitPlaceholder}, true},
		{"init login without persist", Options{Login: InitPlaceholder, Password: hash}, true},
		{"init pair without persist", Options{Login: InitPlaceholder, Password: InitPlaceholder}, true},
		{"init login and password", Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: func(string, string) error { return nil }}, false},
		{"init password", Options{Login: "user", Password: InitPlaceholder, Persist: func(string, string) error { return nil }}, false},
		{"init login", Options{Login: InitPlaceholder, Password: hash, Persist: func(string, string) error { return nil }}, false},
		{"literal pair", Options{Login: "user", Password: hash}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.o
			if o.Persist == nil && tc.o.Password != InitPlaceholder && tc.o.Login != InitPlaceholder {
				o.Persist = func(string, string) error { return nil }
			}
			a, err := New(o)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), hash) {
					t.Errorf("error leaks a secret: %v", err)
				}
				return
			}
			if a == nil {
				t.Fatal("expected non-nil authenticator")
			}
		})
	}
}

func TestVerifyEmptyAndOversizedRejected(t *testing.T) {
	setFastCost(t)
	a := mustNew(t, Options{Login: "user", Password: validHashFor(t, "hunter2")})
	if a.Verify("", "hunter2") {
		t.Fatal("empty user accepted")
	}
	if a.Verify("user", "") {
		t.Fatal("empty password accepted")
	}
	if a.Verify("", "") {
		t.Fatal("empty pair accepted")
	}
	if a.Verify("user", strings.Repeat("x", maxPasswordLen+1)) {
		t.Fatal("oversized password accepted")
	}
}

// The bound must reject a password that WOULD match its hash, not merely
// one that fails verification anyway.
func TestVerifyPasswordLengthBound(t *testing.T) {
	setFastCost(t)
	atLimit := strings.Repeat("x", maxPasswordLen)
	a := mustNew(t, Options{Login: "user", Password: validHashFor(t, atLimit)})
	if !a.Verify("user", atLimit) {
		t.Fatal("matching password of exactly maxPasswordLen rejected")
	}
	over := strings.Repeat("x", maxPasswordLen+1)
	b := mustNew(t, Options{Login: "user", Password: validHashFor(t, over)})
	if b.Verify("user", over) {
		t.Fatal("matching password above maxPasswordLen accepted")
	}
}

func TestVerifyHashMode(t *testing.T) {
	setFastCost(t)
	hash := validHashFor(t, "hunter2")
	a := mustNew(t, Options{Login: "user", Password: hash})
	if !a.Verify("user", "hunter2") {
		t.Fatal("correct credentials rejected")
	}
	if a.Verify("user", "wrong") {
		t.Fatal("wrong password accepted")
	}
	if a.Verify("other", "hunter2") {
		t.Fatal("wrong user accepted")
	}
	if a.cachedPass == "" {
		t.Fatal("plaintext not cached after first success")
	}
	if !a.Verify("user", "hunter2") {
		t.Fatal("correct credentials rejected after cache")
	}
}

func TestCacheFastPath(t *testing.T) {
	setFastCost(t)
	a := mustNew(t, Options{Login: "user", Password: validHashFor(t, "hunter2")})
	if !a.Verify("user", "hunter2") {
		t.Fatal("first verify failed")
	}
	// Make the stored hash unusable: the cache must answer on its own.
	a.mu.Lock()
	a.hash = "bogus"
	a.mu.Unlock()
	if !a.Verify("user", "hunter2") {
		t.Fatal("cache fast path did not verify after the hash was destroyed")
	}
	if a.Verify("user", "wrong") {
		t.Fatal("cache accepted a wrong password")
	}
}

func TestInitPasswordFlow(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: "user", Password: InitPlaceholder, Persist: rec.persist})

	// Only the password is the placeholder: the literal login is
	// already known and must be reported.
	if users := a.Users(); !reflect.DeepEqual(users, []string{"user"}) {
		t.Fatalf("Users during password init = %v, want [user]", users)
	}
	if !a.Verify("user", "new-secret") {
		t.Fatal("init attempt rejected")
	}
	if rec.count() != 1 {
		t.Fatalf("Persist called %d times, want 1", rec.count())
	}
	login, hash := rec.last()
	if login != "user" {
		t.Fatalf("persisted login = %q", login)
	}
	if ok, err := VerifyHash(hash, "new-secret"); err != nil || !ok {
		t.Fatalf("persisted hash does not verify: %v %v", ok, err)
	}
	if users := a.Users(); !reflect.DeepEqual(users, []string{"user"}) {
		t.Fatalf("Users after init = %v", users)
	}
	if !a.Verify("user", "new-secret") {
		t.Fatal("verify after init failed")
	}
	if a.Verify("user", "other") {
		t.Fatal("wrong password accepted after init")
	}
}

func TestInitPasswordPersistFailureAndRetry(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{err: errors.New("disk full")}
	a := mustNew(t, Options{Login: "user", Password: InitPlaceholder, Persist: rec.persist})

	if a.Verify("user", "new-secret") {
		t.Fatal("attempt accepted despite Persist failure")
	}
	if users := a.Users(); !reflect.DeepEqual(users, []string{"user"}) {
		t.Fatalf("Users after failed persist = %v", users)
	}
	rec.err = nil
	if !a.Verify("user", "new-secret") {
		t.Fatal("good attempt rejected after failed persist")
	}
	if rec.count() != 2 {
		t.Fatalf("Persist called %d times, want 2", rec.count())
	}
}

func TestWrongUserDoesNotConsumeInit(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: "user", Password: InitPlaceholder, Persist: rec.persist})

	if a.Verify("attacker", "guess") {
		t.Fatal("wrong user accepted during init")
	}
	if rec.count() != 0 {
		t.Fatalf("wrong user consumed the init (Persist called %d times)", rec.count())
	}
	if !a.Verify("user", "real-secret") {
		t.Fatal("correct user rejected after wrong-user attempt")
	}
	if rec.count() != 1 {
		t.Fatalf("Persist called %d times, want 1", rec.count())
	}
}

func TestInitUserPlaceholderNeverClaims(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})

	if a.Verify(InitPlaceholder, "pw") {
		t.Fatal("user init accepted as a claim")
	}
	if rec.count() != 0 {
		t.Fatalf("init user consumed the init (Persist called %d times)", rec.count())
	}
	if !a.Verify("real", "pw") {
		t.Fatal("real user rejected after init-user attempt")
	}
	if login, _ := rec.last(); login != "real" {
		t.Fatalf("persisted login = %q", login)
	}

	// Same guard on the login-init + hash flow.
	hash := validHashFor(t, "hunter2")
	rec2 := &persistRecorder{}
	a2 := mustNew(t, Options{Login: InitPlaceholder, Password: hash, Persist: rec2.persist})
	if a2.Verify(InitPlaceholder, "hunter2") {
		t.Fatal("user init accepted by claimLogin")
	}
	if rec2.count() != 0 {
		t.Fatal("init user claimed the login")
	}
	if !a2.Verify("alice", "hunter2") {
		t.Fatal("real user rejected after init-user attempt on claimLogin")
	}
	if login, stored := rec2.last(); login != "alice" || stored != hash {
		t.Fatalf("persisted (%q, hash ok: %v)", login, stored == hash)
	}
}

func TestInvalidClaimedLoginsRejected(t *testing.T) {
	setFastCost(t)
	cases := []struct {
		name string
		user string
	}{
		{"leading space", " alice"},
		{"trailing space", "alice "},
		{"embedded newline", "ali\nce"},
		{"embedded carriage return", "ali\rce"},
		{"control char", "ali\x00ce"},
		{"empty after init", ""},
		{"too long", strings.Repeat("u", 257)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &persistRecorder{}
			a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})
			if a.Verify(tc.user, "pw") {
				t.Fatal("invalid login accepted")
			}
			if rec.count() != 0 {
				t.Fatalf("invalid login persisted (count %d)", rec.count())
			}
			if users := a.Users(); users == nil || len(users) != 0 {
				t.Fatalf("Users = %v, want still empty", users)
			}
		})
	}
	// A 256-byte login is the accepted maximum.
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})
	maxLogin := strings.Repeat("u", 256)
	if !a.Verify(maxLogin, "pw") {
		t.Fatal("256-byte login rejected")
	}
	if login, _ := rec.last(); login != maxLogin {
		t.Fatal("max-length login not persisted")
	}
}

func TestInitBothFlow(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})

	if a.Verify("", "pw") || a.Verify("user", "") {
		t.Fatal("empty credential parts accepted during init")
	}
	if users := a.Users(); users == nil || len(users) != 0 {
		t.Fatalf("Users during init = %v, want empty non-nil", users)
	}
	if !a.Verify("first-user", "first-secret") {
		t.Fatal("init attempt rejected")
	}
	if rec.count() != 1 {
		t.Fatalf("Persist called %d times, want 1", rec.count())
	}
	login, hash := rec.last()
	if login != "first-user" {
		t.Fatalf("persisted login = %q", login)
	}
	if ok, err := VerifyHash(hash, "first-secret"); err != nil || !ok {
		t.Fatalf("persisted hash does not verify: %v %v", ok, err)
	}
	if a.Verify("second-user", "first-secret") {
		t.Fatal("a second user is accepted after init")
	}
}

func TestInitBothPersistFailureAndRetry(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{err: errors.New("disk full")}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})

	if a.Verify("first-user", "first-secret") {
		t.Fatal("attempt accepted despite Persist failure")
	}
	if users := a.Users(); users == nil || len(users) != 0 {
		t.Fatalf("state did not stay init: Users = %v", users)
	}
	rec.err = nil
	if !a.Verify("first-user", "first-secret") {
		t.Fatal("good attempt rejected after failed persist")
	}
	if rec.count() != 2 {
		t.Fatalf("Persist called %d times, want 2", rec.count())
	}
}

func TestInitLoginFlow(t *testing.T) {
	setFastCost(t)
	hash := validHashFor(t, "hunter2")
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: hash, Persist: rec.persist})

	if a.Verify("alice", "wrong") {
		t.Fatal("wrong password accepted while login is init")
	}
	if rec.count() != 0 {
		t.Fatal("wrong password claimed the login")
	}
	if !a.Verify("alice", "hunter2") {
		t.Fatal("verifying password rejected while login is init")
	}
	if rec.count() != 1 {
		t.Fatalf("Persist called %d times, want 1", rec.count())
	}
	login, storedHash := rec.last()
	if login != "alice" || storedHash != hash {
		t.Fatalf("persisted (%q, hash-ok=%v), want (alice, same hash)", login, storedHash == hash)
	}
	if users := a.Users(); !reflect.DeepEqual(users, []string{"alice"}) {
		t.Fatalf("Users after claim = %v", users)
	}
	if a.Verify("bob", "hunter2") {
		t.Fatal("second user accepted after login was claimed")
	}
	if a.Verify("alice", "wrong") {
		t.Fatal("wrong password accepted after login was claimed")
	}
}

func TestInitLoginPersistFailureAndRetry(t *testing.T) {
	setFastCost(t)
	hash := validHashFor(t, "hunter2")
	rec := &persistRecorder{err: errors.New("disk full")}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: hash, Persist: rec.persist})

	if a.Verify("alice", "hunter2") {
		t.Fatal("claim accepted despite Persist failure")
	}
	if users := a.Users(); users == nil || len(users) != 0 {
		t.Fatalf("login was claimed despite Persist failure: Users = %v", users)
	}
	rec.err = nil
	if !a.Verify("alice", "hunter2") {
		t.Fatal("good claim rejected after failed persist")
	}
	if rec.count() != 2 {
		t.Fatalf("Persist called %d times, want 2", rec.count())
	}
	login, storedHash := rec.last()
	if login != "alice" || storedHash != hash {
		t.Fatalf("persisted (%q, hash-ok=%v)", login, storedHash == hash)
	}
}

func TestConcurrentFirstUse(t *testing.T) {
	setFastCost(t)

	t.Run("same credentials race for the init", func(t *testing.T) {
		rec := &persistRecorder{}
		a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})
		const n = 32
		var accepted int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if a.Verify("racer", "secret") {
					atomic.AddInt32(&accepted, 1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := atomic.LoadInt32(&accepted); got != n {
			t.Fatalf("accepted %d/%d verifies with the winning credentials", got, n)
		}
		if rec.count() != 1 {
			t.Fatalf("Persist called %d times, want exactly 1", rec.count())
		}
	})

	t.Run("distinct pairs race for the both-init, exactly one wins", func(t *testing.T) {
		rec := &persistRecorder{}
		a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})
		const n = 32
		type cred struct{ user, pass string }
		pairs := make([]cred, n)
		for i := range pairs {
			pairs[i] = cred{fmt.Sprintf("u%d", i), fmt.Sprintf("p%d", i)}
		}
		var accepted int32
		var winner int32 = -1
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if a.Verify(pairs[i].user, pairs[i].pass) {
					atomic.AddInt32(&accepted, 1)
					atomic.StoreInt32(&winner, int32(i))
				}
			}(i)
		}
		close(start)
		wg.Wait()
		if got := atomic.LoadInt32(&accepted); got != 1 {
			t.Fatalf("accepted %d verifies, want exactly 1", got)
		}
		if rec.count() != 1 {
			t.Fatalf("Persist called %d times, want exactly 1", rec.count())
		}
		w := atomic.LoadInt32(&winner)
		if w < 0 {
			t.Fatal("no winner recorded")
		}
		if login, _ := rec.last(); login != pairs[w].user {
			t.Fatalf("persisted login %q, want winner %q", login, pairs[w].user)
		}
		// Afterwards only the winner's pair verifies.
		for i, p := range pairs {
			want := i == int(w)
			if got := a.Verify(p.user, p.pass); got != want {
				t.Fatalf("pair %d verifies = %v, want %v", i, got, want)
			}
		}
	})

	t.Run("distinct users race for the login claim, exactly one wins", func(t *testing.T) {
		hash := validHashFor(t, "hunter2")
		rec := &persistRecorder{}
		a := mustNew(t, Options{Login: InitPlaceholder, Password: hash, Persist: rec.persist})
		const n = 32
		var accepted int32
		var winner int32 = -1
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				user := fmt.Sprintf("u%d", i)
				if a.Verify(user, "hunter2") {
					atomic.AddInt32(&accepted, 1)
					atomic.StoreInt32(&winner, int32(i))
				}
			}(i)
		}
		close(start)
		wg.Wait()
		if got := atomic.LoadInt32(&accepted); got != 1 {
			t.Fatalf("accepted %d verifies, want exactly 1", got)
		}
		if rec.count() != 1 {
			t.Fatalf("Persist called %d times, want exactly 1", rec.count())
		}
		w := atomic.LoadInt32(&winner)
		login, storedHash := rec.last()
		if login != fmt.Sprintf("u%d", w) || storedHash != hash {
			t.Fatalf("persisted (%q, hash ok: %v), want winner u%d", login, storedHash == hash, w)
		}
		// Distinct users: everyone else is rejected afterwards.
		for i := 0; i < n; i++ {
			if i == int(w) {
				continue
			}
			if a.Verify(fmt.Sprintf("u%d", i), "hunter2") {
				t.Fatalf("loser u%d accepted after the claim", i)
			}
		}
	})
}

func TestUsersBehavior(t *testing.T) {
	setFastCost(t)
	rec := &persistRecorder{}
	a := mustNew(t, Options{Login: InitPlaceholder, Password: InitPlaceholder, Persist: rec.persist})
	users := a.Users()
	if users == nil {
		t.Fatal("Users is nil during init, want empty non-nil slice")
	}
	if len(users) != 0 {
		t.Fatalf("Users during init = %v", users)
	}
	a.Verify("zed", "pw")
	if users := a.Users(); !reflect.DeepEqual(users, []string{"zed"}) {
		t.Fatalf("Users after init = %v", users)
	}
}
