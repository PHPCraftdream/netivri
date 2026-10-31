// Package config loads, validates and edits the netivri configuration
// file, written in Ktav 0.8.0 (docs/config.md).
package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// Stable error codes produced by this package (see docs/cli.md).
const CodeConfigInvalid = "CONFIG_INVALID"

// Selection modes.
const (
	ModeManual  = "manual"
	ModeFastest = "fastest"
	ModeAuto    = "auto"
)

// InitPlaceholder fills auth.login / auth.password until the first
// client provides the real credentials (docs/config.md, "init").
const InitPlaceholder = "init"

const argon2Prefix = "$argon2id$"

// Config is the whole configuration file, decoded from Ktav.
type Config struct {
	Listen        Listen         `json:"listen"`
	Auth          Auth           `json:"auth"`
	Fetch         Fetch          `json:"fetch"`
	Chain         Chain          `json:"chain"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	Accounts      []Account      `json:"accounts,omitempty"`
	Selection     Selection      `json:"selection"`
}

// Account is one proxy account with its optional provider binding and
// exit country.
type Account struct {
	Login       string `json:"login"`
	Password    string `json:"password"`
	Provider    string `json:"provider,omitempty"`
	ExitCountry string `json:"exit_country,omitempty"`
	// AllowedClients optionally narrows the client addresses this account
	// may authenticate from; empty means no account restriction.
	AllowedClients string `json:"allowed_clients,omitempty"`
	PreSOCKS5      string `json:"pre_socks5,omitempty"`
	PostSOCKS5     string `json:"post_socks5,omitempty"`
}

// Account returns the account and its index, or (nil, -1) when absent.
func (c *Config) Account(login string) (*Account, int) {
	for i := range c.Accounts {
		if c.Accounts[i].Login == login {
			return &c.Accounts[i], i
		}
	}
	return nil, -1
}

// ValidAccountLogin reports whether login uses the supported account alphabet.
func ValidAccountLogin(login string) bool {
	if len(login) < 1 || len(login) > 64 {
		return false
	}
	for _, r := range login {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._@-", r)) {
			return false
		}
	}
	return true
}

// ValidProviderName reports whether name matches the subscription
// name alphabet: 1..64 characters of A-Za-z0-9._-.
func ValidProviderName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// NormalizeCountry upper-cases a two-letter ASCII country code.
func NormalizeCountry(code string) (string, bool) {
	if len(code) != 2 {
		return "", false
	}
	b := []byte(code)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		} else if b[i] < 'A' || b[i] > 'Z' {
			return "", false
		}
	}
	return string(b), true
}

// Chain is the optional pair of global SOCKS5 hops.
type Chain struct {
	PreSOCKS5  string `json:"pre_socks5,omitempty"`
	PostSOCKS5 string `json:"post_socks5,omitempty"`
}

// Listen is the local mixed HTTP/SOCKS5 proxy endpoint.
type Listen struct {
	Bind string `json:"bind"`
	Port int    `json:"port"`
	// AllowedClients restricts which client addresses may connect: an
	// allowed-clients rule list (allowed_clients.go). Empty is invalid;
	// the loaded value of an absent key is "*".
	AllowedClients string `json:"allowed_clients,omitempty"`
}

// Auth controls who may use the local proxy. Empty Auth means no auth.
// Empty fields carry omitempty so a rendered file stays tidy.
type Auth struct {
	Login           string `json:"login,omitempty"`
	Password        string `json:"password,omitempty"`
	AllowInitRemote bool   `json:"allow_init_remote,omitempty"`
}

// Automatic subscription refresh by the angel (fetch.update_interval).
const (
	DefaultUpdateInterval = time.Hour
	MinUpdateInterval     = 5 * time.Minute
	MaxUpdateInterval     = 7 * 24 * time.Hour
	// UpdateOff is the fetch.update_interval value that disables the refresh.
	UpdateOff = "off"
)

// Fetch carries the proxy used for all outgoing subscription fetches and how
// often the angel refreshes the subscriptions.
type Fetch struct {
	Proxy string `json:"proxy,omitempty"`
	// UpdateInterval is a Go duration ("30m", "1h"), "off", or empty for the
	// default of one hour.
	UpdateInterval string `json:"update_interval,omitempty"`
}

// Interval returns the refresh period and false when the refresh is off. It
// expects a validated value; an unparsable one reads as the default.
func (f Fetch) Interval() (time.Duration, bool) {
	switch f.UpdateInterval {
	case "":
		return DefaultUpdateInterval, true
	case UpdateOff:
		return 0, false
	}
	d, err := time.ParseDuration(f.UpdateInterval)
	if err != nil {
		return DefaultUpdateInterval, true
	}
	return d, true
}

func validateUpdateInterval(raw string) error {
	if raw == "" || raw == UpdateOff {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < MinUpdateInterval || d > MaxUpdateInterval {
		return invalid(`fetch.update_interval must be "off" or a duration between 5m and 168h, such as "1h"`)
	}
	return nil
}

// Subscription is one provider subscription. URL is a secret.
type Subscription struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Via  string `json:"via,omitempty"`
}

// Selection is the exit-node choice applied on start and by `netivri use`.
type Selection struct {
	Node    string `json:"node,omitempty"`
	Country string `json:"country,omitempty"`
	Mode    string `json:"mode"`
}

// Default returns the safe default configuration.
func Default() *Config {
	return &Config{
		Listen:    Listen{Bind: "127.0.0.1", Port: 7890, AllowedClients: AllowedClientsAny},
		Selection: Selection{Mode: ModeManual},
	}
}

// Validate checks the whole configuration. Errors are *cli.Error with
// code CONFIG_INVALID and never contain subscription URLs, proxy
// credentials or passwords.
func (c *Config) Validate() error {
	if err := validateBind(c.Listen.Bind); err != nil {
		return err
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		return invalid("listen.port must be between 1 and 65535")
	}
	if v := strings.TrimSpace(c.Listen.AllowedClients); v == "" {
		return invalid(`listen.allowed_clients must not be empty; omit the key or set it to "*"`)
	} else if _, err := ParseAllowedClients(v); err != nil {
		return invalid("listen.allowed_clients: %v", err)
	}
	if err := checkScalar("fetch.proxy", c.Fetch.Proxy); err != nil {
		return err
	}
	if err := validateProxy("fetch.proxy", c.Fetch.Proxy); err != nil {
		return err
	}
	if err := checkScalar("fetch.update_interval", c.Fetch.UpdateInterval); err != nil {
		return err
	}
	if err := validateUpdateInterval(c.Fetch.UpdateInterval); err != nil {
		return err
	}
	if err := validateChainURL("chain.pre_socks5", c.Chain.PreSOCKS5); err != nil {
		return err
	}
	if err := validateChainURL("chain.post_socks5", c.Chain.PostSOCKS5); err != nil {
		return err
	}
	first := make(map[string]int, len(c.Subscriptions))
	for i, s := range c.Subscriptions {
		if err := checkScalar(fmt.Sprintf("subscriptions[%d].name", i), s.Name); err != nil {
			return err
		}
		if strings.TrimSpace(s.Name) == "" {
			return invalid("subscriptions[%d].name must not be empty", i)
		}
		// Positions only: a user-supplied name is never echoed (it
		// could be a URL or a credential).
		if j, seen := first[s.Name]; seen {
			return invalid("subscriptions[%d] duplicates subscriptions[%d]", i, j)
		}
		first[s.Name] = i
		if err := checkScalar(fmt.Sprintf("subscriptions[%d].url", i), s.URL); err != nil {
			return err
		}
		if strings.TrimSpace(s.URL) == "" {
			return invalid("subscriptions[%d].url must not be empty", i)
		}
		if err := checkScalar(fmt.Sprintf("subscriptions[%d].via", i), s.Via); err != nil {
			return err
		}
		if s.Via != "" {
			if err := validateProxy(fmt.Sprintf("subscriptions[%d].via", i), s.Via); err != nil {
				return err
			}
		}
	}
	if err := checkScalar("selection.country", c.Selection.Country); err != nil {
		return err
	}
	if err := checkScalar("selection.node", c.Selection.Node); err != nil {
		return err
	}
	switch c.Selection.Mode {
	case ModeManual, ModeFastest, ModeAuto:
	default:
		return invalid(`selection.mode must be one of "manual", "fastest", "auto"`)
	}
	if c.Auth.Password != "" {
		if c.Auth.Password != InitPlaceholder && !strings.HasPrefix(c.Auth.Password, argon2Prefix) {
			return invalid(`auth.password must be empty, "init" or an argon2id PHC hash`)
		}
		if c.Auth.Login == "" {
			return invalid("auth.password requires auth.login")
		}
	}
	if c.Auth.Login != "" && c.Auth.Password == "" {
		return invalid("auth.login requires auth.password")
	}
	if err := checkScalar("auth.login", c.Auth.Login); err != nil {
		return err
	}
	if err := checkScalar("auth.password", c.Auth.Password); err != nil {
		return err
	}
	firstLogin := make(map[string]int, len(c.Accounts))
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if !ValidAccountLogin(a.Login) {
			return invalid("accounts[%d].login must be a valid account login", i)
		}
		if j, ok := firstLogin[a.Login]; ok {
			return invalid("accounts[%d] duplicates accounts[%d]", i, j)
		}
		firstLogin[a.Login] = i
		if a.Login == InitPlaceholder {
			return invalid("accounts[%d].login must not be the reserved init login", i)
		}
		if c.Auth.Login != "" && c.Auth.Login != InitPlaceholder && c.Auth.Login == a.Login {
			return invalid("accounts[%d].login duplicates auth.login", i)
		}
		if a.Password != InitPlaceholder && !strings.HasPrefix(a.Password, argon2Prefix) {
			return invalid("accounts[%d].password must be init or an argon2id PHC hash", i)
		}
		if err := checkScalar(fmt.Sprintf("accounts[%d].password", i), a.Password); err != nil {
			return err
		}
		if _, ok := ParseCountryList(a.ExitCountry); !ok {
			return invalid("accounts[%d].exit_country must be empty or a comma-separated list of two-letter country codes", i)
		}
		if err := checkScalar(fmt.Sprintf("accounts[%d].exit_country", i), a.ExitCountry); err != nil {
			return err
		}
		if _, ok := ParseProviderList(a.Provider); !ok {
			return invalid("accounts[%d].provider must be empty or a valid provider name, or a comma-separated list of them", i)
		}
		if err := checkScalar(fmt.Sprintf("accounts[%d].provider", i), a.Provider); err != nil {
			return err
		}
		if _, err := ParseAllowedClients(a.AllowedClients); err != nil {
			return invalid("accounts[%d].allowed_clients: %v", i, err)
		}
		if err := checkScalar(fmt.Sprintf("accounts[%d].pre_socks5", i), a.PreSOCKS5); err != nil {
			return err
		}
		if err := validateChainURL(fmt.Sprintf("accounts[%d].pre_socks5", i), a.PreSOCKS5); err != nil {
			return err
		}
		if err := checkScalar(fmt.Sprintf("accounts[%d].post_socks5", i), a.PostSOCKS5); err != nil {
			return err
		}
		if err := validateChainURL(fmt.Sprintf("accounts[%d].post_socks5", i), a.PostSOCKS5); err != nil {
			return err
		}
	}
	if c.usesInitAuth() && !isLoopback(c.Listen.Bind) && !c.Auth.AllowInitRemote {
		return invalid(`auth "init" is only accepted on a loopback bind; set auth.allow_init_remote: true to allow it elsewhere`)
	}
	return nil
}

// normalizeAccountLists puts the provider and exit-country lists of every
// account into the canonical stored form: no blanks, upper-case codes, a
// repeated item kept once. An invalid list is left for Validate to report.
func (c *Config) normalizeAccountLists() {
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if codes, ok := ParseCountryList(a.ExitCountry); ok {
			a.ExitCountry = JoinList(codes)
		}
		if names, ok := ParseProviderList(a.Provider); ok {
			a.Provider = JoinList(names)
		}
	}
}

func (c *Config) usesInitAuth() bool {
	if c.Auth.Login == InitPlaceholder || c.Auth.Password == InitPlaceholder {
		return true
	}
	for _, a := range c.Accounts {
		if a.Password == InitPlaceholder {
			return true
		}
	}
	return false
}

func validateBind(bind string) error {
	if bind == "localhost" {
		return nil
	}
	if net.ParseIP(bind) == nil {
		return invalid(`listen.bind must be an IP address or "localhost"`)
	}
	return nil
}

// isChainScheme reports whether scheme names a proxy that a chain hop may be:
// socks5, http (an HTTP CONNECT proxy) or https (the same over TLS).
func isChainScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "socks5", "http", "https":
		return true
	}
	return false
}

func validateChainURL(field, raw string) error {
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || !isChainScheme(u.Scheme) || u.Opaque != "" || u.Hostname() == "" || hasInvalidEscapes(raw) || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return invalid("%s must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", field)
	}
	port := u.Port()
	if port == "" {
		return invalid("%s must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", field)
	}
	if port != "" {
		for _, r := range port {
			if r < '0' || r > '9' {
				return invalid("%s must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", field)
			}
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return invalid("%s must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", field)
		}
	}
	return nil
}

func hasInvalidEscapes(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '%' {
			if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
				return true
			}
			i += 2
		}
	}
	return false
}
func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// validateProxy checks a fetch/via proxy URL: scheme whitelist plus a
// host. The parsed URL itself is never echoed back.
func validateProxy(field, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return invalid("%s must be a socks5://, socks5h://, http:// or https:// URL with a host", field)
	}
	switch u.Scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return invalid("%s scheme must be socks5, socks5h, http or https", field)
	}
	return nil
}

// checkScalar rejects values that cannot survive a whole-file
// rewrite: Ktav trims leading/trailing whitespace from scalar bodies
// (spec 3.3, 5.2) and a scalar value is a single line, so control
// characters and line breaks are rejected outright. The value itself
// is never echoed back into the message.
func checkScalar(field, s string) error {
	if s == "" {
		return nil
	}
	if s != strings.TrimSpace(s) {
		return invalid("%s must not have leading or trailing whitespace", field)
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return invalid("%s must not contain control characters or line breaks", field)
		}
	}
	return nil
}

func isLoopback(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

func invalid(format string, args ...any) *cli.Error {
	return &cli.Error{
		Code:    CodeConfigInvalid,
		Exit:    cli.ExitUsage,
		Message: fmt.Sprintf(format, args...),
		Hint:    "fix the value in config.ktav; see netivri help config",
	}
}
