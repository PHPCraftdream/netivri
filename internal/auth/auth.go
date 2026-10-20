package auth

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"unicode"

	mihauth "github.com/metacubex/mihomo/component/auth"
	"golang.org/x/sync/singleflight"
)

// maxPasswordLen bounds argon2 input cost from hostile clients.
const maxPasswordLen = 1024

// verifyHash is the package verification seam; tests replace it to
// observe and gate argon2 work.
var verifyHash = VerifyHash

// Options configures the proxy authenticator. Persist must be set
// whenever a placeholder is configured; it is called exactly once when
// the init placeholder is consumed and must store
// (login, passwordHash) in the configuration file.
type Options struct {
	Login    string
	Password string
	Persist  func(login, passwordHash string) error
}

// authenticator implements mihauth.Authenticator.
type authenticator struct {
	mu          sync.Mutex
	login       string // literal login, or "init" while loginIsInit
	loginIsInit bool
	hash        string // PHC hash, or "" while hashIsInit
	hashIsInit  bool
	cachedPass  string // plaintext after the first success; empty before
	persist     func(login, passwordHash string) error
	slots       chan struct{}
	sharedSlots chan struct{}
	sf          singleflight.Group
}

// New returns a Mihomo Authenticator. When Login and Password are both
// empty, auth is disabled and a true nil interface is returned.
// A non-empty password must be "init" or a parseable argon2id PHC hash
// (parsed eagerly here to fail early); login and password must be set
// together; a placeholder requires Persist. Error messages never
// contain credentials.
func New(o Options) (mihauth.Authenticator, error) {
	if o.Login == "" && o.Password == "" {
		return nil, nil
	}
	if o.Login == "" {
		return nil, errors.New("auth: password requires a login")
	}
	if o.Password == "" {
		return nil, errors.New("auth: login requires a password")
	}
	hasPlaceholder := o.Login == InitPlaceholder || o.Password == InitPlaceholder
	if hasPlaceholder && o.Persist == nil {
		return nil, errors.New("auth: init placeholder requires a persist callback")
	}
	a := &authenticator{
		login:       o.Login,
		loginIsInit: o.Login == InitPlaceholder,
		persist:     o.Persist,
		slots:       make(chan struct{}, min(4, runtime.GOMAXPROCS(0))),
	}
	if o.Password == InitPlaceholder {
		a.hashIsInit = true
	} else {
		if _, err := parseHash(o.Password); err != nil {
			return nil, err
		}
		a.hash = o.Password
	}
	return a, nil
}

// Verify implements mihauth.Authenticator. Empty user or pass is
// always rejected (SOCKS4 sends no password; an empty password must
// never match). The first-use init transition happens under the
// mutex, so exactly one goroutine wins under concurrency.
func (a *authenticator) Verify(user, pass string) bool {
	if user == "" || pass == "" {
		return false
	}
	if len(pass) > maxPasswordLen {
		return false
	}

	a.mu.Lock()
	hashIsInit := a.hashIsInit
	login := a.login
	loginIsInit := a.loginIsInit
	cached := a.cachedPass
	a.mu.Unlock()

	if hashIsInit {
		return a.consumeInit(user, pass)
	}

	if loginIsInit {
		// Login still "init", password a hash: the first call whose
		// password verifies claims the login.
		ok, err := a.verifyWithSlot(a.hash, pass)
		if err != nil || !ok {
			return false
		}
		return a.claimLogin(user, pass)
	}

	// Established credential: wrong user is rejected without running
	// argon2 (cost asymmetry — guessing a username must not burn 19 MiB
	// per attempt); the correct user goes through the fast cache first.
	if !ctEqual(user, login) {
		return false
	}
	if cached != "" {
		return ctEqual(cached, pass)
	}
	// A burst of first verifications of the same credential shares one
	// argon2 run; the key contains the password, so different
	// passwords never share a result.
	v, err, _ := a.sf.Do(login+"\x00"+pass, func() (any, error) {
		// A flight that starts after another one finished finds the cache.
		a.mu.Lock()
		if a.cachedPass != "" {
			ok := ctEqual(a.cachedPass, pass)
			a.mu.Unlock()
			return ok, nil
		}
		a.mu.Unlock()
		ok, err := a.verifyWithSlot(a.hash, pass)
		if err != nil || !ok {
			return false, err
		}
		a.mu.Lock()
		a.cachedPass = pass
		a.mu.Unlock()
		return true, nil
	})
	ok, _ := v.(bool)
	return err == nil && ok
}

// Users implements mihauth.Authenticator: the login once known (it is
// already known when only the password is the placeholder), an empty
// (non-nil) slice only while the login itself is still "init".
func (a *authenticator) Users() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loginIsInit {
		return []string{}
	}
	return []string{a.login}
}

// verifyWithSlot runs argon2 under the concurrency semaphore so that
// parallel failed attempts cannot each allocate the full memory cost.
func (a *authenticator) verifyWithSlot(hash, pass string) (bool, error) {
	slots := a.slots
	if a.sharedSlots != nil {
		slots = a.sharedSlots
	}
	slots <- struct{}{}
	defer func() { <-slots }()
	return verifyHash(hash, pass)
}

// consumeInit handles password == "init" (login literal or "init").
// The whole transition (hash + persist + state update) runs under the
// mutex, so exactly one attempt consumes the init; a Persist failure
// leaves the state "init" (fail closed) and the attempt is rejected.
// The standalone New path keeps its historical serialization; NewMulti supplies
// a shared semaphore so its legacy init hashing is bounded with account work.
func (a *authenticator) consumeInit(user, pass string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.hashIsInit {
		// Lost the init race: accept only the winning credentials.
		return a.login == user && a.cachedPass != "" && ctEqual(a.cachedPass, pass)
	}
	// A wrong user never consumes the init and never reaches argon2.
	if !a.loginIsInit && !ctEqual(user, a.login) {
		return false
	}
	// user "init" must never become the stored login: on the next
	// start config would treat it as the placeholder again and let
	// anyone re-claim it.
	if a.loginIsInit && user == InitPlaceholder {
		return false
	}
	if !validClaimedLogin(user) {
		return false
	}
	if a.persist == nil {
		return false
	}
	if a.sharedSlots != nil {
		a.sharedSlots <- struct{}{}
	}
	h, err := Hash(pass)
	if a.sharedSlots != nil {
		<-a.sharedSlots
	}
	if err != nil {
		return false
	}
	if err := a.persist(user, h); err != nil {
		return false
	}
	a.login, a.loginIsInit = user, false
	a.hash, a.hashIsInit = h, false
	a.cachedPass = pass
	return true
}

// claimLogin handles login == "init" with a known hash: after the
// password has verified, the caller's user becomes the login.
func (a *authenticator) claimLogin(user, pass string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loginIsInit {
		// Lost the race: only the winner's user is accepted.
		return a.login == user
	}
	// The literal "init" must never be claimed as a login (see
	// consumeInit).
	if user == InitPlaceholder {
		return false
	}
	if !validClaimedLogin(user) {
		return false
	}
	if a.persist == nil {
		return false
	}
	if err := a.persist(user, a.hash); err != nil {
		return false
	}
	a.login, a.loginIsInit = user, false
	a.cachedPass = pass
	return true
}

// validClaimedLogin accepts only logins that survive a config
// rewrite: 1..256 bytes, no leading/trailing whitespace, no control
// characters or line breaks.
func validClaimedLogin(login string) bool {
	if len(login) == 0 || len(login) > 256 {
		return false
	}
	if strings.TrimSpace(login) != login {
		return false
	}
	for _, r := range login {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
