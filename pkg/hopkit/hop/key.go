// Package hop provides canonical, secret-free identities for proxy hops.
package hop

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Key returns a SHA-256 key for canonical secret-free scheme://host:port; hashes keep identities opaque, non-URL, and easy to validate.
func Key(rawURL string) (string, bool) {
	if rawURL == "" || strings.TrimSpace(rawURL) != rawURL {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	defaultPort := ""
	switch scheme {
	case "socks5":
		defaultPort = "1080"
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	default:
		return "", false
	}
	if u.Host == "" {
		return "", false
	}

	host, port, ok := splitAuthority(u.Host)
	if !ok || host == "" {
		return "", false
	}
	host, ok = canonicalHost(host)
	if !ok {
		return "", false
	}
	if port == "" {
		port = defaultPort
	} else {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", false
		}
		port = strconv.FormatUint(n, 10)
	}
	canonical := scheme + "://" + host + ":" + port
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), true
}

func splitAuthority(authority string) (host, port string, ok bool) {
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", false
		}
		host = authority[:end+1]
		rest := authority[end+1:]
		if rest == "" {
			return host, "", true
		}
		if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
			return "", "", false
		}
		return host, rest[1:], allDigits(rest[1:])
	}
	if strings.Count(authority, ":") > 1 {
		return "", "", false
	}
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		host, port = authority[:i], authority[i+1:]
		if port == "" || !allDigits(port) {
			return "", "", false
		}
		return host, port, true
	}
	return authority, "", true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func canonicalHost(host string) (string, bool) {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		ip, err := netip.ParseAddr(host[1 : len(host)-1])
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false
		}
		return "[" + ip.String() + "]", true
	}
	if host == "" || strings.ContainsAny(host, "%[]/@\\ \t\r\n:") || len(host) > 253 {
		return "", false
	}
	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return "", false
			}
		}
	}
	return host, true
}

// ValidKey reports whether key has the shape Key returns: 64 lowercase hex digits.
func ValidKey(key string) bool {
	if len(key) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(key)
	return err == nil && len(decoded) == sha256.Size && key == strings.ToLower(key)
}
