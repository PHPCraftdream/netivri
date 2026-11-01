package main

import (
	"errors"
	"flag"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

type serverParamFlags []string

func (p *serverParamFlags) String() string     { return "" }
func (p *serverParamFlags) Set(v string) error { *p = append(*p, v); return nil }

type serverView struct {
	Name          string   `json:"name"`
	Protocol      string   `json:"protocol"`
	Bind          string   `json:"bind"`
	Port          int      `json:"port"`
	Accounts      string   `json:"accounts"`
	PublicHost    string   `json:"public_host,omitempty"`
	Members       []string `json:"members,omitempty"`
	Default       bool     `json:"default,omitempty"`
	SingleUser    bool     `json:"single_user"`
	Unclassified  uint64   `json:"unclassified,omitempty"`
	SlowFirstByte uint64   `json:"slow_first_byte,omitempty"`
	Replayed      uint64   `json:"replayed,omitempty"`
	Ambiguous     uint64   `json:"ambiguous,omitempty"`
	Params        []string `json:"params"`
}

func serverPublic(cfg *config.Config, s config.Server) serverView {
	p, _ := servers.Lookup(s.Protocol)
	keys := make([]string, 0, len(s.Params))
	for k := range s.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	bind := s.Bind
	if bind == "" {
		bind = "0.0.0.0"
	}
	view := serverView{Name: s.Name, Protocol: s.Protocol, Bind: bind, Port: s.Port, Accounts: s.Accounts, PublicHost: s.PublicHost, Default: s.Default, SingleUser: p != nil && p.SingleUser(), Params: keys}
	if cfg != nil {
		for _, peer := range cfg.Servers {
			peerBind := peer.Bind
			if peerBind == "" {
				peerBind = "0.0.0.0"
			}
			if peer.Port == s.Port && peerBind == bind {
				peerProto, _ := servers.Lookup(peer.Protocol)
				if peerProto != nil && p != nil && servers.IsUDPBound(peerProto) == servers.IsUDPBound(p) {
					view.Members = append(view.Members, peer.Name)
				}
			}
		}
	}
	if len(view.Members) <= 1 {
		view.Members = nil
	}
	sort.Strings(view.Members)
	return view
}
func serverError(code, msg string, exit int) error {
	return &cli.Error{Code: code, Message: msg, Exit: exit}
}
func serverNotFound() error {
	return serverError("SERVER_NOT_FOUND", "server not found", cli.ExitNotFound)
}
func serverConflict(msg string) error     { return serverError("SERVER_EXISTS", msg, cli.ExitConflict) }
func serverPortConflict(msg string) error { return serverError("PORT_IN_USE", msg, cli.ExitConflict) }
func serverPortAmbiguous(msg string) error {
	return serverError("SERVER_PORT_AMBIGUOUS", msg, cli.ExitConflict)
}
func serverNameValid(s string) bool {
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
func serverHasAccount(accounts []string, login string) bool {
	for _, a := range accounts {
		if a == login {
			return true
		}
	}
	return false
}
func serverValidationError(err error) error {
	var ce *cli.Error
	if errors.As(err, &ce) {
		return ce
	}
	return serverError("CONFIG_INVALID", "server configuration is invalid", cli.ExitUsage)
}
func runServer(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("server: missing subcommand")
	}
	sub := args[0]
	home, rest, err := parseHomeArgs("server "+sub, args[1:])
	if err != nil {
		return err
	}
	if sub == "protocols" {
		if len(rest) != 0 {
			return cli.Usage("server protocols: unexpected argument")
		}
		items := make([]map[string]any, 0)
		for _, p := range servers.Registered() {
			items = append(items, map[string]any{"name": p.Name(), "single_user": p.SingleUser(), "params": []string{}})
		}
		cli.Success(stdout, map[string]any{"protocols": items})
		return nil
	}
	cfg, err := loadConfig(home)
	if err != nil {
		return err
	}
	store := servers.NewStore(home)
	usage := func() error { return cli.Usage("server %s: invalid arguments", sub) }
	switch sub {
	case "list":
		if len(rest) != 0 {
			return usage()
		}
		items := make([]serverView, 0, len(cfg.Servers))
		for _, s := range cfg.Servers {
			items = append(items, serverPublic(cfg, s))
		}
		overlayServerRuntimeCounters(home, items)
		cli.Success(stdout, map[string]any{"servers": items})
		return nil
	case "add":
		fs := flag.NewFlagSet("server add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		protocol := fs.String("protocol", "", "protocol")
		name := fs.String("name", "", "name")
		account := fs.String("account", "", "account")
		port := fs.Int("port", 0, "port")
		bind := fs.String("bind", "0.0.0.0", "bind")
		public := fs.String("public-host", "", "host")
		defaultServer := fs.Bool("default", false, "default TLS server")
		var params serverParamFlags
		fs.Var(&params, "param", "key=value")
		pos, e := parseFlags(fs, rest)
		if e != nil || len(pos) != 0 || *protocol == "" || *account == "" || *port < 1 || *port > 65535 {
			return usage()
		}
		if _, ok := servers.Lookup(*protocol); !ok {
			return serverValidationError(&cli.Error{Code: "CONFIG_INVALID", Message: "servers[0].protocol is not registered", Exit: cli.ExitUsage})
		}
		if *account != "*" {
			for _, login := range strings.Split(*account, ",") {
				if !config.ValidAccountLogin(login) {
					return serverValidationError(&cli.Error{Code: "CONFIG_INVALID", Message: "servers[0].accounts contains an invalid account login", Exit: cli.ExitUsage})
				}
			}
		}
		pm := map[string]string{}
		for _, raw := range params {
			k, v, ok := strings.Cut(raw, "=")
			if !ok || k == "" {
				return cli.Usage("server add --param requires key=value")
			}
			if _, ok := pm[k]; ok {
				return cli.Usage("server add: duplicate --param key")
			}
			pm[k] = v
		}
		n := *name
		if n == "" {
			if *protocol == "shadowsocks" {
				n = "ss-" + strconv.Itoa(*port)
			} else {
				n = *protocol + "-" + strconv.Itoa(*port)
			}
		}
		if *name != "" && !serverNameValid(*name) {
			return serverValidationError(&cli.Error{Code: "CONFIG_INVALID", Message: "servers[0].name must be 1 to 32 letters, digits, dots, underscores or hyphens", Exit: cli.ExitUsage})
		}
		if _, i := cfg.GetServer(n); i >= 0 {
			return serverConflict("server name is already configured")
		}
		newProto, _ := servers.Lookup(*protocol)
		newUDP := newProto != nil && servers.IsUDPBound(newProto)
		newTLS, newTrial := servers.ShareMode(servers.Spec{Protocol: *protocol, Params: pm})
		if !newUDP && cfg.Listen.Port == *port && serverBindsOverlap(cfg.Listen.Bind, *bind) {
			return serverPortConflict("server port conflicts with the proxy listener")
		}
		for _, existing := range cfg.Servers {
			existingProto, known := servers.Lookup(existing.Protocol)
			if known && servers.IsUDPBound(existingProto) != newUDP {
				continue
			}
			if existing.Port != *port || !serverBindsOverlap(existing.Bind, *bind) {
				continue
			}
			existingTLS, existingTrial := servers.ShareMode(servers.Spec{Protocol: existing.Protocol, Params: existing.Params})
			if !newUDP && serverSameBind(existing.Bind, *bind) && ((existingTLS || existingTrial) && (newTLS || newTrial)) {
				continue
			}
			return serverPortConflict("server port conflicts with an existing listener")
		}
		entry := config.Server{Name: n, Protocol: *protocol, Bind: *bind, Port: *port, Accounts: *account, PublicHost: *public, Default: *defaultServer, Params: pm}
		oldServers := cfg.Servers
		cfg.Servers = append(cfg.Servers, entry)
		if e = cfg.Validate(); e != nil {
			cfg.Servers = oldServers
			return serverValidationError(e)
		}
		specs, e := cfg.ResolveServers()
		if e != nil {
			cfg.Servers = oldServers
			return serverValidationError(e)
		}
		var spec servers.Spec
		for _, s := range specs {
			if s.Name == n {
				spec = s
				break
			}
		}
		ensured, ensureErr := store.EnsureSecrets(spec)
		if ensureErr != nil {
			cfg.Servers = oldServers
			return serverError("INTERNAL", "server secrets could not be stored", cli.ExitInternal)
		}
		secretSpecs := make([]servers.Spec, 0)
		for _, current := range specs {
			proto, _ := servers.Lookup(current.Protocol)
			if current.Port != *port || !serverSameBind(current.Bind, *bind) || servers.IsUDPBound(proto) != newUDP {
				continue
			}
			if current.Name == n {
				secretSpecs = append(secretSpecs, ensured)
				continue
			}
			if applied, applyErr := store.Apply(current); applyErr != nil {
				cfg.Servers = oldServers
				_ = store.Remove(n)
				return serverError("INTERNAL", "server secrets could not be loaded", cli.ExitInternal)
			} else {
				secretSpecs = append(secretSpecs, applied)
			}
		}
		if err := servers.ValidateSharedPort(secretSpecs); err != nil {
			cfg.Servers = oldServers
			_ = store.Remove(n)
			return serverValidationError(err)
		}
		if err := servers.ValidateTrialSecrets(secretSpecs); err != nil {
			cfg.Servers = oldServers
			_ = store.Remove(n)
			return serverPortAmbiguous("shared endpoint credentials are ambiguous")
		}
		if err := config.Save(config.ConfigPath(home), cfg); err != nil {
			cfg.Servers = oldServers
			_ = store.Remove(n)
			return serverError("INTERNAL", "server configuration could not be saved", cli.ExitInternal)
		}
		cli.Success(stdout, map[string]any{"server": serverPublic(cfg, cfg.Servers[len(cfg.Servers)-1])})
		return nil
	case "show", "remove", "rotate", "link":
		fs := flag.NewFlagSet("server "+sub, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var acct, host, format *string
		var qr *bool
		if sub == "rotate" || sub == "link" {
			acct = fs.String("account", "", "account")
		}
		if sub == "link" {
			host = fs.String("host", "", "host")
			format = fs.String("format", "uri", "format")
			qr = fs.Bool("qr", false, "QR")
		}
		if sub != "link" {
			host, format, qr = nil, nil, nil
		}
		pos, e := parseFlags(fs, rest)
		if e != nil || len(pos) != 1 {
			return usage()
		}
		name := pos[0]
		entry, _ := cfg.GetServer(name)
		if entry == nil {
			return serverNotFound()
		}
		switch sub {
		case "show":
			view := []serverView{serverPublic(cfg, *entry)}
			overlayServerRuntimeCounters(home, view)
			cli.Success(stdout, map[string]any{"server": view[0]})
			return nil
		case "remove":
			_, i := cfg.GetServer(name)
			old := cfg.Servers[i]
			cfg.Servers = append(cfg.Servers[:i], cfg.Servers[i+1:]...)
			if err := config.Save(config.ConfigPath(home), cfg); err != nil {
				cfg.Servers = append(cfg.Servers[:i], append([]config.Server{old}, cfg.Servers[i:]...)...)
				return serverError("INTERNAL", "server configuration could not be saved", cli.ExitInternal)
			}
			if e = store.Remove(name); e != nil {
				cfg.Servers = append(cfg.Servers[:i], append([]config.Server{old}, cfg.Servers[i:]...)...)
				_ = config.Save(config.ConfigPath(home), cfg)
				return serverError("INTERNAL", "server secrets could not be removed", cli.ExitInternal)
			}
			cli.Success(stdout, map[string]any{"removed": name})
			return nil
		case "rotate", "link":
			specs, e := cfg.ResolveServers()
			if e != nil {
				return serverValidationError(e)
			}
			var spec servers.Spec
			for _, s := range specs {
				if s.Name == name {
					spec = s
					break
				}
			}
			if sub == "rotate" {
				if *acct != "" && !serverHasAccount(spec.Accounts, *acct) {
					return cli.Usage("account is not served by this server")
				}
				if _, e = store.Rotate(spec, *acct); e != nil {
					return serverError("INTERNAL", "server secret rotation failed", cli.ExitInternal)
				}
				cli.Success(stdout, map[string]any{"rotated": name, "account": *acct})
				return nil
			}
			if sub == "link" && *qr {
				return cli.Usage("server link --qr is unavailable: no QR encoder is included")
			}
			if *format != "uri" && *format != "mihomo" {
				return usage()
			}
			if e = store.Load(); e != nil {
				return serverError("INTERNAL", "server secrets could not be loaded", cli.ExitInternal)
			}
			spec, e = store.Apply(spec)
			if e != nil {
				return serverError("INTERNAL", "server secrets could not be loaded", cli.ExitInternal)
			}
			login := *acct
			if login == "" {
				if len(spec.Accounts) != 1 {
					return cli.Usage("server link requires --account when the server has multiple accounts")
				}
				login = spec.Accounts[0]
			}
			if !serverHasAccount(spec.Accounts, login) {
				return cli.Usage("account is not served by this server")
			}
			h := ""
			if *host != "" {
				h = *host
			} else {
				h = entry.PublicHost
			}
			h = strings.Trim(h, "[]")
			if h == "" || !validServerHost(h) {
				return cli.Usage("server link requires a hostname or IP without a port")
			}
			p, _ := servers.Lookup(spec.Protocol)
			var link string
			if *format == "mihomo" {
				m, ok := p.(servers.MihomoLinker)
				if !ok {
					return cli.Usage("protocol does not support mihomo format")
				}
				link, e = m.Mihomo(spec, login, h)
			} else {
				link, e = p.Link(spec, login, h)
			}
			if e != nil {
				return serverValidationError(e)
			}
			cli.Success(stdout, map[string]any{"name": name, "account": login, "format": *format, "link": link})
			return nil
		}
	}
	return cli.Usage("unknown server command %q", sub)
}
func configInvalidServer() error {
	return &cli.Error{Code: "CONFIG_INVALID", Message: "server configuration is invalid", Exit: cli.ExitUsage}
}

// serverSameBind reports whether two bind values are the same address (an
// empty bind is 0.0.0.0).
func serverSameBind(a, b string) bool {
	if a == "" {
		a = "0.0.0.0"
	}
	if b == "" {
		b = "0.0.0.0"
	}
	ipA, ipB := net.ParseIP(a), net.ParseIP(b)
	if ipA == nil || ipB == nil {
		return a == b
	}
	return ipA.Equal(ipB)
}

func serverBindsOverlap(a, b string) bool {
	if a == "" {
		a = "0.0.0.0"
	}
	if b == "" {
		b = "0.0.0.0"
	}
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	return ia != nil && ib != nil && (ia.Equal(ib) || ia.IsUnspecified() && (ia.To4() == nil || ib.To4() != nil) || ib.IsUnspecified() && (ib.To4() == nil || ia.To4() != nil))
}
func validServerHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " /\t\r\n?#@") || strings.Contains(host, "[") || strings.Contains(host, "]") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	u, err := url.Parse("//" + host)
	return err == nil && u.Host == host && u.Hostname() != "" && u.Port() == "" && (u.Hostname() == "localhost" || strings.Contains(u.Hostname(), "."))
}
