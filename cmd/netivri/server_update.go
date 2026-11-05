package main

import (
	"flag"
	"io"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

// serverUpdateCommand changes fields of an existing server. A field that is
// not given stays as it is; secrets are not touched.
func serverUpdateCommand(home string, cfg *config.Config, rest []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("server update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	public := fs.String("public-host", "", "host or address put into client links")
	bind := fs.String("bind", "", "bind address")
	port := fs.Int("port", 0, "port")
	account := fs.String("account", "", "accounts (comma-separated logins, or *)")
	var params serverParamFlags
	fs.Var(&params, "param", "key=value")
	pos, e := parseFlags(fs, rest)
	if e != nil || len(pos) != 1 {
		return cli.Usage("server update: requires NAME and at least one field to change")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if len(given) == 0 {
		return cli.Usage("server update: requires at least one of --public-host, --bind, --port, --account, --param")
	}
	var o ServerUpdateOptions
	if given["public-host"] {
		o.PublicHost = public
	}
	if given["bind"] {
		o.Bind = bind
	}
	if given["port"] {
		if *port < 1 || *port > 65535 {
			return cli.Usage("server update: --port must be 1 to 65535")
		}
		o.Port = port
	}
	if given["account"] {
		if *account != "*" {
			for _, a := range strings.Split(*account, ",") {
				if !config.ValidAccountLogin(a) {
					return serverValidationError(&cli.Error{Code: "CONFIG_INVALID", Message: "servers[0].accounts contains an invalid account login", Exit: cli.ExitUsage})
				}
			}
		}
		o.Accounts = account
	}
	if len(params) > 0 {
		o.Params = map[string]string{}
		for _, raw := range params {
			k, v, ok := strings.Cut(raw, "=")
			if !ok || k == "" {
				return cli.Usage("server update --param requires key=value")
			}
			if _, dup := o.Params[k]; dup {
				return cli.Usage("server update: duplicate --param key")
			}
			o.Params[k] = v
		}
	}
	if _, i := cfg.GetServer(pos[0]); i < 0 {
		return serverNotFound()
	}
	view, e := serverUpdateConfig(home, cfg, pos[0], o)
	if e != nil {
		return e
	}
	cli.Success(stdout, map[string]any{"server": view})
	return nil
}
