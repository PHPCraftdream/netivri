package main

import (
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

var doctorServerListenTCP = func(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

var doctorServerListenUDP = func(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	return pc.Close()
}

func doctorInvalidClassifierConfig(home string) []doctorCheck {
	_, err := config.Load(config.ConfigPath(home))
	if err == nil {
		return nil
	}
	var ce *cli.Error
	if !errors.As(err, &ce) {
		return nil
	}
	switch ce.Code {
	case "SERVER_PORT_AMBIGUOUS":
		return []doctorCheck{{"servers", doctorFail, "configured server classifier is ambiguous; " + ce.Hint}}
	case "PORT_IN_USE":
		return []doctorCheck{{"servers", doctorFail, "configured server endpoints conflict"}}
	default:
		return nil
	}
}

func (d *doctorContext) checkServers() []doctorCheck {
	if d.cfgIssue != "" || d.cfg == nil {
		return doctorInvalidClassifierConfig(d.home)
	}
	store := servers.NewStore(d.home)
	_, statErr := os.Stat(store.Path())
	if len(d.cfg.Servers) == 0 && errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	checks := []doctorCheck{}
	checks = append(checks, d.serverSecretsChecks(store)...)
	if len(d.cfg.Servers) > 0 {
		checks = append(checks, d.orphanSecretChecks(store)...)
	}
	if len(d.cfg.Servers) == 0 {
		if len(store.Names()) > 0 {
			checks = append(checks, doctorCheck{"server-secrets", doctorWarn, "server-secrets.ktav contains orphan entries; remove or reconfigure servers"})
		}
		return checks
	}
	resolved, err := d.cfg.ResolveServers()
	if err != nil {
		return append(checks, doctorCheck{"servers", doctorFail, "configured servers are invalid"})
	}
	appliedSpecs, ok := applyServerSecrets(store, resolved)
	if !ok {
		return append(checks, doctorCheck{"server-secrets", doctorFail, "configured server secrets are unavailable"})
	}
	if msg := sharedPortProblem(appliedSpecs); msg != "" {
		return append(checks, doctorCheck{"servers", doctorFail, msg})
	}
	issues := d.serverIssues(store, resolved)
	if d.angelUp {
		checks = append(checks, serverCounterChecks(d.status, resolved)...)
	}
	if len(issues) == 0 {
		checks = append(checks, doctorCheck{"servers", doctorOK, "configured server endpoints are available"})
	} else {
		checks = append(checks, doctorCheck{"servers", doctorWarn, strings.Join(issues, "; ")})
	}
	return checks
}

// serverSecretsChecks reports an unreadable secrets file, and when servers are
// configured, a secrets file that is not restricted to its owner.
func (d *doctorContext) serverSecretsChecks(store *servers.Store) []doctorCheck {
	if err := store.Load(); err != nil {
		return []doctorCheck{{"server-secrets", doctorFail, "server secrets file cannot be read"}}
	}
	if len(d.cfg.Servers) == 0 {
		return nil
	}
	info, err := os.Stat(store.Path())
	switch {
	case err != nil:
		return []doctorCheck{{"server-secrets-mode", doctorFail, "server-secrets.ktav cannot be inspected"}}
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0:
		return []doctorCheck{{"server-secrets-mode", doctorFail, "server-secrets.ktav permissions must be 0600"}}
	default:
		return []doctorCheck{{"server-secrets-mode", doctorOK, "server-secrets.ktav permissions are restricted"}}
	}
}

// orphanSecretChecks warns when the secrets file holds an entry for a server
// that is not configured.
func (d *doctorContext) orphanSecretChecks(store *servers.Store) []doctorCheck {
	configured := map[string]bool{}
	for _, spec := range d.cfg.Servers {
		configured[spec.Name] = true
	}
	for _, name := range store.Names() {
		if !configured[name] {
			return []doctorCheck{{"server-secrets-orphans", doctorWarn, "server-secrets.ktav has orphan entries"}}
		}
	}
	return nil
}

// applyServerSecrets applies the stored secrets to each resolved server; ok is
// false when one of them has no usable entry.
func applyServerSecrets(store *servers.Store, resolved []servers.Spec) (applied []servers.Spec, ok bool) {
	applied = make([]servers.Spec, 0, len(resolved))
	for _, spec := range resolved {
		a, applyErr := store.Apply(spec)
		if applyErr != nil {
			return nil, false
		}
		applied = append(applied, a)
	}
	return applied, true
}

// sharedPortProblem returns why servers that share an endpoint are ambiguous,
// or "" when none is.
func sharedPortProblem(specs []servers.Spec) string {
	groups := map[string][]servers.Spec{}
	for _, spec := range specs {
		key := net.JoinHostPort(spec.Bind, strconv.Itoa(spec.Port))
		proto, _ := servers.Lookup(spec.Protocol)
		key += "/" + strconv.FormatBool(servers.IsUDPBound(proto))
		groups[key] = append(groups[key], spec)
	}
	for _, group := range groups {
		if len(group) > 1 || len(group) == 1 && group[0].Default {
			if err := servers.ValidateSharedPort(group); err != nil {
				return "configured server sharing is ambiguous"
			}
			if err := servers.ValidateTrialSecrets(group); err != nil {
				return "configured server credentials are ambiguous"
			}
		}
	}
	return ""
}

// serverIssues lists the problems of the resolved servers in order; the ports
// are probed only while the core is down.
func (d *doctorContext) serverIssues(store *servers.Store, resolved []servers.Spec) []string {
	var issues []string
	for _, s := range resolved {
		ip := net.ParseIP(s.Bind)
		if ip == nil || !ip.IsLoopback() {
			issues = append(issues, s.Name+" binds publicly (use --bind 127.0.0.1 to limit it locally)")
		}
		for _, login := range s.Accounts {
			account, _ := d.cfg.Account(login)
			if account == nil && d.cfg.Auth.Login != login {
				issues = append(issues, s.Name+" references an unknown account")
			}
		}
		if _, err := store.Apply(s); err != nil {
			issues = append(issues, s.Name+" has no usable secrets entry")
		}
		if !d.angelUp {
			addr := net.JoinHostPort(s.Bind, strconv.Itoa(s.Port))
			probe := doctorServerListenTCP
			if proto, ok := servers.Lookup(s.Protocol); ok && servers.IsUDPBound(proto) {
				probe = doctorServerListenUDP
			}
			if err := probe(addr); err != nil {
				issues = append(issues, s.Name+" port is taken")
			}
		}
	}
	return issues
}
