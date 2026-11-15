package config

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

// Server is a public listener configuration. Params contains protocol options,
// not credentials; server secrets are stored separately.
type Server struct {
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"`
	Bind       string            `json:"bind,omitempty"`
	Port       int               `json:"port"`
	Accounts   string            `json:"accounts"`
	PublicHost string            `json:"public_host,omitempty"`
	Default    bool              `json:"default,omitempty"`
	Params     map[string]string `json:"-"`
}

// GetServer returns a server and its index, or (nil, -1) when absent.
func (c *Config) GetServer(name string) (*Server, int) {
	for i := range c.Servers {
		if c.Servers[i].Name == name {
			return &c.Servers[i], i
		}
	}
	return nil, -1
}
func validServerName(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)
		if !ok {
			return false
		}
	}
	return true
}

// serverEndpointKey identifies a listener endpoint; a TCP and a UDP server may share a port number.
type serverEndpointKey struct {
	port int
	udp  bool
	bind string
}

// serverEndpoint is a registered listener: its bind address and server index.
type serverEndpoint struct {
	bind  string
	index int
}

func serverField(i int, k string) string {
	return fmt.Sprintf("servers[%d].%s", i, k)
}
func bindOrDefault(bind string) string {
	if bind == "" {
		return "0.0.0.0"
	}
	return bind
}
func validateServers(c *Config) error {
	if c == nil {
		return invalid("configuration must not be nil")
	}
	names := map[string]int{}
	ports := map[serverEndpointKey][]serverEndpoint{}
	for i := range c.Servers {
		if err := c.validateServer(i, names, ports); err != nil {
			return err
		}
	}
	return c.validateEndpointGroups()
}

// validateServer checks server i against the servers registered before it.
func (c *Config) validateServer(i int, names map[string]int, ports map[serverEndpointKey][]serverEndpoint) error {
	s := &c.Servers[i]
	p, err := validateServerIdentity(i, s, names)
	if err != nil {
		return err
	}
	if err := validateServerDefault(i, s); err != nil {
		return err
	}
	if err := c.validateServerEndpoint(i, s, p, ports); err != nil {
		return err
	}
	if err := c.validateServerAccounts(i, s, p); err != nil {
		return err
	}
	return validateServerParams(i, s, p)
}

// validateServerIdentity checks the name and protocol of server i and registers the name.
func validateServerIdentity(i int, s *Server, names map[string]int) (servers.Protocol, error) {
	if !validServerName(s.Name) {
		return nil, invalid("%s must be 1 to 32 letters, digits, dots, underscores or hyphens", serverField(i, "name"))
	}
	if j, ok := names[s.Name]; ok {
		return nil, invalid("%s duplicates servers[%d].name", serverField(i, "name"), j)
	}
	names[s.Name] = i
	p, ok := servers.Lookup(s.Protocol)
	if !ok {
		return nil, invalid("%s is not a registered server protocol", serverField(i, "protocol"))
	}
	return p, nil
}

// validateServerDefault requires a default server to be TLS-sniffable, or to pass the shared-port trial.
func validateServerDefault(i int, s *Server) error {
	if !s.Default {
		return nil
	}
	tls, trial := servers.ShareMode(servers.Spec{Protocol: s.Protocol, Params: s.Params})
	if tls {
		return nil
	}
	if trial {
		return servers.ValidateSharedPort([]servers.Spec{{Name: s.Name, Protocol: s.Protocol, Bind: s.Bind, Port: s.Port, Default: s.Default, Params: s.Params}})
	}
	return invalid("%s default requires a TLS-sniffable protocol", serverField(i, "default"))
}

// validateServerEndpoint checks the bind address and port of server i and registers its endpoint.
func (c *Config) validateServerEndpoint(i int, s *Server, p servers.Protocol, ports map[serverEndpointKey][]serverEndpoint) error {
	bind := bindOrDefault(s.Bind)
	if net.ParseIP(bind) == nil {
		return invalid("%s must be an IP address", serverField(i, "bind"))
	}
	if s.Port < 1 || s.Port > 65535 {
		return invalid("%s must be between 1 and 65535", serverField(i, "port"))
	}
	udp := servers.IsUDPBound(p)
	if !udp && s.Port == c.Listen.Port && bindsOverlap(bind, c.Listen.Bind) {
		return invalid("%s conflicts with listen.port", serverField(i, "port"))
	}
	if err := c.checkEndpointOverlap(i, s, bind, udp, ports); err != nil {
		return err
	}
	key := serverEndpointKey{s.Port, udp, bind}
	ports[key] = append(ports[key], serverEndpoint{bind, i})
	return nil
}

// checkEndpointOverlap rejects an endpoint that clashes with one already registered for the same port and transport.
func (c *Config) checkEndpointOverlap(i int, s *Server, bind string, udp bool, ports map[serverEndpointKey][]serverEndpoint) error {
	for priorKey, entries := range ports {
		if priorKey.port != s.Port || priorKey.udp != udp {
			continue
		}
		for _, prior := range entries {
			if !bindsOverlap(bind, prior.bind) {
				continue
			}
			if bind != prior.bind {
				return invalid("%s duplicates servers[%d] endpoint", serverField(i, "port"), prior.index)
			}
			prev := &c.Servers[prior.index]
			tlsNew, trialNew := servers.ShareMode(servers.Spec{Protocol: s.Protocol, Params: s.Params})
			tlsPrev, trialPrev := servers.ShareMode(servers.Spec{Protocol: prev.Protocol, Params: prev.Params})
			if (!tlsNew && !trialNew) || (!tlsPrev && !trialPrev) {
				return invalid("%s duplicates servers[%d] endpoint", serverField(i, "port"), prior.index)
			}
		}
	}
	return nil
}

// validateServerAccounts checks the accounts of server i: * or a list of distinct valid logins.
func (c *Config) validateServerAccounts(i int, s *Server, p servers.Protocol) error {
	if s.Accounts == "" {
		return invalid("%s must be a comma-separated account list or *", serverField(i, "accounts"))
	}
	if s.Accounts == "*" {
		if p.SingleUser() {
			return invalid("%s cannot be * for this protocol", serverField(i, "accounts"))
		}
		if len(c.Accounts) == 0 && (c.Auth.Login == "" || c.Auth.Login == InitPlaceholder) {
			return invalid("%s requires at least one configured account", serverField(i, "accounts"))
		}
		return nil
	}
	seenAccounts := map[string]bool{}
	for _, login := range strings.Split(s.Accounts, ",") {
		if !ValidAccountLogin(login) || login == InitPlaceholder {
			return invalid("%s contains an invalid account login", serverField(i, "accounts"))
		}
		if seenAccounts[login] {
			return invalid("%s contains duplicate accounts", serverField(i, "accounts"))
		}
		seenAccounts[login] = true
	}
	if p.SingleUser() && len(seenAccounts) != 1 {
		return invalid("%s requires exactly one account for this protocol", serverField(i, "accounts"))
	}
	return nil
}

// validateServerParams checks the public host and the protocol parameters of server i.
func validateServerParams(i int, s *Server, p servers.Protocol) error {
	if err := checkScalar(serverField(i, "public_host"), s.PublicHost); err != nil {
		return err
	}
	for k, v := range s.Params {
		if k == "" || strings.ContainsRune(k, '.') || strings.TrimSpace(k) != k {
			return invalid("%s contains an invalid protocol parameter key", serverField(i, "params"))
		}
		switch k {
		case "name", "protocol", "bind", "port", "accounts", "public_host", "default":
			return invalid("%s is reserved", serverField(i, k))
		}
		if err := checkScalar(serverField(i, k), v); err != nil {
			return err
		}
	}
	if err := p.Validate(s.Params); err != nil {
		if key := parseProtocolErrorKey(err, s.Params); key != "" {
			return invalid("%s is invalid", serverField(i, key))
		}
		return invalid("%s contains an invalid protocol parameter", serverField(i, "params"))
	}
	return nil
}

// validateEndpointGroups validates the servers of each endpoint together, so fallback/default and account limits are evaluated consistently rather than pairwise.
func (c *Config) validateEndpointGroups() error {
	groups := map[serverEndpointKey][]servers.Spec{}
	for _, s := range c.Servers {
		p, _ := servers.Lookup(s.Protocol)
		udp := servers.IsUDPBound(p)
		bind := bindOrDefault(s.Bind)
		key := serverEndpointKey{s.Port, udp, bind}
		groups[key] = append(groups[key], servers.Spec{Name: s.Name, Protocol: s.Protocol, Bind: bind, Port: s.Port, Default: s.Default, Params: s.Params, Accounts: c.specAccounts(s.Accounts)})
	}
	for _, group := range groups {
		if err := validateEndpointGroup(group); err != nil {
			return err
		}
	}
	return nil
}

// specAccounts expands a server's accounts field: * means every configured login, including auth.login.
func (c *Config) specAccounts(accounts string) []string {
	if accounts != "*" {
		return strings.Split(accounts, ",")
	}
	out := []string{}
	for _, a := range c.Accounts {
		out = append(out, a.Login)
	}
	if c.Auth.Login != "" && c.Auth.Login != InitPlaceholder {
		out = append(out, c.Auth.Login)
	}
	return out
}

// validateEndpointGroup validates each exact endpoint of one group separately.
func validateEndpointGroup(group []servers.Spec) error {
	// Validate each exact endpoint separately; overlap rules have already
	// rejected wildcard-vs-specific conflicting binds above.
	byBind := map[string][]servers.Spec{}
	for _, member := range group {
		byBind[member.Bind] = append(byBind[member.Bind], member)
	}
	for _, exact := range byBind {
		if err := servers.ValidateSharedPort(exact); err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "SERVER_PORT_AMBIGUOUS" {
				return err
			}
			if trialGroup(exact) {
				return err
			}
			return invalid("servers[1].port duplicates servers[0] endpoint")
		}
	}
	return nil
}
func trialGroup(specs []servers.Spec) bool {
	for _, spec := range specs {
		_, trial := servers.ShareMode(spec)
		if trial {
			return true
		}
	}
	return false
}
func bindsOverlap(a, b string) bool {
	if b == "" {
		b = "127.0.0.1"
	}
	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	if ipA == nil || ipB == nil {
		return false
	}
	return ipA.Equal(ipB) || (ipA.IsUnspecified() && (ipA.To4() == nil || ipB.To4() != nil)) || (ipB.IsUnspecified() && (ipB.To4() == nil || ipA.To4() != nil))
}

// ResolveServers expands wildcard accounts and verifies configured accounts before producing listener specs.
func (c *Config) ResolveServers() ([]servers.Spec, error) {
	if c == nil {
		return nil, invalid("configuration must not be nil")
	}
	out := make([]servers.Spec, 0, len(c.Servers))
	for i, s := range c.Servers {
		accounts := []string{}
		if s.Accounts == "*" {
			if p, ok := servers.Lookup(s.Protocol); ok && p.SingleUser() {
				return nil, invalid("servers[%d].accounts cannot be * for this protocol", i)
			}
			for _, a := range c.Accounts {
				accounts = append(accounts, a.Login)
			}
			if c.Auth.Login != "" && c.Auth.Login != InitPlaceholder {
				accounts = append(accounts, c.Auth.Login)
			}
		} else {
			accounts = strings.Split(s.Accounts, ",")
		}
		if len(accounts) == 0 {
			return nil, invalid("servers[%d].accounts resolves to no accounts", i)
		}
		seen := map[string]bool{}
		for _, account := range accounts {
			found := false
			for _, a := range c.Accounts {
				if a.Login == account {
					found = true
					break
				}
			}
			if c.Auth.Login == account && account != InitPlaceholder {
				found = true
			}
			if !found {
				return nil, invalid("servers[%d].accounts references an account that does not exist", i)
			}
			if seen[account] {
				return nil, invalid("servers[%d].accounts contains a duplicate account", i)
			}
			seen[account] = true
		}
		bind := s.Bind
		if bind == "" {
			bind = "0.0.0.0"
		}
		spec := servers.Spec{Name: s.Name, Protocol: s.Protocol, Bind: bind, Port: s.Port, Default: s.Default, Accounts: accounts, Params: s.Params}
		if err := spec.Validate(); err != nil {
			return nil, invalid("servers[%d] is invalid", i)
		}
		out = append(out, spec)
	}
	return out, nil
}
