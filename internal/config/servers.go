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
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func validateServers(c *Config) error {
	if c == nil {
		return invalid("configuration must not be nil")
	}
	names := map[string]int{}
	// A TCP and a UDP server may share a port number: the check is per transport.
	type endpointKey struct {
		port int
		udp  bool
		bind string
	}
	type endpoint struct {
		bind  string
		index int
	}
	ports := map[endpointKey][]endpoint{}
	for i := range c.Servers {
		s := &c.Servers[i]
		field := func(k string) string { return fmt.Sprintf("servers[%d].%s", i, k) }
		if !validServerName(s.Name) {
			return invalid("%s must be 1 to 32 letters, digits, dots, underscores or hyphens", field("name"))
		}
		if j, ok := names[s.Name]; ok {
			return invalid("%s duplicates servers[%d].name", field("name"), j)
		}
		names[s.Name] = i
		p, ok := servers.Lookup(s.Protocol)
		if !ok {
			return invalid("%s is not a registered server protocol", field("protocol"))
		}
		if s.Default {
			tls, trial := servers.ShareMode(servers.Spec{Protocol: s.Protocol, Params: s.Params})
			if !tls {
				if trial {
					return servers.ValidateSharedPort([]servers.Spec{{Name: s.Name, Protocol: s.Protocol, Bind: s.Bind, Port: s.Port, Default: s.Default, Params: s.Params}})
				}
				return invalid("%s default requires a TLS-sniffable protocol", field("default"))
			}
		}
		bind := s.Bind
		if bind == "" {
			bind = "0.0.0.0"
		}
		if net.ParseIP(bind) == nil {
			return invalid("%s must be an IP address", field("bind"))
		}
		if s.Port < 1 || s.Port > 65535 {
			return invalid("%s must be between 1 and 65535", field("port"))
		}
		udp := servers.IsUDPBound(p)
		if !udp && s.Port == c.Listen.Port && bindsOverlap(bind, c.Listen.Bind) {
			return invalid("%s conflicts with listen.port", field("port"))
		}
		key := endpointKey{s.Port, udp, bind}
		for priorKey, entries := range ports {
			if priorKey.port != s.Port || priorKey.udp != udp {
				continue
			}
			for _, prior := range entries {
				if !bindsOverlap(bind, prior.bind) {
					continue
				}
				if bind != prior.bind {
					return invalid("%s duplicates servers[%d] endpoint", field("port"), prior.index)
				}
				prev := &c.Servers[prior.index]
				tlsNew, trialNew := servers.ShareMode(servers.Spec{Protocol: s.Protocol, Params: s.Params})
				tlsPrev, trialPrev := servers.ShareMode(servers.Spec{Protocol: prev.Protocol, Params: prev.Params})
				if !((tlsNew || trialNew) && (tlsPrev || trialPrev)) {
					return invalid("%s duplicates servers[%d] endpoint", field("port"), prior.index)
				}
			}
		}
		ports[key] = append(ports[key], endpoint{bind, i})
		if s.Accounts == "" {
			return invalid("%s must be a comma-separated account list or *", field("accounts"))
		}
		if s.Accounts == "*" {
			if p.SingleUser() {
				return invalid("%s cannot be * for this protocol", field("accounts"))
			}
			if len(c.Accounts) == 0 && (c.Auth.Login == "" || c.Auth.Login == InitPlaceholder) {
				return invalid("%s requires at least one configured account", field("accounts"))
			}
		} else {
			seenAccounts := map[string]bool{}
			for _, login := range strings.Split(s.Accounts, ",") {
				if !ValidAccountLogin(login) || login == InitPlaceholder {
					return invalid("%s contains an invalid account login", field("accounts"))
				}
				if seenAccounts[login] {
					return invalid("%s contains duplicate accounts", field("accounts"))
				}
				seenAccounts[login] = true
			}
			if p.SingleUser() && len(seenAccounts) != 1 {
				return invalid("%s requires exactly one account for this protocol", field("accounts"))
			}
		}
		if err := checkScalar(field("public_host"), s.PublicHost); err != nil {
			return err
		}
		for k, v := range s.Params {
			if k == "" || strings.ContainsRune(k, '.') || strings.TrimSpace(k) != k {
				return invalid("%s contains an invalid protocol parameter key", field("params"))
			}
			switch k {
			case "name", "protocol", "bind", "port", "accounts", "public_host", "default":
				return invalid("%s is reserved", field(k))
			}
			if err := checkScalar(field(k), v); err != nil {
				return err
			}
		}
		if err := p.Validate(s.Params); err != nil {
			if key := parseProtocolErrorKey(err, s.Params); key != "" {
				return invalid("%s is invalid", field(key))
			}
			return invalid("%s contains an invalid protocol parameter", field("params"))
		}
	}
	// Validate complete endpoint groups so fallback/default and account limits
	// are evaluated consistently rather than pairwise.
	groups := map[endpointKey][]servers.Spec{}
	for _, s := range c.Servers {
		p, _ := servers.Lookup(s.Protocol)
		udp := servers.IsUDPBound(p)
		bind := s.Bind
		if bind == "" {
			bind = "0.0.0.0"
		}
		accounts := []string{}
		if s.Accounts == "*" {
			for _, a := range c.Accounts {
				accounts = append(accounts, a.Login)
			}
			if c.Auth.Login != "" && c.Auth.Login != InitPlaceholder {
				accounts = append(accounts, c.Auth.Login)
			}
		} else {
			accounts = strings.Split(s.Accounts, ",")
		}
		key := endpointKey{s.Port, udp, bind}
		groups[key] = append(groups[key], servers.Spec{Name: s.Name, Protocol: s.Protocol, Bind: bind, Port: s.Port, Default: s.Default, Params: s.Params, Accounts: accounts})
	}
	for _, group := range groups {
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
func hasSniffer(p servers.Protocol) bool { _, ok := p.(servers.Sniffer); return ok }
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
