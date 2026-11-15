//go:build e2e

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestE2ESnellLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &e2eSnellState{ctx: ctx}
	setupSnellServerA(t, st)
	aliceURL, bobURL := setupSnellFeeds(t, st)
	setupSnellClientB(t, st, aliceURL, bobURL)
	aliceLink := checkSnellInitialRoutes(t, st)
	rotatedLink := rotateSnellAlice(t, st, aliceLink)
	updateSnellSubscription(t, st)
	restrictSnellAlice(t, st, rotatedLink)
	clearSnellRestriction(t, st)
	restartSnellServer(t, st, rotatedLink)
	removeSnellServices(t, st)
}

// e2eSnellState is the shared state of TestE2ESnellLifecycle. secretCheck grows
// as PSKs are created, and the cleanups read the current angels at teardown.
type e2eSnellState struct {
	ctx         context.Context
	pass        string
	backend     string
	aliceUp     *accountsUpstream
	bobUp       *accountsUpstream
	homeA       string
	homeB       string
	ports       []int
	secretCheck []string
	aliceFeed   *e2eSnellFeed
	serverProc  *upProc
	clientProc  *upProc
}

// setupSnellServerA writes A's fixture, starts A's angel and adds one Snell
// edge per account.
func setupSnellServerA(t *testing.T, st *e2eSnellState) {
	t.Helper()
	const pass = "snell-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	st.pass = pass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "alice-route"}, {Login: "bob", Password: hash, Provider: "bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "alice-route", URL: "http://alice.invalid/sub"}, {Name: "bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", st.bobUp)})
	st.serverProc = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverProc.Status())
	st.secretCheck = []string{pass, hash}
	t.Cleanup(func() {
		st.serverProc.Stop()
		assertNoSecrets(t, st.serverProc.Stdout(), st.serverProc.Stderr(), st.secretCheck...)
	})
	st.ports = []int{freePort(t)}
	for i, account := range []string{"alice", "bob"} {
		name := "snell-" + account
		code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "snell", "--name", name, "--account", account, "--port", strconv.Itoa(st.ports[i]), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "version=2", "--param", "obfs=none")
		if code != 0 {
			t.Fatalf("Snell server add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secretCheck...)
		waitStatusReload(t, st.ctx, st.homeA, uint64(i+1))
		if i == 0 {
			st.ports = append(st.ports, freePort(t))
		}
	}
	for _, name := range []string{"snell-alice", "snell-bob"} {
		e2eSnellWaitStatus(t, st.ctx, st.homeA, name, true)
	}
}

// getSnellLink returns the mihomo Snell link of account on the named edge.
func getSnellLink(t *testing.T, st *e2eSnellState, name, account string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", name, "--account", account, "--format", "mihomo")
	if code != 0 {
		t.Fatalf("Snell link failed: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("Snell link response invalid")
	}
	assertNoSecrets(t, stderr, stderr, st.secretCheck...)
	return env.Data.Link
}

// setupSnellFeeds serves alice's and bob's links as subscription feeds and
// returns their URLs.
func setupSnellFeeds(t *testing.T, st *e2eSnellState) (string, string) {
	t.Helper()
	st.aliceFeed = &e2eSnellFeed{body: getSnellLink(t, st, "snell-alice", "alice")}
	bobFeed := &e2eSnellFeed{body: getSnellLink(t, st, "snell-bob", "bob")}
	st.secretCheck = append(st.secretCheck, e2eSnellPSK(t, st.aliceFeed.get()), e2eSnellPSK(t, bobFeed.get()))
	return st.aliceFeed.start(t), bobFeed.start(t)
}

// setupSnellClientB imports both feeds into B, adds one account per provider
// and starts B's angel.
func setupSnellClientB(t *testing.T, st *e2eSnellState, aliceURL, bobURL string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, item := range []struct{ name, url string }{{"alice-edge", aliceURL}, {"bob-edge", bobURL}} {
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", item.url, "--name", item.name)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secretCheck...)
	}
	for _, item := range []struct{ account, provider string }{{"alice", "alice-edge"}, {"bob", "bob-edge"}} {
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "account", "add", item.account, "--provider", item.provider)
		if code != 0 {
			t.Fatalf("client account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secretCheck...)
	}
	st.clientProc = startUp(t, st.homeB, "--port", "0")
	assertStatusSuccess(t, st.clientProc.Status())
	t.Cleanup(func() {
		st.clientProc.Stop()
		assertNoSecrets(t, st.clientProc.Stdout(), st.clientProc.Stderr(), st.secretCheck...)
	})
}

// snellDiagnostic returns the redacted diagnostics of both homes.
func snellDiagnostic(t *testing.T, st *e2eSnellState) string {
	return e2eSnellDiagnostics(t, st.homeA, st.homeB, st.secretCheck...)
}

// driveSnellAccount sends one echo through B as account and checks that only
// that account's upstream recorded it. With retry it keeps trying for 30 s.
func driveSnellAccount(st *e2eSnellState, account string, retry bool) error {
	beforeA, beforeB := st.aliceUp.count.Load(), st.bobUp.count.Load()
	deadline := time.Now().Add(30 * time.Second)
	if !retry {
		deadline = time.Now()
	}
	for {
		err := e2eSnellAccountsEcho(st.ctx, st.clientProc.Status().Data.Listen, account, st.pass, st.backend)
		if err == nil {
			if account == "alice" && (st.aliceUp.count.Load() <= beforeA || st.bobUp.count.Load() != beforeB) {
				return errors.New("Alice recorder routing mismatch")
			}
			if account == "bob" && (st.bobUp.count.Load() <= beforeB || st.aliceUp.count.Load() != beforeA) {
				return errors.New("Bob recorder routing mismatch")
			}
			return nil
		}
		if !retry || time.Now().After(deadline) {
			return errors.New("account echo failed")
		}
		select {
		case <-st.ctx.Done():
			return errors.New("account echo deadline expired")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// driveSnellPair drives alice and then bob through B without retries.
func driveSnellPair(st *e2eSnellState) error {
	if err := driveSnellAccount(st, "alice", false); err != nil {
		return err
	}
	return driveSnellAccount(st, "bob", false)
}

// checkSnellInitialRoutes drives both accounts, then checks alice's link: a
// valid PSK connects and a wrong one is rejected. It returns alice's link.
func checkSnellInitialRoutes(t *testing.T, st *e2eSnellState) string {
	t.Helper()
	if err := driveSnellPair(st); err != nil {
		t.Fatalf("initial drive failed: %v\n%s", err, snellDiagnostic(t, st))
	}
	aliceLink := getSnellLink(t, st, "snell-alice", "alice")
	if err := e2eSnellProbe(st.ctx, aliceLink, st.backend, ""); err != nil {
		t.Fatalf("valid Snell probe failed\n%s", snellDiagnostic(t, st))
	}
	if err := e2eSnellProbe(st.ctx, aliceLink, st.backend, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !e2eSnellAuthRejected(err) {
		t.Fatalf("wrong PSK rejection failed\n%s", snellDiagnostic(t, st))
	}
	return aliceLink
}

// rotateSnellAlice rotates alice's PSK, checks that the old link is refused and
// publishes the rotated link to alice's feed. It returns the rotated link.
func rotateSnellAlice(t *testing.T, st *e2eSnellState, aliceLink string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "rotate", "snell-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotation failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	if err := e2eSnellProbe(st.ctx, aliceLink, st.backend, ""); !e2eSnellAuthRejected(err) {
		t.Fatalf("old PSK rejection failed\n%s", snellDiagnostic(t, st))
	}
	rotatedLink := getSnellLink(t, st, "snell-alice", "alice")
	rotatedPSK := e2eSnellPSK(t, rotatedLink)
	st.secretCheck = append(st.secretCheck, rotatedPSK)
	st.aliceFeed.set(rotatedLink)
	return rotatedLink
}

// updateSnellSubscription refreshes B's alice feed and drives both accounts
// through the refreshed subscription.
func updateSnellSubscription(t *testing.T, st *e2eSnellState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeB, 1)
	if err := driveSnellPair(st); err != nil {
		t.Fatalf("post-rotation drive failed: %v\n%s", err, snellDiagnostic(t, st))
	}
}

// restrictSnellAlice limits alice to 192.0.2.0/24: the rotated link is refused,
// and bob still routes after B restarts.
func restrictSnellAlice(t *testing.T, st *e2eSnellState, rotatedLink string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	if err := e2eSnellProbe(st.ctx, rotatedLink, st.backend, ""); !e2eSnellAuthRejected(err) {
		t.Fatalf("restricted Alice probe failed\n%s", snellDiagnostic(t, st))
	}
	e2eSnellRestartClient(t, st.homeB, &st.clientProc, st.secretCheck...)
	if err := driveSnellAccount(st, "bob", true); err != nil {
		t.Fatalf("restricted Bob echo failed: %v\n%s", err, snellDiagnostic(t, st))
	}
}

// clearSnellRestriction clears alice's client filter and checks that both
// accounts route again after B restarts.
func clearSnellRestriction(t *testing.T, st *e2eSnellState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	waitStatusReload(t, st.ctx, st.homeA, 5)
	e2eSnellRestartClient(t, st.homeB, &st.clientProc, st.secretCheck...)
	if err := driveSnellAccount(st, "alice", true); err != nil {
		t.Fatalf("post-clear Alice echo failed: %v\n%s", err, snellDiagnostic(t, st))
	}
	if err := driveSnellAccount(st, "bob", true); err != nil {
		t.Fatalf("post-clear Bob echo failed: %v\n%s", err, snellDiagnostic(t, st))
	}
}

// restartSnellServer restarts A's angel and checks that the rotated link
// persisted and both accounts route again.
func restartSnellServer(t *testing.T, st *e2eSnellState, rotatedLink string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("server down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secretCheck...)
	oldServer := st.serverProc
	oldServer.Stop()
	assertNoSecrets(t, oldServer.Stdout(), oldServer.Stderr(), st.secretCheck...)
	st.serverProc = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverProc.Status())
	e2eSnellRestartClient(t, st.homeB, &st.clientProc, st.secretCheck...)
	if got := getSnellLink(t, st, "snell-alice", "alice"); got != rotatedLink {
		t.Fatalf("link persistence failed\n%s", snellDiagnostic(t, st))
	}
	if err := driveSnellAccount(st, "alice", true); err != nil {
		t.Fatalf("post-A-restart Alice echo failed: %v\n%s", err, snellDiagnostic(t, st))
	}
	if err := driveSnellAccount(st, "bob", true); err != nil {
		t.Fatalf("post-A-restart Bob echo failed: %v\n%s", err, snellDiagnostic(t, st))
	}
}

// removeSnellServices removes both Snell edges and checks that their ports are
// released.
func removeSnellServices(t *testing.T, st *e2eSnellState) {
	t.Helper()
	for i, name := range []string{"snell-alice", "snell-bob"} {
		code, out, stderr := serverRun(t, st.homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secretCheck...)
		waitStatusReload(t, st.ctx, st.homeA, uint64(i+1))
	}
	for _, name := range []string{"snell-alice", "snell-bob"} {
		e2eSnellWaitStatus(t, st.ctx, st.homeA, name, false)
	}
	for _, port := range st.ports {
		if err := waitServerPort(st.ctx, port, false); err != nil {
			t.Fatalf("removed port check failed\n%s", snellDiagnostic(t, st))
		}
	}
}

func e2eSnellPSK(t *testing.T, link string) string {
	t.Helper()
	nodes, err := subscription.Parse([]byte(link))
	if err != nil || len(nodes) != 1 {
		t.Fatal("invalid generated Snell link")
	}
	psk, _ := nodes[0].Raw["psk"].(string)
	if psk == "" {
		t.Fatal("generated Snell link has no PSK")
	}
	return psk
}

type e2eSnellFeed struct {
	mu   sync.RWMutex
	body string
}

func (f *e2eSnellFeed) set(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = body
}

func (f *e2eSnellFeed) get() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.body
}
func (f *e2eSnellFeed) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/subscription", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, f.get()) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listen")
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/subscription"
}
func e2eSnellProbe(parent context.Context, link, target, wrongPSK string) error {
	nodes, err := subscription.Parse([]byte(link))
	if err != nil || len(nodes) != 1 {
		return errors.New("invalid Snell subscription")
	}
	n := nodes[0]
	psk, _ := n.Raw["psk"].(string)
	if wrongPSK != "" {
		psk = wrongPSK
	}
	version := 2
	if v, ok := n.Raw["version"].(int); ok {
		version = v
	} else if v, ok := n.Raw["version"].(float64); ok {
		version = int(v)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid echo target")
	}
	dp, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("invalid echo target port")
	}
	if dp < 0 {
		return errors.New("invalid echo target port")
	}
	if dp > math.MaxUint16 {
		return errors.New("invalid echo target port")
	}
	client, err := outbound.NewSnell(outbound.SnellOption{Name: "e2e-snell-probe", Server: n.Server, Port: n.Port, Psk: psk, Version: version, UDP: false})
	if err != nil {
		return errors.New("construct Snell probe")
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(n.Server, strconv.Itoa(n.Port)))
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	_ = raw.SetDeadline(time.Now().Add(4 * time.Second))
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("invalid echo target IP")
	}
	conn, err := client.StreamConnContext(ctx, raw, &C.Metadata{NetWork: C.TCP, DstIP: ip, DstPort: uint16(dp)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	const msg = "snell-e2e-probe"
	if _, err = io.WriteString(conn, msg); err != nil {
		return err
	}
	got := make([]byte, len(msg))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != msg {
		return errors.New("Snell echo response mismatch")
	}
	return nil
}
func e2eSnellAuthRejected(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	s := strings.ToLower(err.Error())
	// A connection the server closes before its reply (denied client) comes back as Snell error 101 "Remote EOF".
	return strings.Contains(s, "forcibly closed") || strings.Contains(s, "connection reset") || strings.Contains(s, "connection was aborted") || strings.Contains(s, "server reported code: 101")
}
func e2eSnellAccountsEcho(parent context.Context, proxy, user, pass, target string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return errors.New("SOCKS connect failed")
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS method negotiation failed")
	}
	userLen := len(user)
	if userLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	passLen := len(pass)
	if passLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	credentials := append([]byte{1, byte(userLen)}, []byte(user)...)
	credentials = append(credentials, byte(passLen))
	credentials = append(credentials, []byte(pass)...)
	if _, err = c.Write(credentials); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(c, authReply[:]); err != nil || authReply[1] != 0 {
		return errors.New("SOCKS authentication failed")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid echo target")
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil {
		return errors.New("invalid echo destination")
	}
	if port < 1 {
		return errors.New("invalid echo destination")
	}
	if port > math.MaxUint16 {
		return errors.New("invalid echo destination")
	}
	req := make([]byte, 0, 4+len(ip)+2)
	req = append(req, 5, 1, 0, 1)
	req = append(req, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err = c.Write(req); err != nil {
		return err
	}
	var hdr [4]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 || hdr[1] != 0 {
		return errors.New("SOCKS CONNECT failed")
	}
	var length int
	switch hdr[3] {
	case 1:
		length = net.IPv4len
	case 4:
		length = net.IPv6len
	case 3:
		var n [1]byte
		if _, err = io.ReadFull(c, n[:]); err != nil {
			return errors.New("SOCKS bound address failed")
		}
		length = int(n[0])
	default:
		return errors.New("SOCKS bound address type invalid")
	}
	if _, err = io.CopyN(io.Discard, c, int64(length+2)); err != nil {
		return errors.New("SOCKS bound address failed")
	}
	const payload = "snell-e2e-routed-echo"
	if _, err = io.WriteString(c, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil || string(got) != payload {
		return errors.New("routed echo mismatch")
	}
	return nil
}
func e2eSnellDiagnostics(t *testing.T, homeA, homeB string, secrets ...string) string {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, homeA, "status")
	status := serverErrorSummary(out, stderr)
	if code == 0 {
		var env struct {
			Data struct {
				Servers []struct {
					Name        string `json:"name"`
					Protocol    string `json:"protocol"`
					Listen      string `json:"listen"`
					Connections int    `json:"connections"`
					Dropped     uint64 `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &env) == nil {
			rows := make([]string, 0, len(env.Data.Servers))
			for _, s := range env.Data.Servers {
				rows = append(rows, fmt.Sprintf("%s %s %s connections=%d dropped=%d", s.Name, s.Protocol, s.Listen, s.Connections, s.Dropped))
			}
			status = strings.Join(rows, "; ")
		}
	}
	logs := serverE2ELogTail(t.Context(), t, homeA)
	codeB, outB, stderrB := accountsRun(t.Context(), t, homeB, "status", "--full")
	health := serverErrorSummary(outB, stderrB)
	if codeB == 0 {
		var env struct {
			Data struct {
				Warm struct {
					Nodes []struct {
						Name    string `json:"name"`
						Label   string `json:"label"`
						Alive   bool   `json:"alive"`
						DelayMS int    `json:"delay_ms"`
					} `json:"nodes"`
				} `json:"warm"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(outB), &env) == nil {
			rows := make([]string, 0, len(env.Data.Warm.Nodes))
			for _, node := range env.Data.Warm.Nodes {
				rows = append(rows, fmt.Sprintf("%s %s alive=%t delay_ms=%d", node.Name, node.Label, node.Alive, node.DelayMS))
			}
			health = strings.Join(rows, "; ")
		}
	}
	text := status + "\n" + logs + "\nB node health: " + health
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	t.Logf("%s", text)
	return text
}
func e2eSnellWaitStatus(t *testing.T, ctx context.Context, home, name string, want bool) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Servers []struct {
						Name     string `json:"name"`
						Protocol string `json:"protocol"`
					} `json:"servers"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(out), &env); err != nil {
				t.Fatal("status response malformed")
			}
			found := false
			for _, s := range env.Data.Servers {
				if s.Name == name {
					found = true
					if s.Protocol != "snell" {
						t.Fatal("Snell status protocol mismatch")
					}
				}
			}
			if found == want {
				return
			}
		} else {
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(stderr), &env); err != nil || env.Error.Code == "" {
				t.Fatal("status returned malformed error envelope")
			}
			if env.Error.Code != "ANGEL_NOT_RUNNING" {
				t.Fatalf("status failed with code %q", env.Error.Code)
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Snell status wait expired")
			t.Fatalf("Snell status did not converge for %s", name)
		case <-tick.C:
		}
	}
}
func e2eSnellRestartClient(t *testing.T, home string, p **upProc, secrets ...string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, home, "down")
	if code != 0 {
		t.Fatalf("client down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	old := *p
	old.Stop()
	assertNoSecrets(t, old.Stdout(), old.Stderr(), secrets...)
	*p = startUp(t, home, "--port", "0")
	assertStatusSuccess(t, (*p).Status())
}
