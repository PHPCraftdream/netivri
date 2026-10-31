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

// chainProxy parses an effective chain hop URI (socks5://, http:// for an HTTP
// CONNECT proxy, https:// for one reached over TLS) and renders its Mihomo
// proxy mapping.
func chainProxy(raw, name string) (map[string]any, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nodeInvalid("invalid chain proxy URI")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "socks5" && scheme != "http" && scheme != "https") || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, nodeInvalid("invalid chain proxy URI")
	}
	port := u.Port()
	if port == "" {
		return nil, nodeInvalid("invalid chain proxy URI")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return nil, nodeInvalid("invalid chain proxy URI")
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, nodeInvalid("invalid chain proxy URI")
	}
	m := map[string]any{"name": name, "type": scheme, "server": u.Hostname(), "port": port}
	if scheme == "https" {
		m["type"], m["tls"] = "http", true
	}
	if u.User != nil {
		m["username"] = u.User.Username()
		if password, ok := u.User.Password(); ok {
			m["password"] = password
		}
	}
	return m, nil
}
