package main

import (
	"context"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// Error codes specific to the sub/node commands (docs/cli.md).
const (
	codeSubNotFound     = "SUB_NOT_FOUND"
	codeSubUpdateFailed = "SUB_UPDATE_FAILED"
	codeSubExistsName   = "SUB_EXISTS_NAME"
)

// subNameRe mirrors internal/subscription's stored-name rule
// (^[A-Za-z0-9._-]{1,64}$, not "." or ".."); keep them in sync.
var subNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validSubName(name string) bool {
	return subNameRe.MatchString(name) && name != "." && name != ".."
}

// subApp carries the dependencies of the sub/node commands so tests can
// inject a fake fetcher and a fixed clock.
type subApp struct {
	fetch  func(ctx context.Context, rawURL string, o fetch.Options) (*fetch.Result, error)
	now    func() time.Time
	stdout io.Writer
	home   string
}

// subFetch is the production fetcher; tests swap it to avoid the network.
var subFetch = fetch.Get

func newSubApp(home string, stdout io.Writer) *subApp {
	return &subApp{
		fetch:  subFetch,
		now:    time.Now,
		stdout: stdout,
		home:   home,
	}
}

func (a *subApp) configPath() string { return config.ConfigPath(a.home) }

func (a *subApp) store() *subscription.Store {
	return subscription.NewStore(filepath.Join(a.home, "subscriptions"))
}

// load reads the config under home; a missing file yields the defaults
// and no file is created.
func (a *subApp) load() (*config.Config, error) { return loadConfig(a.home) }

// runSub dispatches the `sub` subcommands.
func runSub(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return cli.Usage("sub: missing subcommand (add, list, update, rm, countries)")
	}
	home, rest, err := parseHomeArgs("sub "+args[0], args[1:])
	if err != nil {
		return err
	}
	app := newSubApp(home, stdout)
	switch args[0] {
	case "add":
		return app.add(rest)
	case "list":
		return app.list()
	case "update":
		return app.update(rest)
	case "countries":
		return app.countries(rest)
	case "rm":
		return app.rm(rest)
	default:
		return cli.Usage("unknown sub command %q", args[0])
	}
}

// parseHomeArgs extracts the global --home and --local flags and strips the
// --json, --quiet and --no-color compatibility flags (output is always JSON
// without ANSI; --quiet suppresses nothing today). Everything else, command
// flags and positional arguments alike, is returned untouched and in order for
// the subcommand's own parser. Home resolves exactly like `up` (resolveHome).
// A bare "--" ends the scan.
func parseHomeArgs(name string, args []string) (string, []string, error) {
	home, _, rest, err := parseHomeArgsFrom(name, args)
	return home, rest, err
}

// parseHomeArgsFrom is parseHomeArgs that also says where the state directory
// came from (see resolveHome).
func parseHomeArgsFrom(name string, args []string) (home, source string, rest []string, err error) {
	var flagHome string
	var local bool
scan:
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest = append(rest, args[i:]...)
			break scan
		case a == "--home" || a == "-home":
			if i+1 >= len(args) {
				return "", "", nil, cli.Usage("%s: flag needs an argument: --home", name)
			}
			flagHome = args[i+1]
			i++
		case strings.HasPrefix(a, "--home="):
			flagHome = strings.TrimPrefix(a, "--home=")
		case strings.HasPrefix(a, "-home="):
			flagHome = strings.TrimPrefix(a, "-home=")
		case a == "--local" || a == "-local":
			local = true
		case strings.HasPrefix(a, "--local=") || strings.HasPrefix(a, "-local="):
			_, value, _ := strings.Cut(a, "=")
			v, err := strconv.ParseBool(value)
			if err != nil {
				return "", "", nil, cli.Usage("%s: invalid value %q for flag --local", name, value)
			}
			local = v
		case a == "--json" || a == "-json" || a == "--quiet" || a == "-quiet" ||
			a == "--no-color" || a == "-no-color":
			// Accepted for compatibility, no effect.
		default:
			rest = append(rest, a)
		}
	}
	home, source, err = resolveHome(flagHome, local, os.Getenv, os.UserConfigDir)
	if err != nil {
		return "", "", nil, err
	}
	return home, source, rest, nil
}

// parseFlags parses flags that may also appear after positional
// arguments (Go's flag package stops at the first non-flag token) and
// returns the positional arguments in order.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	rest := args
	var positional []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}
	return positional, nil
}

// deriveName builds a subscription name from the URL host: lower-case,
// characters outside [A-Za-z0-9._-] become '-', truncated to 64 (the
// stored-name limit), and made unique among the configured names with a
// numeric suffix.
func deriveName(rawURL string, subs []config.Subscription) string {
	host := ""
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		host = u.Hostname()
	}
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base := b.String()
	if len(base) > 64 {
		base = base[:64]
	}
	if base == "." || base == ".." {
		base = "subscription"
	}
	if base == "" {
		base = "subscription"
	}
	taken := make(map[string]bool, len(subs))
	for _, s := range subs {
		taken[s.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		sfx := "-" + strconv.Itoa(i)
		cut := 64 - len(sfx)
		if cut > len(base) {
			cut = len(base)
		}
		name = base[:cut] + sfx
	}
	return name
}

// proxyFor picks the fetch proxy: an explicit --via wins over the
// configured fetch.proxy; there is no fallback to direct and no
// environment proxy (fetch.Options.Proxy == "" is an explicit direct
// connection inside internal/fetch).
func proxyFor(via, cfgProxy string) string {
	if via != "" {
		return via
	}
	return cfgProxy
}
