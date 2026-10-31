package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/core"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

const (
	// connLogEvery is how often the connection reports are written.
	connLogEvery = time.Minute
	// connBeatEvery is how often the hourly summary of an account is written.
	connBeatEvery = time.Hour
	// slowAnswerMin and slowAnswerMedian: a node whose answers started after a
	// median of at least this long, over at least that many connections of a
	// minute, gets a line saying the answers are slow.
	slowAnswerMin    = 10
	slowAnswerMedian = 2 * time.Second
	// connWarnFailures: that many failed dials, or that many broken
	// connections (at least a twentieth of those that ended), in one period are
	// a warning; fewer are INFO.
	connWarnFailures = 3
)

// connLog turns what happens to the connections of the accounts (failed
// dials, connections that ended with an error from the node, sites bound to
// another node) and failed proxy logins into a few log lines per period: one
// per account and node and kind, only when something went wrong. A line holds
// the account, the node and counts of error kinds from a fixed list, never a
// site, an address, an error text or a password.
type connLog struct {
	info, warn logFunc
	// debug takes the detail that the summary lines leave out; nothing by default.
	debug logFunc
	// quiet, when it says true (the computer woke up a moment ago), turns the
	// warnings of a period into INFO lines; nil is never quiet.
	quiet func() bool

	mu    sync.Mutex
	nodes map[string]stats.NodeInfo
	dials map[loginNode]map[string]int // failed dials by kind
	ended map[loginNode]*endedTally
	moves map[siteMove]int
	// hour sums what the accounts did since the last hourly summary, so that a
	// quiet log can be told from a counter that does not work.
	hour map[string]*hourTally
	// failed proxy logins: configured logins by name, the rest only counted.
	loginFails    map[string]int
	unknownLogins int
}

// loginNode is what the lines of a period are kept by: the account and the
// node, and for failed dials the stage of the route the failure came from.
type loginNode struct{ login, label, hop string }

// hourTally counts the connections of one account for the hourly summary.
// empty ended before a byte moved either way (the client left), silent after
// the client had sent data and the node's side never answered.
// up and down are the bytes of the ended connections, so that a count of
// connections without an answer can be checked against what really moved.
type hourTally struct {
	ended, broken, empty, silent, dialsFailed int
	up, down                                  int64
	// what the connections without an answer have in common: the sites they
	// went to (hashes), their ports and how long they waited.
	sites  map[uint64]struct{}
	ports  map[uint16]int
	waited []time.Duration
	// how long the node took to start answering, of the connections that got
	// an answer.
	answers answerTimes
}

// answerTimes are the times the node took to start answering (see
// core.ConnEvent.Answer); an hour keeps at most answerCap of them.
type answerTimes []time.Duration

const answerCap = 8192

func (a *answerTimes) add(d time.Duration) {
	if len(*a) < answerCap {
		*a = append(*a, d)
	}
}

// median and p95 are the middle and the 95th percentile of the times; zero
// for none.
func (a answerTimes) median() time.Duration { return a.percentile(50) }
func (a answerTimes) p95() time.Duration    { return a.percentile(95) }

func (a answerTimes) percentile(p int) time.Duration {
	if len(a) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), a...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p*len(sorted)+99)/100 - 1
	return sorted[min(max(idx, 0), len(sorted)-1)]
}

// detail says how long the answers took to start: ", answers started after
// 0.4s median, 1.8s 95th"; nothing when no connection got one.
func (a answerTimes) detail() string {
	if len(a) == 0 {
		return ""
	}
	return fmt.Sprintf(", answers started after %s median, %s 95th", waitText(a.median()), waitText(a.p95()))
}

const (
	// silentSiteCap and silentWaitedCap bound what an hour keeps about the
	// connections without an answer.
	silentSiteCap   = 10000
	silentWaitedCap = 8192
)

// noteSilent records a connection that sent data and got no answer.
func (t *hourTally) noteSilent(ev core.ConnEvent) {
	t.silent++
	if t.sites == nil {
		t.sites, t.ports = map[uint64]struct{}{}, map[uint16]int{}
	}
	if len(t.sites) < silentSiteCap {
		t.sites[ev.Site] = struct{}{}
	}
	t.ports[ev.Port]++
	if len(t.waited) < silentWaitedCap {
		t.waited = append(t.waited, ev.Lifetime)
	}
}

// silentDetail says where the connections without an answer went and how long
// they waited, without naming a site: " (41 sites; ports 443 x340, 80 x23;
// waited 2.3s median, 15s longest)".
func (t *hourTally) silentDetail() string {
	if t.silent == 0 || len(t.waited) == 0 {
		return ""
	}
	sites := count(len(t.sites), "site", "sites")
	if len(t.sites) >= silentSiteCap {
		sites = fmt.Sprintf("more than %d sites", silentSiteCap)
	}
	ports := make([]uint16, 0, len(t.ports))
	for p := range t.ports {
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool {
		if t.ports[ports[i]] != t.ports[ports[j]] {
			return t.ports[ports[i]] > t.ports[ports[j]]
		}
		return ports[i] < ports[j]
	})
	var parts []string
	other := 0
	for i, p := range ports {
		if i < 3 {
			parts = append(parts, fmt.Sprintf("%d x%d", p, t.ports[p]))
		} else {
			other += t.ports[p]
		}
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("other x%d", other))
	}
	waited := append([]time.Duration(nil), t.waited...)
	sort.Slice(waited, func(i, j int) bool { return waited[i] < waited[j] })
	return fmt.Sprintf(" (%s; ports %s; waited %s median, %s longest)", sites, strings.Join(parts, ", "),
		waitText(waited[len(waited)/2]), waitText(waited[len(waited)-1]))
}

// waitText renders a wait: tenths of a second below 10 s, whole seconds above.
func waitText(d time.Duration) string {
	if d < 100*time.Millisecond {
		return "<0.1s"
	}
	if d = d.Round(100 * time.Millisecond); d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

type siteMove struct{ login, from, to, why string }

// endedTally counts the connections that ended through one node.
type endedTally struct {
	total, empty, silent int
	broken               map[string]int // by kind of the first error
	answers              answerTimes
}

func newConnLog(info, warn logFunc) *connLog {
	return &connLog{info: info, warn: warn, debug: func(string, ...any) {}, nodes: map[string]stats.NodeInfo{},
		dials: map[loginNode]map[string]int{}, ended: map[loginNode]*endedTally{},
		moves: map[siteMove]int{}, loginFails: map[string]int{}, hour: map[string]*hourTally{}}
}

// setNodes tells which nodes exist (for their names in the lines); a reload
// calls it again.
func (c *connLog) setNodes(infos []stats.NodeInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = make(map[string]stats.NodeInfo, len(infos))
	for _, n := range infos {
		c.nodes[n.Label] = n
	}
}

// event is the core.Options.OnConnEvent hook.
func (c *connLog) event(ev core.ConnEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := loginNode{login: ev.Login, label: ev.Label}
	tally := c.hour[ev.Login]
	if tally == nil && ev.Kind != core.ConnSiteMoved {
		tally = &hourTally{}
		c.hour[ev.Login] = tally
	}
	switch ev.Kind {
	case core.ConnDialFailed:
		tally.dialsFailed++
		key.hop = ev.Hop
		kinds := c.dials[key]
		if kinds == nil {
			kinds = map[string]int{}
			c.dials[key] = kinds
		}
		kinds[ev.Why]++
	case core.ConnStreamEnded:
		ended := c.ended[key]
		if ended == nil {
			ended = &endedTally{broken: map[string]int{}}
			c.ended[key] = ended
		}
		ended.total++
		tally.ended++
		tally.up += ev.Up
		tally.down += ev.Down
		if ev.Answer > 0 {
			ended.answers.add(ev.Answer)
			tally.answers.add(ev.Answer)
		}
		switch {
		case ev.Why != "":
			ended.broken[ev.Why]++
			tally.broken++
		case ev.Down == 0 && ev.Up == 0:
			ended.empty++
			tally.empty++
		case ev.Down == 0:
			ended.silent++
			tally.noteSilent(ev)
		}
	case core.ConnSiteMoved:
		c.moves[siteMove{ev.Login, ev.From, ev.Label, ev.Why}]++
	}
}

// loginFailed counts a failed proxy login; known says that the login is one
// of the configured accounts, only those are named.
func (c *connLog) loginFailed(login string, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if known {
		c.loginFails[login]++
		return
	}
	c.unknownLogins++
}

// wrapAuth makes the authenticator report its failures to the log.
func (c *connLog) wrapAuth(a mihauth.Authenticator) mihauth.Authenticator {
	if a == nil {
		return nil
	}
	return &loggedAuth{inner: a, log: c}
}

type loggedAuth struct {
	inner mihauth.Authenticator
	log   *connLog
}

func (a *loggedAuth) Verify(user, pass string) bool {
	return a.report(user, a.inner.Verify(user, pass))
}

// VerifyFrom keeps the inner authenticator's check of the client address (the
// per-account allowed_clients rule, which must refuse before the password is
// verified): Mihomo finds it by an interface assertion, which a wrapper would
// otherwise hide.
func (a *loggedAuth) VerifyFrom(user, pass string, remote net.Addr) bool {
	return a.report(user, mihauth.VerifyWithRemote(a.inner, user, pass, remote))
}

// report counts a failed login and passes the result on.
func (a *loggedAuth) report(user string, ok bool) bool {
	if ok {
		return true
	}
	known := false
	for _, u := range a.inner.Users() {
		if u == user {
			known = true
			break
		}
	}
	a.log.loginFailed(user, known)
	return false
}

func (a *loggedAuth) Users() []string { return a.inner.Users() }

// run writes the reports every period, the summary of the accounts every beat
// and the last period when ctx ends.
func (c *connLog) run(ctx context.Context, every, beat time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	beats := time.NewTicker(beat)
	defer beats.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush()
			return
		case <-ticker.C:
			c.flush()
		case <-beats.C:
			c.heartbeat()
		}
	}
}

// heartbeat writes one INFO line per account that had connections or failed
// dials since the last one: with it a log without a report of broken
// connections says "none" instead of saying nothing.
func (c *connLog) heartbeat() {
	c.mu.Lock()
	hour := c.hour
	c.hour = map[string]*hourTally{}
	c.mu.Unlock()
	logins := make([]string, 0, len(hour))
	for login, t := range hour {
		if t.ended > 0 || t.dialsFailed > 0 {
			logins = append(logins, login)
		}
	}
	sort.Strings(logins)
	for _, login := range logins {
		t := hour[login]
		c.info("account %s: last hour: %s ended, %d broken, %d closed before any data, %d without an answer%s, %d dials failed, %s down, %s up%s",
			login, count(t.ended, "connection", "connections"), t.broken, t.empty, t.silent, t.silentDetail(), t.dialsFailed,
			humanBytes(t.down), humanBytes(t.up), t.answers.detail())
	}
}

// humanBytes renders a byte count as B, KiB, MiB or GiB.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 2; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMG"[exp])
}

// flush writes the lines of the period and starts a new one.
func (c *connLog) flush() {
	c.mu.Lock()
	dials, ended, moves := c.dials, c.ended, c.moves
	fails, unknown := c.loginFails, c.unknownLogins
	nodes := c.nodes
	c.dials, c.ended, c.moves = map[loginNode]map[string]int{}, map[loginNode]*endedTally{}, map[siteMove]int{}
	c.loginFails, c.unknownLogins = map[string]int{}, 0
	c.mu.Unlock()

	describe := func(label string) string {
		n, ok := nodes[label]
		if !ok {
			return label
		}
		country := n.Country
		if country == "" {
			country = unknownCountry
		}
		if n.Name != "" {
			return fmt.Sprintf("%s (%s, country %s)", label, n.Name, country)
		}
		return fmt.Sprintf("%s (country %s)", label, country)
	}

	for _, key := range sortedKeys(dials) {
		kinds := dials[key]
		total := sumCounts(kinds)
		line := fmt.Sprintf("account %s via %s%s: %s failed (%s)", key.login, describe(key.label), hopText(key.hop), count(total, "dial", "dials"), joinKinds(kinds))
		c.level(total >= connWarnFailures)("%s", line)
	}
	for _, key := range sortedKeys(ended) {
		tally := ended[key]
		if n := len(tally.answers); n >= slowAnswerMin && tally.answers.median() >= slowAnswerMedian {
			c.info("account %s via %s: slow answers: started after %s median, %s 95th, over %s", key.login, describe(key.label),
				waitText(tally.answers.median()), waitText(tally.answers.p95()), count(n, "connection", "connections"))
		}
		broken := sumCounts(tally.broken)
		if broken == 0 {
			continue
		}
		line := fmt.Sprintf("account %s via %s: %s ended, %d broken (%s)", key.login, describe(key.label),
			count(tally.total, "connection", "connections"), broken, joinKinds(tally.broken))
		if tally.empty > 0 {
			line += fmt.Sprintf(", %d closed before any data", tally.empty)
		}
		if tally.silent > 0 {
			line += fmt.Sprintf(", %d without an answer", tally.silent)
		}
		c.level(broken >= connWarnFailures && broken*20 >= tally.total)("%s", line)
	}
	c.writeMoves(moves, describe)
	if failed := sumCounts(fails) + unknown; failed > 0 {
		var parts []string
		for _, login := range sortedStrings(fails) {
			parts = append(parts, fmt.Sprintf("%s x%d", login, fails[login]))
		}
		if unknown > 0 {
			parts = append(parts, fmt.Sprintf("unknown logins x%d", unknown))
		}
		c.warn("%s failed (%s)", count(failed, "proxy login", "proxy logins"), strings.Join(parts, ", "))
	}
}

// writeMoves writes the moved sites of the period. Moves of one account for one
// reason between a single pair of nodes are one line naming both; between
// several pairs (a node went down and its sites were spread over the rest) they
// are one line with the counts, and the pairs go to DEBUG.
func (c *connLog) writeMoves(moves map[siteMove]int, describe func(string) string) {
	type group struct {
		login, why string
		pairs      []siteMove
	}
	var groups []*group
	byKey := map[[2]string]*group{}
	keys := make([]siteMove, 0, len(moves))
	for k := range moves {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		g := byKey[[2]string{k.login, k.why}]
		if g == nil {
			g = &group{login: k.login, why: k.why}
			byKey[[2]string{k.login, k.why}] = g
			groups = append(groups, g)
		}
		g.pairs = append(g.pairs, k)
	}
	for _, g := range groups {
		serious := g.why == core.MoveNodeDown
		if len(g.pairs) == 1 {
			k := g.pairs[0]
			c.level(serious)("account %s: %s moved from %s to %s: %s", k.login, count(moves[k], "site", "sites"),
				describe(k.from), describe(k.to), k.why)
			continue
		}
		total := 0
		from, to := map[string]struct{}{}, map[string]struct{}{}
		for _, k := range g.pairs {
			total += moves[k]
			from[k.from], to[k.to] = struct{}{}, struct{}{}
			c.debug("account %s: %s moved from %s to %s: %s", k.login, count(moves[k], "site", "sites"),
				describe(k.from), describe(k.to), k.why)
		}
		nodes := func(set map[string]struct{}) string {
			for label := range set {
				if len(set) == 1 {
					return describe(label)
				}
			}
			return count(len(set), "node", "nodes")
		}
		c.level(serious)("account %s: %s moved from %s to %s: %s", g.login, count(total, "site", "sites"),
			nodes(from), nodes(to), g.why)
	}
}

// level picks the writer of a line: WARN when serious, INFO otherwise, and
// always INFO right after the computer woke up.
func (c *connLog) level(serious bool) logFunc {
	if serious && (c.quiet == nil || !c.quiet()) {
		return c.warn
	}
	return c.info
}

// hopText names the stage of the route a failure came from after the node
// of a line: ", post hop"; nothing when the route is only a node.
func hopText(hop string) string {
	if hop == "" {
		return ""
	}
	return ", " + hop
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, n := range m {
		total += n
	}
	return total
}

func sortedStrings(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[loginNode]V) []loginNode {
	out := make([]loginNode, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].login != out[j].login {
			return out[i].login < out[j].login
		}
		if out[i].label != out[j].label {
			return out[i].label < out[j].label
		}
		return out[i].hop < out[j].hop
	})
	return out
}
