package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/daemon"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// doctorStatus values of a check.
const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
)

// doctorLogWindow is how far back the log check looks.
const doctorLogWindow = time.Hour

// doctorCheck is one entry of the doctor document.
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// doctorSummary counts the checks by status.
type doctorSummary struct {
	Ok   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// doctorNow is a seam for tests: the time the checks read as now.
var doctorNow = func() time.Time { return time.Now().UTC() }

// doctorListen is a seam for tests: it reports whether the address can be
// bound right now. The default binds and closes a real listener.
var doctorListen = func(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// runDoctor implements `doctor`: one pass of independent checks, one document
// on stdout, and DOCTOR_FAILED (exit 7) when a check failed. It never talks
// to anything but the local daemon and the local port, and never prints a
// subscription URL, a password or a hash.
func runDoctor(args []string, stdout io.Writer) error {
	home, rest, err := parseHomeArgs("doctor", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return cli.Usage("doctor: unexpected argument %q", rest[0])
	}
	checks := doctorRun(home)
	summary := doctorSummary{}
	for _, c := range checks {
		switch c.Status {
		case doctorFail:
			summary.Fail++
		case doctorWarn:
			summary.Warn++
		default:
			summary.Ok++
		}
	}
	// stdout always carries the document; the error (if any) goes to stderr.
	cli.Success(stdout, map[string]any{"checks": checks, "summary": summary})
	if summary.Fail > 0 {
		return &cli.Error{
			Code:    "DOCTOR_FAILED",
			Message: fmt.Sprintf("%d check(s) failed", summary.Fail),
			Hint:    "see the failed checks above",
			Exit:    cli.ExitCheckFailed,
		}
	}
	return nil
}

// doctorContext carries what the checks share: the state directory, the
// loaded configuration and the daemon probe result.
type doctorContext struct {
	home     string
	cfg      *config.Config
	cfgIssue string // why the configuration could not be used, "" when fine
	now      time.Time
	daemonUp bool
	status   daemon.Status
	health   map[string]daemon.NodeHealth // live node health by label, when the daemon runs
}

// doctorRun performs every check once, in document order.
func doctorRun(home string) []doctorCheck {
	d := &doctorContext{home: home, now: doctorNow()}
	d.probeDaemon()
	checks := []doctorCheck{
		d.checkConfig(),
		d.checkDaemon(),
		d.checkListen(),
		d.checkSubscriptions(),
	}
	for i := range d.cfgAccounts() {
		checks = append(checks, d.checkAccount(&d.cfg.Accounts[i]))
	}
	return append(checks, d.checkLog())
}

// cfgAccounts returns the configured accounts; empty when the configuration
// could not be loaded.
func (d *doctorContext) cfgAccounts() []config.Account {
	if d.cfg == nil {
		return nil
	}
	return d.cfg.Accounts
}

// probeDaemon asks the local control channel once; any failure means down.
func (d *doctorContext) probeDaemon() {
	ctx, cancel := context.WithTimeout(context.Background(), daemon.ProbeTimeout)
	defer cancel()
	_, st, err := daemonProbeLive(ctx, d.home)
	if err != nil {
		return
	}
	d.daemonUp = true
	d.status = st
	d.health = make(map[string]daemon.NodeHealth)
	if st.Warm != nil {
		for _, n := range st.Warm.Nodes {
			if n.Label != "" {
				d.health[n.Label] = n
			}
		}
	}
}

// checkConfig loads config.ktav with the same loader as every command; the
// message names the bad key, never a secret.
func (d *doctorContext) checkConfig() doctorCheck {
	cfg, err := loadConfig(d.home)
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			d.cfgIssue = ce.Message
		} else {
			d.cfgIssue = err.Error()
		}
		return doctorCheck{"config", doctorFail, d.cfgIssue}
	}
	d.cfg = cfg
	return doctorCheck{"config", doctorOK, "config.ktav loaded and valid"}
}

// checkDaemon reports the running daemon, or a plain warn.
func (d *doctorContext) checkDaemon() doctorCheck {
	if d.daemonUp {
		return doctorCheck{"daemon", doctorOK, fmt.Sprintf("running, pid %d, uptime %ds", d.status.PID, uptimeSeconds(d.status.StartedAt))}
	}
	return doctorCheck{"daemon", doctorWarn, "daemon is not running"}
}

// checkListen reports the running daemon's listen address, or, when nothing
// runs, whether the configured port is free (a local probe only).
func (d *doctorContext) checkListen() doctorCheck {
	if d.daemonUp {
		return doctorCheck{"listen", doctorOK, "listen " + d.status.Listen + ", allowed_clients " + allowedClientsLabel(d.status.AllowedClients)}
	}
	if d.cfgIssue != "" {
		return doctorCheck{"listen", doctorWarn, "not checked: the configuration is invalid"}
	}
	bind, port := d.cfg.Listen.Bind, d.cfg.Listen.Port
	addr := net.JoinHostPort(bind, strconv.Itoa(port))
	if port == 0 {
		return doctorCheck{"listen", doctorOK, "configured port is 0 (any free port)"}
	}
	if err := doctorListen(addr); err != nil {
		return doctorCheck{"listen", doctorWarn, addr + " is taken"}
	}
	return doctorCheck{"listen", doctorOK, addr + " is free"}
}

// checkSubscriptions reads the stored snapshots: one must exist per
// configured subscription and not be older than twice the update interval
// (skipped when the interval is "off"). It also fails when an account names
// a subscription that is not configured; that part is config level and runs
// even when the daemon is down.
func (d *doctorContext) checkSubscriptions() doctorCheck {
	if d.cfgIssue != "" {
		return doctorCheck{"subscriptions", doctorWarn, "not checked: the configuration is invalid"}
	}
	known := make(map[string]bool, len(d.cfg.Subscriptions))
	for _, s := range d.cfg.Subscriptions {
		known[s.Name] = true
	}
	interval, refreshOn := d.cfg.Fetch.Interval()
	limit := 2 * interval
	store := subscription.NewStore(filepath.Join(d.home, "subscriptions"))
	var reasons []string
	status := doctorOK
	for _, s := range d.cfg.Subscriptions {
		snap, err := store.Load(s.Name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			reasons = append(reasons, "snapshot of "+s.Name+" is not stored; run netivri sub update")
			status = doctorWarn
		case err != nil:
			reasons = append(reasons, "snapshot of "+s.Name+" cannot be read")
			status = doctorWarn
		case snap.FetchedAt.IsZero():
			reasons = append(reasons, "snapshot of "+s.Name+" has no fetch time, its age is unknown")
			status = doctorWarn
		case refreshOn && d.now.Sub(snap.FetchedAt) > limit:
			reasons = append(reasons, fmt.Sprintf("snapshot of %s is %s old (update interval %s)",
				s.Name, d.now.Sub(snap.FetchedAt).Truncate(time.Second), interval))
			status = doctorWarn
		}
	}
	for _, a := range d.cfgAccounts() {
		providers, _ := routeLists(a)
		for _, p := range providers {
			if !known[p] {
				reasons = append(reasons, "account "+a.Login+": provider "+p+" is not a configured subscription")
				status = doctorFail
			}
		}
	}
	if len(reasons) == 0 {
		detail := "every configured subscription has a usable snapshot"
		if len(d.cfg.Subscriptions) == 0 {
			detail = "no subscriptions configured"
		}
		return doctorCheck{"subscriptions", doctorOK, detail}
	}
	return doctorCheck{"subscriptions", status, strings.Join(reasons, "; ")}
}

// checkAccount checks one account's route: at least one stored node always,
// and, with a running daemon, a live node in the first tier that has nodes
// (a warn when only a later tier is alive, a fail when none is). Without a
// daemon the node count stands and the liveness part is a warn.
func (d *doctorContext) checkAccount(a *config.Account) doctorCheck {
	name := "account " + a.Login
	providers, countries := routeLists(*a)
	if len(providers) == 0 && len(countries) == 0 {
		return doctorCheck{name, doctorOK, "direct"}
	}
	if d.cfgIssue != "" {
		return doctorCheck{name, doctorWarn, "not checked: the configuration is invalid"}
	}
	tiers, err := accountShowTiers(d.home, d.cfg, providers, countries)
	if err != nil {
		return doctorCheck{name, doctorWarn, "route not checked: " + err.Error()}
	}
	total := 0
	for _, t := range tiers {
		total += len(t.Nodes)
	}
	if total == 0 {
		ne, ok := noNodesError(providers, countries).(*cli.Error)
		if !ok {
			return doctorCheck{name, doctorFail, "no nodes available on the route"}
		}
		return doctorCheck{name, doctorFail, ne.Message}
	}
	first := -1
	for i, t := range tiers {
		if len(t.Nodes) > 0 {
			first = i
			break
		}
	}
	if !d.daemonUp {
		return doctorCheck{name, doctorWarn, fmt.Sprintf("%d stored node(s); liveness not checked (daemon is not running)", total)}
	}
	if !d.routeProbed(tiers) {
		return doctorCheck{name, doctorWarn, fmt.Sprintf("%d stored node(s); no probe data yet (the daemon has not probed the route)", total)}
	}
	aliveIn := func(i int) int {
		n := 0
		for _, node := range tiers[i].Nodes {
			if h, ok := d.health[node.Label]; ok && h.Alive {
				n++
			}
		}
		return n
	}
	tierName := func(i int) string {
		return tiers[i].Country + " (" + strings.Join(tiers[i].Providers, ",") + ")"
	}
	if n := aliveIn(first); n > 0 {
		return doctorCheck{name, doctorOK, fmt.Sprintf("tier %d %s: %d/%d node(s) alive", tiers[first].Tier, tierName(first), n, len(tiers[first].Nodes))}
	}
	for i := first + 1; i < len(tiers); i++ {
		if len(tiers[i].Nodes) == 0 {
			continue
		}
		if n := aliveIn(i); n > 0 {
			return doctorCheck{name, doctorWarn, fmt.Sprintf("no live node in tier %d %s; alive only in fallback tier %d %s",
				tiers[first].Tier, tierName(first), tiers[i].Tier, tierName(i))}
		}
	}
	return doctorCheck{name, doctorFail, "no live node on the route"}
}

// routeProbed reports whether the daemon's warm-up knows any node of the
// route; before the first probe round a not-alive answer means nothing.
func (d *doctorContext) routeProbed(tiers []accountShowTier) bool {
	for _, t := range tiers {
		for _, n := range t.Nodes {
			if _, ok := d.health[n.Label]; ok {
				return true
			}
		}
	}
	return false
}

// checkLog reads the daemon log for WARN and ERROR entries of the last hour.
// It reads the file, so it works while the daemon runs and after it stopped;
// a missing log file is fine.
func (d *doctorContext) checkLog() doctorCheck {
	q := daemon.LogQuery{MinLevel: daemon.LevelWarn, Since: d.now.Add(-doctorLogWindow)}
	entries, _, err := daemon.ReadLog(d.home, q)
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{"log", doctorOK, "no log yet"}
	}
	if err != nil {
		return doctorCheck{"log", doctorWarn, "log cannot be read: " + err.Error()}
	}
	if len(entries) == 0 {
		return doctorCheck{"log", doctorOK, "no warn or error entries in the last hour"}
	}
	return doctorCheck{"log", doctorWarn, fmt.Sprintf("%d warn/error entries in the last hour; newest: %s", len(entries), entries[len(entries)-1].Message)}
}
