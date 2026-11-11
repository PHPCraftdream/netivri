package main

import (
	"slices"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// ConfigKeyKind selects how a settings key is prompted, normalized and displayed
// (see cmd/netivri/tui_settings.go).
type ConfigKeyKind int

const (
	ConfigKeyText ConfigKeyKind = iota
	ConfigKeyPort
	ConfigKeyBool
	ConfigKeyPassword
	ConfigKeyProxy
)

// ConfigKey describes one editable settings key of config.ktav.
type ConfigKey struct {
	Key        string // "listen.bind" — exact CLI key name
	Group      string // "listen" | "auth" | "fetch" | "chain"
	GroupLabel string // "Listen" | "Auth" | "Fetch" | "Chain"
	Label      string // short label shown by the Settings screen
	Hint       string // one ASCII line describing the value
	Kind       ConfigKeyKind
	Restarts   bool
}

const argon2DisplayPrefix = "$argon2id$"

// configKeys is the canonical, ordered list of settings keys.
//
// Restarts mirrors the live-reload split (docs/design/socks5-chain.md,
// "Live reload"; cmd/netivri/live.go): listenerFingerprint covers bind,
// port, allowed clients and the auth credentials, so listen.* and
// auth.login/auth.password need an angel restart; a changed global
// post-hop restarts too; chain.pre_socks5 is applied in place;
// fetch.* and auth.allow_init_remote are not core settings and apply
// without a restart.
var configKeys = []ConfigKey{
	{Key: "listen.bind", Group: "listen", GroupLabel: "Listen", Label: "Bind",
		Hint: "IP address or localhost the proxy listens on", Kind: ConfigKeyText, Restarts: true},
	{Key: "listen.port", Group: "listen", GroupLabel: "Listen", Label: "Port",
		Hint: "mixed proxy port between 1 and 65535", Kind: ConfigKeyPort, Restarts: true},
	{Key: "listen.allowed_clients", Group: "listen", GroupLabel: "Listen", Label: "Allowed clients",
		Hint: "client address rules such as * or 127.0.0.1", Kind: ConfigKeyText, Restarts: true},
	{Key: "auth.login", Group: "auth", GroupLabel: "Auth", Label: "Login",
		Hint: "local proxy login; empty disables proxy auth", Kind: ConfigKeyText, Restarts: true},
	{Key: "auth.password", Group: "auth", GroupLabel: "Auth", Label: "Password",
		Hint: "stored as an argon2id hash; init waits for the first client", Kind: ConfigKeyPassword, Restarts: true},
	{Key: "auth.allow_init_remote", Group: "auth", GroupLabel: "Auth", Label: "Allow init remote",
		Hint: "let non-loopback clients claim the init login", Kind: ConfigKeyBool},
	{Key: "fetch.proxy", Group: "fetch", GroupLabel: "Fetch", Label: "Fetch proxy",
		Hint: "proxy URL for subscription fetches; none clears", Kind: ConfigKeyProxy},
	{Key: "fetch.update_interval", Group: "fetch", GroupLabel: "Fetch", Label: "Update interval",
		Hint: "refresh period such as 30m or 1h, off, or empty for 1h", Kind: ConfigKeyText},
	{Key: "chain.pre_socks5", Group: "chain", GroupLabel: "Chain", Label: "Pre-hop",
		Hint: "pre-hop proxy URL (socks5/http/https); none clears", Kind: ConfigKeyProxy},
	{Key: "chain.post_socks5", Group: "chain", GroupLabel: "Chain", Label: "Post-hop",
		Hint: "post-hop proxy URL (socks5/http/https); none clears", Kind: ConfigKeyProxy, Restarts: true},
}

// ConfigKeys returns the settings keys in canonical order.
func ConfigKeys() []ConfigKey { return append([]ConfigKey(nil), configKeys...) }

// ConfigKeyByName looks a key up by its exact CLI name.
func ConfigKeyByName(key string) (ConfigKey, bool) {
	for _, k := range configKeys {
		if k.Key == key {
			return k, true
		}
	}
	return ConfigKey{}, false
}

// configKeysHint lists the valid key names for diagnostics.
func configKeysHint() string {
	names := make([]string, 0, len(configKeys))
	for _, k := range configKeys {
		names = append(names, k.Key)
	}
	return "valid keys: " + strings.Join(names, ", ")
}

func configUsageError(op, key string) *cli.Error {
	return &cli.Error{
		Code:    "USAGE",
		Message: "config " + op + ": unknown key " + strconv.Quote(key),
		Hint:    configKeysHint(),
		Exit:    cli.ExitUsage,
	}
}

// configProxyValue is the redacted display shape of a scalar proxy setting.
type configProxyValue struct {
	Value string `json:"value"`
	Auth  bool   `json:"auth"`
}

type configHopValue struct {
	Value []string `json:"value"`
	Auth  bool     `json:"auth"`
}

// ConfigDisplayValue returns redacted display values: strings for text keys,
// integers for ports, booleans, password state, a scalar fetch proxy object,
// or a redacted ordered array object for chain keys. Unknown keys yield nil.
func ConfigDisplayValue(cfg *config.Config, key string) any {
	switch key {
	case "listen.bind":
		return cfg.Listen.Bind
	case "listen.port":
		return cfg.Listen.Port
	case "listen.allowed_clients":
		return cfg.Listen.AllowedClients
	case "auth.login":
		return cfg.Auth.Login
	case "auth.password":
		if strings.HasPrefix(cfg.Auth.Password, argon2DisplayPrefix) {
			return "set"
		}
		return "not set"
	case "auth.allow_init_remote":
		return cfg.Auth.AllowInitRemote
	case "fetch.proxy":
		return configProxyValue{Value: redactChain(cfg.Fetch.Proxy), Auth: chainHasAuth(cfg.Fetch.Proxy)}
	case "fetch.update_interval":
		return cfg.Fetch.UpdateInterval
	case "chain.pre_socks5":
		return configHopValue{Value: redactChains(cfg.Chain.PreSOCKS5), Auth: chainsHaveAuth(cfg.Chain.PreSOCKS5)}
	case "chain.post_socks5":
		return configHopValue{Value: redactChains(cfg.Chain.PostSOCKS5), Auth: chainsHaveAuth(cfg.Chain.PostSOCKS5)}
	}
	return nil
}

// ConfigRawValue returns the presettable raw value of a key for the
// prompt: a decimal port, "true"/"false", or the raw text. Secrets
// (auth.password) and proxy URLs are never presettable and return
// ("", false), like unknown keys.
func ConfigRawValue(cfg *config.Config, key string) (string, bool) {
	k, ok := ConfigKeyByName(key)
	if !ok {
		return "", false
	}
	switch k.Kind {
	case ConfigKeyPort:
		return strconv.Itoa(cfg.Listen.Port), true
	case ConfigKeyBool:
		if cfg.Auth.AllowInitRemote {
			return "true", true
		}
		return "false", true
	case ConfigKeyPassword, ConfigKeyProxy:
		return "", false
	}
	switch key {
	case "listen.bind":
		return cfg.Listen.Bind, true
	case "listen.allowed_clients":
		return cfg.Listen.AllowedClients, true
	case "auth.login":
		return cfg.Auth.Login, true
	case "fetch.update_interval":
		return cfg.Fetch.UpdateInterval, true
	}
	return "", false
}

// ConfigGetResult is the `config get` payload: display values grouped
// by settings group, each group keyed by the key suffix.
type ConfigGetResult struct {
	Groups map[string]map[string]any `json:"groups"`
}

// ConfigGet reads the configuration and returns its display values.
// An empty key returns all four groups; a valid key returns only that
// key's group. Display values never contain secrets.
func ConfigGet(home, key string) (ConfigGetResult, error) {
	filter := ""
	if key != "" {
		k, ok := ConfigKeyByName(key)
		if !ok {
			return ConfigGetResult{}, configUsageError("get", key)
		}
		filter = k.Group
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return ConfigGetResult{}, err
	}
	groups := map[string]map[string]any{}
	for _, k := range configKeys {
		if filter != "" && k.Group != filter {
			continue
		}
		g, ok := groups[k.Group]
		if !ok {
			g = map[string]any{}
			groups[k.Group] = g
		}
		g[strings.TrimPrefix(k.Key, k.Group+".")] = ConfigDisplayValue(cfg, k.Key)
	}
	return ConfigGetResult{Groups: groups}, nil
}

// ConfigSetResult reports what `config set` did.
type ConfigSetResult struct {
	Key           string `json:"key"`
	Changed       bool   `json:"changed"`
	RestartNeeded bool   `json:"restart_needed"`
}

// parseConfigBool accepts the case-insensitive truthy/falsy spellings
// of the boolean settings keys.
func parseConfigBool(value string) (bool, bool) {
	switch strings.ToLower(value) {
	case "true", "1", "t", "y", "yes":
		return true, true
	case "false", "0", "f", "n", "no":
		return false, true
	}
	return false, false
}

func singletonHop(value string) config.HopList {
	if value == "" {
		return nil
	}
	return config.HopList{value}
}

// configClearableKey reports whether "none" or an empty value clears
// the key.
func configClearableKey(key string) bool {
	switch key {
	case "auth.login", "auth.password", "fetch.proxy",
		"fetch.update_interval", "chain.pre_socks5", "chain.post_socks5":
		return true
	}
	return false
}

// ConfigSet validates and applies one settings value. Nothing is
// written when the value is invalid: the whole resulting configuration
// is validated on a candidate copy first and the loaded config is left
// untouched on failure. Error messages name the key, never the value;
// plain passwords are stored as argon2id hashes and never echoed.
func ConfigSet(home, key, value string) (ConfigSetResult, error) {
	k, ok := ConfigKeyByName(key)
	if !ok {
		return ConfigSetResult{}, configUsageError("set", key)
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return ConfigSetResult{}, err
	}

	var (
		text    string
		port    int
		boolean bool
		changed bool
	)
	switch k.Kind {
	case ConfigKeyPort:
		p, perr := strconv.Atoi(value)
		if perr != nil {
			return ConfigSetResult{}, &cli.Error{
				Code:    "USAGE",
				Message: "config set: listen.port must be a whole number between 1 and 65535",
				Exit:    cli.ExitUsage,
			}
		}
		port, changed = p, cfg.Listen.Port != p
	case ConfigKeyBool:
		b, berr := parseConfigBool(value)
		if !berr {
			return ConfigSetResult{}, &cli.Error{
				Code:    "USAGE",
				Message: "config set: auth.allow_init_remote must be true or false",
				Exit:    cli.ExitUsage,
			}
		}
		boolean, changed = b, cfg.Auth.AllowInitRemote != b
	case ConfigKeyPassword:
		old := cfg.Auth.Password
		switch {
		case value == "" || strings.EqualFold(value, "none"):
			text, changed = "", old != ""
		case value == config.InitPlaceholder:
			text, changed = value, old != value
		default:
			if len(value) > auth.MaxPasswordLen {
				return ConfigSetResult{}, cli.Usage("config set: auth.password is longer than %d bytes", auth.MaxPasswordLen)
			}
			if same, verr := auth.VerifyHash(old, value); verr == nil && same {
				changed, text = false, old
			} else {
				h, herr := auth.Hash(value)
				if herr != nil {
					return ConfigSetResult{}, &cli.Error{Code: "INTERNAL", Message: "hash the password", Exit: cli.ExitInternal}
				}
				changed, text = true, h
			}
		}
	default:
		text = value
		if configClearableKey(key) && (value == "" || strings.EqualFold(value, "none") && key != "chain.pre_socks5" && key != "chain.post_socks5") {
			text = ""
		}
		switch key {
		case "listen.bind":
			changed = cfg.Listen.Bind != text
		case "listen.allowed_clients":
			changed = cfg.Listen.AllowedClients != text
		case "auth.login":
			changed = cfg.Auth.Login != text
		case "fetch.proxy":
			changed = cfg.Fetch.Proxy != text
		case "fetch.update_interval":
			changed = cfg.Fetch.UpdateInterval != text
		case "chain.pre_socks5":
			changed = !slices.Equal(cfg.Chain.PreSOCKS5, singletonHop(text))
		case "chain.post_socks5":
			changed = !slices.Equal(cfg.Chain.PostSOCKS5, singletonHop(text))
		}
	}

	// Validate the whole candidate before touching the loaded config:
	// Validate only reads, so a shallow copy is enough.
	cand := *cfg
	switch key {
	case "listen.bind":
		cand.Listen.Bind = text
	case "listen.port":
		cand.Listen.Port = port
	case "listen.allowed_clients":
		cand.Listen.AllowedClients = text
	case "auth.login":
		cand.Auth.Login = text
	case "auth.password":
		cand.Auth.Password = text
	case "auth.allow_init_remote":
		cand.Auth.AllowInitRemote = boolean
	case "fetch.proxy":
		cand.Fetch.Proxy = text
	case "fetch.update_interval":
		cand.Fetch.UpdateInterval = text
	case "chain.pre_socks5":
		cand.Chain.PreSOCKS5 = singletonHop(text)
	case "chain.post_socks5":
		cand.Chain.PostSOCKS5 = singletonHop(text)
	}
	if err := cand.Validate(); err != nil {
		return ConfigSetResult{}, err
	}
	if !changed {
		return ConfigSetResult{Key: key, Changed: false, RestartNeeded: false}, nil
	}
	*cfg = cand
	if err := saveAccountConfig(home, cfg); err != nil {
		return ConfigSetResult{}, err
	}
	return ConfigSetResult{Key: key, Changed: true, RestartNeeded: k.Restarts}, nil
}
