package main

import (
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/servers"
)

func (d *doctorContext) checkServers() []doctorCheck {
	if d.cfgIssue != "" || d.cfg == nil {
		return nil
	}
	store := servers.NewStore(d.home)
	_, statErr := os.Stat(store.Path())
	if len(d.cfg.Servers) == 0 && errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	checks := []doctorCheck{}
	if err := store.Load(); err != nil {
		checks = append(checks, doctorCheck{"server-secrets", doctorFail, "server secrets file cannot be read"})
	} else if len(d.cfg.Servers) > 0 {
		info, err := os.Stat(store.Path())
		if err != nil {
			checks = append(checks, doctorCheck{"server-secrets-mode", doctorFail, "server-secrets.ktav cannot be inspected"})
		} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			checks = append(checks, doctorCheck{"server-secrets-mode", doctorFail, "server-secrets.ktav permissions must be 0600"})
		} else {
			checks = append(checks, doctorCheck{"server-secrets-mode", doctorOK, "server-secrets.ktav permissions are restricted"})
		}
	}
	if len(d.cfg.Servers) > 0 {
		configured := map[string]bool{}
		for _, spec := range d.cfg.Servers {
			configured[spec.Name] = true
		}
		for _, name := range store.Names() {
			if !configured[name] {
				checks = append(checks, doctorCheck{"server-secrets-orphans", doctorWarn, "server-secrets.ktav has orphan entries"})
				break
			}
		}
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
			if err := doctorListen(addr); err != nil {
				issues = append(issues, s.Name+" port is taken")
			}
		}
	}
	if len(issues) == 0 {
		checks = append(checks, doctorCheck{"servers", doctorOK, "configured server endpoints are available"})
	} else {
		checks = append(checks, doctorCheck{"servers", doctorWarn, strings.Join(issues, "; ")})
	}
	return checks
}
