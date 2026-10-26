package core

import (
	"net/url"
	"strconv"
	"strings"
)

func isNoChain(value string) bool { return strings.EqualFold(value, "none") }

// explicitNoChain reports whether m holds the value none for key.
func explicitNoChain(m map[string]string, key string) bool {
	v, ok := m[key]
	return ok && isNoChain(v)
}

// chainProxy parses an effective SOCKS5 URI and renders its Mihomo proxy mapping.
func chainProxy(raw, name string) (map[string]any, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "socks5") || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, nodeInvalid("invalid SOCKS5 chain URI")
	}
	port := u.Port()
	if port == "" {
		return nil, nodeInvalid("invalid SOCKS5 chain URI")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return nil, nodeInvalid("invalid SOCKS5 chain URI")
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, nodeInvalid("invalid SOCKS5 chain URI")
	}
	m := map[string]any{"name": name, "type": "socks5", "server": u.Hostname(), "port": port}
	if u.User != nil {
		m["username"] = u.User.Username()
		if password, ok := u.User.Password(); ok {
			m["password"] = password
		}
	}
	return m, nil
}
