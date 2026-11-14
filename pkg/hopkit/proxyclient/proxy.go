// Package proxyclient parses proxy URLs and establishes SOCKS5 or HTTP CONNECT tunnels.
package proxyclient

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Proxy describes a SOCKS5, HTTP CONNECT, or HTTPS CONNECT proxy.
type Proxy struct {
	// Scheme is socks5, http, or https.
	Scheme string
	// Host is the proxy server name or IP literal.
	Host string
	// Port is the proxy server port.
	Port int
	// Username is an optional proxy credential.
	Username string
	// Password is an optional proxy credential.
	Password string
}

var errInvalidProxyURL = errors.New("invalid proxy URL")
var errInvalidProxy = errors.New("invalid proxy")
var errInvalidTarget = errors.New("invalid target")

// Parse parses a proxy URL, applying the scheme's default port when omitted.
func Parse(raw string) (Proxy, error) {
	var p Proxy
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return p, &Error{Stage: StageOther, Err: errInvalidProxyURL}
	}
	if u.Scheme == "" || u.Opaque != "" || u.Host == "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return p, &Error{Stage: StageOther, Err: errInvalidProxyURL}
	}

	p.Scheme = strings.ToLower(u.Scheme)
	switch p.Scheme {
	case "socks5":
		p.Port = 1080
	case "http":
		p.Port = 80
	case "https":
		p.Port = 443
	default:
		return Proxy{}, &Error{Stage: StageOther, Err: errInvalidProxyURL}
	}
	authority := u.Host
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	p.Host = u.Hostname()
	if p.Host == "" || validateHost(p.Host) != nil || (strings.HasPrefix(authority, "[") && !strings.Contains(p.Host, ":")) {
		return Proxy{}, &Error{Stage: StageOther, Err: errInvalidProxyURL}
	}
	port, present, ok := urlPort(authority)
	if !ok {
		return Proxy{}, &Error{Stage: StageOther, Err: errInvalidProxyURL}
	}
	if present {
		p.Port = port
	}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, nil
}

// String returns the proxy URL with any nonempty password replaced by ***.
// It includes the username when credentials are configured.
func (p Proxy) String() string {
	port := strconv.Itoa(p.Port)
	host := p.Host
	if strings.Contains(host, ":") {
		host = strings.ReplaceAll(host, "%", "%25")
	}
	u := url.URL{Scheme: p.Scheme, Host: net.JoinHostPort(host, port)}
	if p.Username == "" && p.Password == "" {
		return u.String()
	}
	userinfo := url.User(p.Username).String()
	if p.Password != "" {
		userinfo += ":***"
	}
	return u.Scheme + "://" + userinfo + "@" + u.Host
}

func urlPort(authority string) (int, bool, bool) {
	var i int
	if strings.HasPrefix(authority, "[") {
		close := strings.IndexByte(authority, ']')
		if close < 0 {
			return 0, false, false
		}
		suffix := authority[close+1:]
		if suffix == "" {
			return 0, false, true
		}
		if !strings.HasPrefix(suffix, ":") {
			return 0, false, false
		}
		i = close + 1
	} else {
		n := strings.Count(authority, ":")
		if n > 1 {
			return 0, false, false
		}
		if n == 0 {
			return 0, false, true
		}
		i = strings.LastIndexByte(authority, ':')
	}
	port, ok := parsePort(authority[i+1:])
	return port, true, ok
}

func parsePort(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil || n == 0 {
		return 0, false
	}
	return int(n), true
}

func validateProxy(p Proxy) error {
	scheme := strings.ToLower(p.Scheme)
	if scheme != "socks5" && scheme != "http" && scheme != "https" {
		return errInvalidProxy
	}
	if validateHost(p.Host) != nil || p.Port < 1 || p.Port > 65535 {
		return errInvalidProxy
	}
	if scheme == "socks5" && (len(p.Username) > 255 || len(p.Password) > 255) {
		return errInvalidProxy
	}
	return nil
}

func validateHost(host string) error {
	if host == "" || strings.TrimSpace(host) != host {
		return errInvalidProxy
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || len(name) > 253 {
		return errInvalidProxy
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errInvalidProxy
		}
		for _, c := range label {
			if !isHostChar(c) {
				return errInvalidProxy
			}
		}
	}
	return nil
}

func isHostChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func validateTarget(target string) (host string, port int, err error) {
	host, portText, splitErr := net.SplitHostPort(target)
	if splitErr != nil || validateHost(host) != nil || strings.Contains(host, "%") {
		return "", 0, errInvalidTarget
	}
	port, ok := parsePort(portText)
	if !ok {
		return "", 0, errInvalidTarget
	}
	return host, port, nil
}
