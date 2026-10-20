package auth

import (
	"crypto/subtle"
	"runtime"
	"sort"
	"sync"

	"github.com/PHPCraftdream/netivri/internal/cli"
	mihauth "github.com/metacubex/mihomo/component/auth"
	"golang.org/x/sync/singleflight"
)

// AccountSpec configures a named account. Password is an argon2id PHC hash
// or InitPlaceholder.
type AccountSpec struct {
	Login    string
	Password string
}

// MultiOptions configures named accounts and the optional legacy credential.
type MultiOptions struct {
	Legacy         Options
	Accounts       []AccountSpec
	PersistAccount func(login, passwordHash string) error
}

type multiAuthenticator struct {
	accounts map[string]*accountState
	legacy   mihauth.Authenticator
	slots    chan struct{}
	sf       singleflight.Group
}

type accountState struct {
	mu      sync.Mutex
	login   string
	hash    string
	init    bool
	cached  string
	persist func(string, string) error
}

// NewMulti constructs an authenticator from named accounts and an optional
// legacy credential. It returns nil when no credentials are configured.
// Configuration errors are generic CONFIG_INVALID errors that do not expose
// supplied logins, passwords, or hashes.
func NewMulti(o MultiOptions) (mihauth.Authenticator, error) {
	invalid := func() (mihauth.Authenticator, error) {
		return nil, &cli.Error{
			Code:    "CONFIG_INVALID",
			Message: "invalid authentication configuration",
			Exit:    cli.ExitUsage,
		}
	}
	if len(o.Accounts) == 0 && o.Legacy.Login == "" && o.Legacy.Password == "" {
		return nil, nil
	}
	legacy, err := New(o.Legacy)
	if err != nil {
		return invalid()
	}
	m := &multiAuthenticator{
		accounts: make(map[string]*accountState),
		legacy:   legacy,
		slots:    make(chan struct{}, min(4, runtime.GOMAXPROCS(0))),
	}
	for _, spec := range o.Accounts {
		if !validAccountLogin(spec.Login) || spec.Password == "" {
			return invalid()
		}
		if _, exists := m.accounts[spec.Login]; exists {
			return invalid()
		}
		if spec.Password == InitPlaceholder {
			if o.PersistAccount == nil {
				return invalid()
			}
			m.accounts[spec.Login] = &accountState{
				login:   spec.Login,
				init:    true,
				persist: o.PersistAccount,
			}
		} else {
			if _, e := parseHash(spec.Password); e != nil {
				return invalid()
			}
			m.accounts[spec.Login] = &accountState{
				login: spec.Login,
				hash:  spec.Password,
			}
		}
	}
	if legacyAuth, ok := legacy.(*authenticator); ok {
		legacyAuth.mu.Lock()
		legacyAuth.sharedSlots = m.slots
		if !legacyAuth.loginIsInit {
			if _, collision := m.accounts[legacyAuth.login]; collision {
				legacyAuth.mu.Unlock()
				return invalid()
			}
		}
		legacyAuth.mu.Unlock()
	}
	return m, nil
}

func validAccountLogin(s string) bool {
	// Keep this alphabet and length in sync with config.ValidAccountLogin.
	if len(s) < 1 || len(s) > 64 || s == InitPlaceholder {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '@' || c == '-') {
			return false
		}
	}
	return true
}

func (m *multiAuthenticator) Verify(user, pass string) bool {
	if user == "" || pass == "" || len(pass) > maxPasswordLen {
		return false
	}
	a := m.accounts[user]
	if a != nil && ctEqual(user, a.login) {
		a.mu.Lock()
		if a.init {
			m.slots <- struct{}{}
			h, err := Hash(pass)
			<-m.slots
			if err != nil || a.persist == nil || a.persist(a.login, h) != nil {
				a.mu.Unlock()
				return false
			}
			a.hash, a.cached, a.init = h, pass, false
			a.mu.Unlock()
			return true
		}
		if a.cached != "" {
			ok := subtle.ConstantTimeCompare([]byte(a.cached), []byte(pass)) == 1
			a.mu.Unlock()
			return ok
		}
		hash := a.hash
		a.mu.Unlock()
		// A burst of first verifications of one credential runs
		// argon2 once: the key contains login and password, so
		// different passwords never share a result.
		v, err, _ := m.sf.Do(user+"\x00"+pass, func() (any, error) {
			// A flight that starts after another one finished finds the cache.
			a.mu.Lock()
			if a.cached != "" {
				ok := subtle.ConstantTimeCompare([]byte(a.cached), []byte(pass)) == 1
				a.mu.Unlock()
				return ok, nil
			}
			a.mu.Unlock()
			m.slots <- struct{}{}
			ok, err := verifyHash(hash, pass)
			<-m.slots
			if err != nil || !ok {
				return false, err
			}
			a.mu.Lock()
			a.cached = pass
			a.mu.Unlock()
			return true, nil
		})
		ok, _ := v.(bool)
		return err == nil && ok
	}
	if m.legacy == nil {
		return false
	}
	return m.legacy.Verify(user, pass)
}

func (m *multiAuthenticator) Users() []string {
	users := make([]string, 0, len(m.accounts))
	for login := range m.accounts {
		users = append(users, login)
	}
	if m.legacy != nil {
		users = append(users, m.legacy.Users()...)
	}
	sort.Strings(users)
	return users
}

var _ mihauth.Authenticator = (*multiAuthenticator)(nil)
