package main

import (
	"errors"
	"flag"
	"io"
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
	v := serverView{Name: s.Name, Protocol: s.Protocol, Bind: bind, Port: s.Port, Accounts: s.Accounts, PublicHost: s.PublicHost, Default: s.Default, SingleUser: p != nil && p.SingleUser(), Params: keys}
	if cfg != nil {
		for _, peer := range cfg.Servers {
			pb := peer.Bind
			if pb == "" {
				pb = "0.0.0.0"
			}
			if peer.Port == s.Port && pb == bind {
				pp, _ := servers.Lookup(peer.Protocol)
				if pp != nil && p != nil && servers.IsUDPBound(pp) == servers.IsUDPBound(p) {
					v.Members = append(v.Members, peer.Name)
				}
			}
		}
	}
	if len(v.Members) <= 1 {
		v.Members = nil
	}
	sort.Strings(v.Members)
	return v
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
func serverHasAccount(a []string, l string) bool {
	for _, v := range a {
		if v == l {
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
func ServerList(home string) ([]serverView, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return nil, e
	}
	return serverListConfig(home, cfg)
}
func serverListConfig(home string, cfg *config.Config) ([]serverView, error) {
	items := make([]serverView, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		items = append(items, serverPublic(cfg, s))
	}
	overlayServerRuntimeCounters(home, items)
	return items, nil
}
func ServerShow(home, name string) (serverView, error) {
	cfg, e := loadConfig(home)
	if e != nil {
		return serverView{}, e
	}
	return serverShowConfig(home, cfg, name)
}
func serverShowConfig(home string, cfg *config.Config, name string) (serverView, error) {
	s, _ := cfg.GetServer(name)
	if s == nil {
		return serverView{}, serverNotFound()
	}
	v := []serverView{serverPublic(cfg, *s)}
	overlayServerRuntimeCounters(home, v)
	return v[0], nil
}
func runServer(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("server: missing subcommand")
	}
	sub := args[0]
	home, rest, e := parseHomeArgs("server "+sub, args[1:])
	if e != nil {
		return e
	}
	if sub == "protocols" {
		if len(rest) != 0 {
			return cli.Usage("server protocols: unexpected argument")
		}
		items := []map[string]any{}
		for _, p := range ServerProtocols() {
			items = append(items, map[string]any{"name": p.Name, "single_user": p.SingleUser, "params": []string{}})
		}
		cli.Success(stdout, map[string]any{"protocols": items})
		return nil
	}
	cfg, e := loadConfig(home)
	if e != nil {
		return e
	}
	usage := func() error { return cli.Usage("server %s: invalid arguments", sub) }
	switch sub {
	case "list":
		if len(rest) != 0 {
			return usage()
		}
		v, e := serverListConfig(home, cfg)
		if e != nil {
			return e
		}
		cli.Success(stdout, map[string]any{"servers": v})
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
		def := fs.Bool("default", false, "default TLS server")
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
			for _, a := range strings.Split(*account, ",") {
				if !config.ValidAccountLogin(a) {
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
			if _, ok = pm[k]; ok {
				return cli.Usage("server add: duplicate --param key")
			}
			pm[k] = v
		}
		n := *name
		if n == "" {
			n = *protocol + "-" + strconv.Itoa(*port)
			if *protocol == "shadowsocks" {
				n = "ss-" + strconv.Itoa(*port)
			}
		}
		if *name != "" && !serverNameValid(*name) {
			return serverValidationError(&cli.Error{Code: "CONFIG_INVALID", Message: "servers[0].name must be 1 to 32 letters, digits, dots, underscores or hyphens", Exit: cli.ExitUsage})
		}
		result, e := serverAddConfig(home, cfg, ServerAddOptions{Name: n, Protocol: *protocol, Bind: *bind, Port: *port, Accounts: *account, Params: pm, PublicHost: *public, Default: *def})
		if e != nil {
			return e
		}
		cli.Success(stdout, map[string]any{"server": result.serverView})
		return nil
	case "update":
		return serverUpdateCommand(home, cfg, rest, stdout)
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
			host = nil
			format = nil
			qr = nil
		}
		pos, e := parseFlags(fs, rest)
		if e != nil || len(pos) != 1 {
			return usage()
		}
		n := pos[0]
		if _, i := cfg.GetServer(n); i < 0 {
			return serverNotFound()
		}
		switch sub {
		case "show":
			v, e := serverShowConfig(home, cfg, n)
			if e != nil {
				return e
			}
			cli.Success(stdout, map[string]any{"server": v})
			return nil
		case "remove":
			removed, removeErr := removeConfig(home, cfg, n)
			if removeErr != nil {
				return removeErr
			}
			_ = removed
			cli.Success(stdout, map[string]any{"removed": n})
			return nil
		case "rotate":
			rotated, rotateErr := rotateConfig(home, cfg, n, *acct)
			if rotateErr != nil {
				return rotateErr
			}
			_ = rotated
			cli.Success(stdout, map[string]any{"rotated": n, "account": *acct})
			return nil
		case "link":
			if *qr {
				return cli.Usage("server link --qr is unavailable: no QR encoder is included")
			}
			if *format != "uri" && *format != "mihomo" {
				return usage()
			}
			link, login, e := linkConfig(home, cfg, n, ServerLinkOptions{Account: *acct, Format: *format, Host: *host})
			if e != nil {
				return e
			}
			cli.Success(stdout, map[string]any{"name": n, "account": login, "format": *format, "link": link})
			return nil
		}
	}
	return cli.Usage("unknown server command %q", sub)
}
