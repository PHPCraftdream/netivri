package servers

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

// SecretMap stores server-wide secrets. Formatting always redacts its contents.
type SecretMap map[string]string

// UserSecrets maps account logins to secret fields, while remaining assignable
// from ordinary map[string]map[string]string values.
type UserSecrets map[string]map[string]string

func (SecretMap) String() string     { return "<redacted>" }
func (SecretMap) GoString() string   { return "<redacted>" }
func (UserSecrets) String() string   { return "<redacted>" }
func (UserSecrets) GoString() string { return "<redacted>" }

// Spec is a resolved endpoint configuration. All parameter and secret maps are
// redacted by String and GoString.
type Spec struct {
	Name, Protocol, Bind string
	Port                 int
	Accounts             []string
	Params               map[string]string
	Material             SecretMap
	Users                UserSecrets
}

func (s Spec) String() string {
	return fmt.Sprintf("{Name:%q Protocol:%q Bind:%q Port:%d Accounts:%q Params:<redacted> Material:%s Users:%s}", s.Name, s.Protocol, s.Bind, s.Port, s.Accounts, SecretMap{}.String(), UserSecrets{}.String())
}

func (s Spec) GoString() string { return s.String() }

// ListenAddress returns the endpoint, applying the public bind default when empty.
func (s Spec) ListenAddress() string {
	bind := s.Bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	return net.JoinHostPort(bind, fmt.Sprint(s.Port))
}

// Clone returns a deep copy of the spec and its maps/slices.
func (s Spec) Clone() Spec {
	t := s
	t.Accounts = append([]string(nil), s.Accounts...)
	t.Params = cloneMap(s.Params)
	t.Material = SecretMap(cloneMap(s.Material))
	t.Users = make(UserSecrets, len(s.Users))
	for login, secrets := range s.Users {
		t.Users[login] = cloneMap(secrets)
	}
	return t
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Equal compares all public configuration and secret fields without formatting them.
func (s Spec) Equal(other Spec) bool {
	return s.Name == other.Name && s.Protocol == other.Protocol && s.Bind == other.Bind && s.Port == other.Port && equalStrings(s.Accounts, other.Accounts) && equalMap(s.Params, other.Params) && equalMap(s.Material, other.Material) && equalUsers(s.Users, other.Users)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		other, ok := b[k]
		if !ok || other != v {
			return false
		}
	}
	return true
}

func equalUsers(a, b UserSecrets) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		other, ok := b[k]
		if !ok || !equalMap(v, other) {
			return false
		}
	}
	return true
}

var serverNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
var accountLoginRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Validate checks resolved endpoint fields and protocol-specific parameters.
func (s Spec) Validate() error {
	if !serverNameRE.MatchString(s.Name) {
		return errors.New("invalid server name")
	}
	p, ok := Lookup(s.Protocol)
	if !ok {
		return errors.New("unknown server protocol")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("invalid server port")
	}
	if s.Bind != "" && net.ParseIP(s.Bind) == nil {
		return errors.New("invalid server bind")
	}
	if len(s.Accounts) == 0 {
		return errors.New("server accounts are required")
	}
	seen := make(map[string]bool, len(s.Accounts))
	for _, account := range s.Accounts {
		if !accountLoginRE.MatchString(account) || account == "*" || seen[account] {
			return errors.New("invalid server accounts")
		}
		seen[account] = true
	}
	if p.SingleUser() && len(s.Accounts) != 1 {
		return errors.New("protocol requires one account")
	}
	if err := p.Validate(s.Params); err != nil {
		return fmt.Errorf("invalid server parameters: %w", err)
	}
	return nil
}

// Protocol is the contract implemented by each server backend. Start receives
// its listener through Env.ListenConfig. Protocols implementing UDPBound with
// UDPBound() == true must use ListenPacket for their UDP bind; others are treated
// as TCP-bound and use Listen.
type Protocol interface {
	Name() string
	SingleUser() bool
	Validate(params map[string]string) error
	NewMaterial(params map[string]string) (map[string]string, error)
	NewSecret(login string, params map[string]string) (map[string]string, error)
	Start(spec Spec, env Env) (io.Closer, error)
	Link(spec Spec, login, host string) (string, error)
}

// Env contains the filtered Mihomo tunnel and listener abstraction provided to
// Protocol.Start. TCP-bound protocols use Listen; UDP-bound protocols use
// ListenPacket to bind their real UDP socket.
type Env struct {
	Tunnel       C.Tunnel
	ListenConfig C.InboundListenConfig
}

// Running describes a started listener and owns its closer.
type Running struct {
	Name, Protocol, Listen string
	Accounts               []string
	Closer                 io.Closer
}

// Close safely closes a running listener; a nil receiver or closer is a no-op.
func (r *Running) Close() error {
	if r == nil || r.Closer == nil {
		return nil
	}
	return r.Closer.Close()
}

// Info is the non-secret status view of a running endpoint.
type Info struct {
	Name, Protocol, Listen string
	Accounts               []string
	Connections            int
	DroppedClients         uint64
}

// UDPBound is an optional protocol capability that reports whether Start binds UDP.
// When true, Start must bind using env.ListenConfig.ListenPacket with a real UDP
// network (udp, udp4, or udp6); Listen is rejected for that protocol. A false
// result, or not implementing UDPBound, is treated as TCP-bound and uses the
// staged TCP listener.
//
// When a changed UDP-bound server is restarted, the old server's Close is called
// before the new server starts. This close-old-first behavior can leave a brief
// gap; existing connections or datagrams may end. Close must release the UDP
// port before it returns.
type UDPBound interface {
	UDPBound() bool
}

// MihomoLinker is an optional interface for protocols that can emit YAML snippets.
type MihomoLinker interface {
	Mihomo(spec Spec, login, host string) (string, error)
}

var registryMu sync.RWMutex
var registry = map[string]Protocol{}

// Register adds a protocol implementation and panics for invalid or duplicate names.
func Register(p Protocol) {
	if p == nil || strings.TrimSpace(p.Name()) == "" {
		panic("servers: invalid protocol")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[p.Name()]; ok {
		panic("servers: duplicate protocol " + p.Name())
	}
	registry[p.Name()] = p
}

// Lookup finds a protocol by its canonical registered name.
func Lookup(name string) (Protocol, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// Names returns registered protocol names sorted lexicographically.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Registered returns protocol implementations sorted by name.
func Registered() []Protocol {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Protocol, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
