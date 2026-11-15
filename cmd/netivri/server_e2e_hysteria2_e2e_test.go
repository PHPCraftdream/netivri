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
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestE2EHysteria2Lifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	e2eTraceOn(t)
	st := &e2eHysteria2State{ctx: ctx, bodyByAccount: map[string]string{}}
	setupHysteria2ServerA(t, st)
	subAddr := startHysteria2Subscriptions(t, st)
	setHysteria2Links(t, st)
	setupHysteria2ClientB(t, st, "http://"+subAddr)
	checkHysteria2Drives(t, st)
	oldAlice := probeHysteria2Credentials(t, st)
	rotateHysteria2Alice(t, st, oldAlice)
	updateHysteria2Subscription(t, st)
	restrictHysteria2Alice(t, st)
	checkHysteria2RestrictedBob(t, st)
	clearHysteria2Restriction(t, st)
	aliceLink, bobLink := restartHysteria2ServerA(t, st)
	checkHysteria2AfterServerRestart(t, st, aliceLink, bobLink)
	removeHysteria2Edge(t, st)
}

// e2eHysteria2State is the shared state of TestE2EHysteria2Lifecycle. secrets
// grow as links and passwords are created, so every helper redacts with the
// current list.
type e2eHysteria2State struct {
	ctx           context.Context
	password      string
	hash          string
	aliceUp       *accountsUpstream
	bobUp         *accountsUpstream
	aliceEcho     string
	bobEcho       string
	secrets       []string
	homeA         string
	homeB         string
	angelA        *upProc
	angelB        *upProc
	port          int
	bodyMu        sync.RWMutex
	bodyByAccount map[string]string
}

// trackHysteria2Angel stops process and redacts its output when the test ends.
func trackHysteria2Angel(t *testing.T, st *e2eHysteria2State, process *upProc) {
	t.Helper()
	t.Cleanup(func() {
		process.Stop()
		assertNoSecrets(t, process.Stdout(), process.Stderr(), st.secrets...)
	})
}

// e2eHysteria2Link returns the URI that `server link` prints for account.
func e2eHysteria2Link(t *testing.T, home, account string) string {
	t.Helper()
	c, stdout, errout := serverRun(t, home, "link", "edge", "--account", account, "--format", "uri")
	if c != 0 {
		t.Fatalf("server link: %s", serverErrorSummary(stdout, errout))
	}
	var envelope struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(stdout), &envelope) != nil || envelope.Data.Link == "" {
		t.Fatal("server link missing")
	}
	return envelope.Data.Link
}

// setupHysteria2ServerA writes A's fixture, starts A's angel and adds the
// hysteria2 edge that alice and bob share.
func setupHysteria2ServerA(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	const accountPassword = "hysteria2-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	st.password = accountPassword
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	st.aliceEcho, st.bobEcho = accountsEcho(t), accountsEcho(t)
	hash, err := auth.Hash(accountPassword)
	if err != nil {
		t.Fatal("hash account password")
	}
	st.hash = hash
	st.secrets = []string{accountPassword, hash}
	st.homeA = t.TempDir()
	aliceURL := accountsSubServer(t, "placeholder")
	bobURL := accountsSubServer(t, "placeholder")
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "route-alice"}, {Login: "bob", Password: hash, Provider: "route-bob"}}
		cfg.Subscriptions = []config.Subscription{{Name: "route-alice", URL: aliceURL}, {Name: "route-bob", URL: bobURL}}
	})
	accountsSaveSnapshot(t, st.homeA, "route-alice", []subscription.Node{accountsSocksNode("alice-route", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "route-bob", []subscription.Node{accountsSocksNode("bob-route", "", st.bobUp)})
	st.angelA = startUp(t, st.homeA, "--port", "0")
	trackHysteria2Angel(t, st, st.angelA)
	portConn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve UDP port")
	}
	udpAddr, ok := portConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("UDP address is %T, want *net.UDPAddr", portConn.LocalAddr())
	}
	st.port = udpAddr.Port
	_ = portConn.Close()
	e2eMark(t, "add the hysteria2 server edge on udp port %d", st.port)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "hysteria2", "--name", "edge", "--account", "*", "--port", strconv.Itoa(st.port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eHysteria2WaitStatus(t, st.ctx, st.homeA, "edge", "hysteria2", true)
	e2eMark(t, "server edge is up on A, reload 1")
}

// startHysteria2Subscriptions serves each account's current link as a
// subscription and returns the listener address.
func startHysteria2Subscriptions(t *testing.T, st *e2eHysteria2State) string {
	t.Helper()
	subServer := &http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			st.bodyMu.RLock()
			body := st.bodyByAccount[strings.TrimPrefix(r.URL.Path, "/")]
			st.bodyMu.RUnlock()
			_, _ = io.WriteString(w, body+"\n")
		}),
	}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listen")
	}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = subServer.Serve(subListener) }()
	t.Cleanup(func() {
		_ = subServer.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Error("subscription server did not stop")
		}
	})
	return subListener.Addr().String()
}

// setHysteria2Links publishes both current links to the subscription server
// and adds their passwords to the redaction list.
func setHysteria2Links(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	aliceLink, bobLink := e2eHysteria2Link(t, st.homeA, "alice"), e2eHysteria2Link(t, st.homeA, "bob")
	st.bodyMu.Lock()
	st.bodyByAccount["alice"], st.bodyByAccount["bob"] = aliceLink, bobLink
	st.bodyMu.Unlock()
	for _, uri := range []string{aliceLink, bobLink} {
		nodes, parseErr := subscription.Parse([]byte(uri))
		if parseErr != nil || len(nodes) != 1 {
			t.Fatal("parse server URI")
		}
		password, _ := nodes[0].Raw["password"].(string)
		if password != "" {
			st.secrets = append(st.secrets, password)
		}
	}
}

// setupHysteria2ClientB imports both subscriptions into B, claims the accounts
// with the shared password and starts B's angel.
func setupHysteria2ClientB(t *testing.T, st *e2eHysteria2State, baseURL string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		subURL := baseURL + "/" + account
		c, stdout, errout := accountsRun(t.Context(), t, st.homeB, "sub", "add", subURL, "--name", "provider-"+account)
		if c != 0 {
			t.Fatalf("subscription add: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, st.secrets...)
		c, stdout, errout = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", "provider-"+account)
		if c != 0 {
			t.Fatalf("account add: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, st.secrets...)
	}
	cfgB := loadConfigFile(t, st.homeB)
	for i := range cfgB.Accounts {
		cfgB.Accounts[i].Password = st.hash
	}
	if err := config.Save(config.ConfigPath(st.homeB), cfgB); err != nil {
		t.Fatal("save claimed client accounts")
	}
	st.angelB = startUp(t, st.homeB, "--port", "0")
	trackHysteria2Angel(t, st, st.angelB)
}

// driveHysteria2 sends one echo through B as account and checks that it reached
// own and not other.
func driveHysteria2(t *testing.T, st *e2eHysteria2State, account, echo string, own, other *accountsUpstream) {
	t.Helper()
	beforeOwn, beforeOther := own.count.Load(), other.count.Load()
	defer func() {
		if t.Failed() {
			e2eHysteria2Diagnostics(t.Context(), t, st.homeA, st.homeB, st.secrets)
		}
	}()
	accountsClient(t, st.angelB.Status().Data.Listen, account, st.password, echo)
	if own.count.Load() <= beforeOwn || other.count.Load() != beforeOther {
		t.Fatalf("%s routed to wrong recorder", account)
	}
}

// checkHysteria2Drives drives alice and bob through B and records the snapshot.
func checkHysteria2Drives(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	e2eMark(t, "drive alice and bob through B")
	driveHysteria2(t, st, "alice", st.aliceEcho, st.aliceUp, st.bobUp)
	driveHysteria2(t, st, "bob", st.bobEcho, st.bobUp, st.aliceUp)
	e2eHysteria2Snapshot(t, "after the first drives", st.homeA, st.homeB, st.secrets)
}

// probeHysteria2Credentials checks that a wrong credential is refused and the
// imported URI works. It returns alice's imported URI.
func probeHysteria2Credentials(t *testing.T, st *e2eHysteria2State) string {
	t.Helper()
	st.bodyMu.RLock()
	aliceURI := st.bodyByAccount["alice"]
	st.bodyMu.RUnlock()
	if err := e2eHysteria2ProbeFromURI(st.ctx, aliceURI, st.aliceEcho, "incorrect-hysteria2-credential"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed, status code: 404") {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "wrong credential", err)
	}
	e2eMark(t, "probe with a wrong credential was refused as expected")
	if err := e2eHysteria2ProbeFromURI(st.ctx, aliceURI, st.aliceEcho, ""); err != nil {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "valid imported URI", err)
	}
	e2eMark(t, "probe with the valid imported URI passed")
	return aliceURI
}

// rotateHysteria2Alice rotates alice's credential, checks that the old URI is
// refused and reissues the links.
func rotateHysteria2Alice(t *testing.T, st *e2eHysteria2State, oldAlice string) {
	t.Helper()
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	e2eMark(t, "rotate the credential of alice")
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	e2eMark(t, "alice rotated, reload 2")
	e2eHysteria2Snapshot(t, "after rotate", st.homeA, st.homeB, st.secrets)
	if err := e2eHysteria2ProbeFromURI(st.ctx, oldAlice, st.aliceEcho, ""); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication failed, status code: 404") {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "rotated credential", err)
	}
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() != beforeBob {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "failed authentication route recorder", io.ErrUnexpectedEOF)
	}
	setHysteria2Links(t, st)
}

// updateHysteria2Subscription refreshes alice's subscription on B and checks
// that both accounts still route to their own upstreams.
func updateHysteria2Subscription(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	e2eMark(t, "links reissued; updating the subscription of B")
	for _, account := range []string{"alice"} {
		c, stdout, errout := accountsRun(t.Context(), t, st.homeB, "sub", "update", "provider-"+account)
		if c != 0 {
			t.Fatalf("subscription update: %s", serverErrorSummary(stdout, errout))
		}
		assertNoSecrets(t, stdout, errout, st.secrets...)
	}
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveHysteria2(t, st, "alice", st.aliceEcho, st.aliceUp, st.bobUp)
	driveHysteria2(t, st, "bob", st.bobEcho, st.bobUp, st.aliceUp)
	e2eMark(t, "drives after the subscription update passed; restricting alice")
}

// restrictHysteria2Alice limits alice to 192.0.2.0/24 and checks that a probe
// with alice's URI is dropped without reaching a recorder.
func restrictHysteria2Alice(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("set allowed clients: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 3)
	e2eMark(t, "alice restricted to 192.0.2.0/24, reload 3")
	e2eHysteria2Snapshot(t, "after restriction", st.homeA, st.homeB, st.secrets)
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	st.bodyMu.RLock()
	aliceURI := st.bodyByAccount["alice"]
	st.bodyMu.RUnlock()
	if err := e2eHysteria2ProbeFromURI(st.ctx, aliceURI, st.aliceEcho, ""); err == nil {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "restricted Alice probe", nil)
	}
	e2eHysteria2WaitDropped(t, st.ctx, st.homeA, "edge")
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() != beforeBob {
		e2eHysteria2Failure(t, st.homeA, st.homeB, st.secrets, "restricted route recorder", io.ErrUnexpectedEOF)
	}
}

// restartHysteria2Client restarts B. A dead route backs off probes (45s base,
// lengthened per failure); restarting B clears its cached dead-node state.
func restartHysteria2Client(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	e2eMark(t, "restart client angel B")
	if code, stdout, errout := accountsRun(t.Context(), t, st.homeB, "down"); code != 0 && !e2eHysteria2AngelNotRunning(stdout, errout) {
		t.Fatalf("client angel down: %s", serverErrorSummary(stdout, errout))
	}
	st.angelB.Stop()
	st.angelB = startUp(t, st.homeB, "--port", "0")
	trackHysteria2Angel(t, st, st.angelB)
	e2eHysteria2Snapshot(t, "after restart of B", st.homeA, st.homeB, st.secrets)
}

// checkHysteria2RestrictedBob restarts B while alice is restricted and checks
// that bob still routes.
func checkHysteria2RestrictedBob(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	restartHysteria2Client(t, st)
	e2eHysteria2RetryEchoes(t, st.ctx, st.angelB.Status().Data.Listen, st.password, st.secrets, st.homeA, st.homeB, []e2eHysteria2Echo{{"bob", st.bobEcho, st.bobUp, st.aliceUp}})
}

// clearHysteria2Restriction clears alice's client restriction and checks that
// both accounts route after B restarts.
func clearHysteria2Restriction(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear allowed clients: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 4)
	e2eMark(t, "allowed clients of alice cleared, reload 4")
	restartHysteria2Client(t, st)
	e2eHysteria2RetryEchoes(t, st.ctx, st.angelB.Status().Data.Listen, st.password, st.secrets, st.homeA, st.homeB, []e2eHysteria2Echo{{"alice", st.aliceEcho, st.aliceUp, st.bobUp}, {"bob", st.bobEcho, st.bobUp, st.aliceUp}})
}

// restartHysteria2ServerA restarts A's angel and returns the links read before
// the restart, to compare them after it.
func restartHysteria2ServerA(t *testing.T, st *e2eHysteria2State) (string, string) {
	t.Helper()
	aliceLink, bobLink := e2eHysteria2Link(t, st.homeA, "alice"), e2eHysteria2Link(t, st.homeA, "bob")
	e2eMark(t, "restart server angel A")
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("angel down: %s", serverErrorSummary(out, stderr))
	}
	st.angelA.Stop()
	assertNoSecrets(t, st.angelA.Stdout(), st.angelA.Stderr(), st.secrets...)
	st.angelA = startUp(t, st.homeA, "--port", "0")
	trackHysteria2Angel(t, st, st.angelA)
	e2eHysteria2WaitStatus(t, st.ctx, st.homeA, "edge", "hysteria2", true)
	e2eHysteria2Snapshot(t, "after restart of A", st.homeA, st.homeB, st.secrets)
	return aliceLink, bobLink
}

// checkHysteria2AfterServerRestart restarts B and checks that the server links
// are unchanged and both accounts route again.
func checkHysteria2AfterServerRestart(t *testing.T, st *e2eHysteria2State, aliceLink, bobLink string) {
	t.Helper()
	restartHysteria2Client(t, st)
	if e2eHysteria2Link(t, st.homeA, "alice") != aliceLink || e2eHysteria2Link(t, st.homeA, "bob") != bobLink {
		t.Fatal("server links changed across restart")
	}
	e2eHysteria2RetryEchoes(t, st.ctx, st.angelB.Status().Data.Listen, st.password, st.secrets, st.homeA, st.homeB, []e2eHysteria2Echo{{"alice", st.aliceEcho, st.aliceUp, st.bobUp}, {"bob", st.bobEcho, st.bobUp, st.aliceUp}})
}

// removeHysteria2Edge removes the hysteria2 edge and checks that its UDP port
// is released.
func removeHysteria2Edge(t *testing.T, st *e2eHysteria2State) {
	t.Helper()
	e2eMark(t, "remove server edge")
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eHysteria2WaitStatus(t, st.ctx, st.homeA, "edge", "hysteria2", false)
	e2eHysteria2WaitUDPFree(t, st.ctx, st.port)
}

type e2eHysteria2Echo struct {
	account string
	echo    string
	own     *accountsUpstream
	other   *accountsUpstream
}

func e2eHysteria2RetryEchoes(t *testing.T, parent context.Context, proxy, password string, secrets []string, homeA, homeB string, checks []e2eHysteria2Echo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for _, check := range checks {
		beforeOwn, beforeOther := check.own.count.Load(), check.other.count.Load()
		var lastErr error
		attempts := 0
		for {
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 3*time.Second)
			lastErr = e2eHysteria2AccountsClient(attemptCtx, proxy, check.account, password, check.echo)
			attemptCancel()
			attempts++
			if lastErr == nil {
				e2eMark(t, "echo %s passed at attempt %d", check.account, attempts)
			} else if attempts <= 3 || attempts%10 == 0 {
				e2eMark(t, "echo %s attempt %d failed: %s", check.account, attempts, e2eHysteria2Sanitize(lastErr.Error(), secrets))
			}
			if lastErr == nil {
				if check.own.count.Load() <= beforeOwn || check.other.count.Load() != beforeOther {
					e2eHysteria2Diagnostics(context.WithoutCancel(ctx), t, homeA, homeB, secrets)
					t.Fatalf("%s routed to wrong recorder", check.account)
				}
				break
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				e2eHysteria2Diagnostics(context.WithoutCancel(ctx), t, homeA, homeB, secrets)
				t.Fatalf("%s echo retries exhausted: %s", check.account, e2eHysteria2Sanitize(lastErr.Error(), secrets))
			case <-timer.C:
			}
		}
	}
}

func e2eHysteria2AccountsClient(ctx context.Context, proxy, user, pass, target string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil {
		return err
	}
	if method != [2]byte{5, 2} {
		return fmt.Errorf("SOCKS method %v", method)
	}
	userLen := len(user)
	if userLen > 255 {
		return errors.New("credentials too long")
	}
	passLen := len(pass)
	if passLen > 255 {
		return errors.New("credentials too long")
	}
	requestAuth := append([]byte{1, byte(userLen)}, []byte(user)...)
	requestAuth = append(requestAuth, byte(passLen))
	requestAuth = append(requestAuth, []byte(pass)...)
	if _, err = conn.Write(requestAuth); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil {
		return err
	}
	if authReply[1] != 0 {
		return fmt.Errorf("SOCKS auth reply %v", authReply)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("target is not IPv4: %s", target)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return err
	}
	if port > math.MaxUint16 {
		return errors.New("target port out of range")
	}
	request := append([]byte{5, 1, 0, 1}, ip...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err = conn.Write(request); err != nil {
		return err
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("SOCKS CONNECT reply %v", reply)
	}
	payload := []byte("hysteria2-route-check")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func e2eHysteria2Failure(t *testing.T, homeA, homeB string, secrets []string, what string, err error) {
	t.Helper()
	e2eHysteria2Diagnostics(t.Context(), t, homeA, homeB, secrets)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded", what)
	}
	t.Fatalf("%s failed: %s", what, e2eHysteria2Sanitize(err.Error(), secrets))
}

func e2eHysteria2Diagnostics(ctx context.Context, t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	e2eHysteria2Diagnostic(ctx, t, homeA, false, secrets)
	e2eHysteria2Diagnostic(ctx, t, homeB, true, secrets)
}

func e2eHysteria2Diagnostic(ctx context.Context, t *testing.T, home string, full bool, secrets []string) {
	t.Helper()
	args := []string{"status", "--json"}
	if full {
		args = []string{"status", "--full", "--json"}
	}
	deadline := time.NewTimer(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
statusLoop:
	for {
		code, stdout, stderr, err := angelExec(context.WithoutCancel(ctx), home, args...)
		if err == nil && code == 0 {
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Status(stdout, full, secrets))
			break
		}
		if !e2eHysteria2AngelNotRunning(stdout, stderr) {
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Sanitize(stderr, secrets))
			break
		}
		select {
		case <-deadline.C:
			t.Logf("diagnostic status (%s): %s", home, e2eHysteria2Sanitize(stderr, secrets))
			break statusLoop
		case <-ticker.C:
		}
	}
	t.Logf("angel log (%s): %s", home, e2eHysteria2Sanitize(serverE2ELogTail(ctx, t, home), secrets))
	t.Logf("angel crash dumps (%s): %s", home, e2eHysteria2Sanitize(e2eCrashDumps(home), secrets))
}

func e2eHysteria2Status(raw string, full bool, secrets []string) string {
	var envelope struct {
		Data struct {
			Servers []struct {
				Name        string `json:"name"`
				Protocol    string `json:"protocol"`
				Connections int    `json:"connections"`
				Dropped     uint64 `json:"dropped_clients"`
			} `json:"servers"`
			Warm struct {
				Alive         int    `json:"alive"`
				Total         int    `json:"total"`
				Probes        uint64 `json:"probes"`
				ProbeFailures uint64 `json:"probe_failures"`
				PoolDiscards  uint64 `json:"pool_discards"`
				Nodes         []struct {
					Label   string `json:"label"`
					Name    string `json:"name"`
					Alive   bool   `json:"alive"`
					DelayMS int64  `json:"delay_ms"`
				} `json:"nodes"`
			} `json:"warm"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return "status JSON unavailable"
	}
	data, err := json.Marshal(envelope.Data.Servers)
	if err != nil {
		return "servers unavailable: " + err.Error()
	}
	out := "servers=" + string(data)
	if full {
		nodes, err := json.Marshal(envelope.Data.Warm.Nodes)
		if err != nil {
			return "warm nodes unavailable: " + err.Error()
		}
		out += fmt.Sprintf(" warm.alive=%d warm.total=%d warm.probes=%d warm.probe_failures=%d warm.pool_discards=%d nodes.alive=%s", envelope.Data.Warm.Alive, envelope.Data.Warm.Total, envelope.Data.Warm.Probes, envelope.Data.Warm.ProbeFailures, envelope.Data.Warm.PoolDiscards, nodes)
	}
	return e2eHysteria2Sanitize(out, secrets)
}

func e2eHysteria2Sanitize(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, "://") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func e2eHysteria2AngelNotRunning(out, stderr string) bool {
	for _, raw := range []string{out, stderr} {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Error.Code == "ANGEL_NOT_RUNNING" {
			return true
		}
	}
	return false
}

func e2eHysteria2WaitStatus(t *testing.T, ctx context.Context, home, name, protocol string, present bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code != 0 {
			if e2eHysteria2AngelNotRunning(out, stderr) {
				select {
				case <-ctx.Done():
					t.Fatalf("server status presence did not become %t", present)
				case <-ticker.C:
				}
				continue
			}
			t.Fatalf("status: %s", serverErrorSummary(out, stderr))
		}
		var envelope struct {
			Data struct {
				Servers []struct {
					Name        string `json:"name"`
					Protocol    string `json:"protocol"`
					Connections int    `json:"connections"`
					Dropped     int    `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &envelope) != nil {
			t.Fatal("decode server status")
		}
		found := false
		for _, server := range envelope.Data.Servers {
			if server.Name == name {
				found = true
				if server.Protocol != protocol || server.Connections < 0 || server.Dropped < 0 {
					t.Fatal("invalid server status")
				}
			}
		}
		if found == present {
			return
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Hysteria2 status wait expired")
			t.Fatalf("server status presence did not become %t", present)
		case <-ticker.C:
		}
	}
}

func e2eHysteria2WaitDropped(t *testing.T, ctx context.Context, home, name string) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code != 0 {
			if e2eHysteria2AngelNotRunning(out, stderr) {
				select {
				case <-ctx.Done():
					t.Fatal("allowed-client drop was not counted")
				case <-ticker.C:
				}
				continue
			}
			t.Fatalf("status: %s", serverErrorSummary(out, stderr))
		}
		var envelope struct {
			Data struct {
				Servers []struct {
					Name    string `json:"name"`
					Dropped uint64 `json:"dropped_clients"`
				} `json:"servers"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal("decode server status")
		}
		for _, server := range envelope.Data.Servers {
			if server.Name == name && server.Dropped > 0 {
				return
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Hysteria2 dropped status wait expired")
			t.Fatal("allowed-client drop was not counted")
		case <-ticker.C:
		}
	}
}

func e2eHysteria2WaitUDPFree(t *testing.T, ctx context.Context, port int) {
	t.Helper()
	for {
		conn, err := (&net.ListenConfig{}).ListenPacket(context.WithoutCancel(ctx), "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("removed server did not release UDP port")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func e2eHysteria2ProbeFromURI(ctx context.Context, uri, target, overridePassword string) error {
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 || nodes[0].Type != "hysteria2" {
		return io.ErrUnexpectedEOF
	}
	raw := nodes[0].Raw
	password, _ := raw["password"].(string)
	fingerprint, _ := raw["fingerprint"].(string)
	sni, _ := raw["sni"].(string)
	skipVerify, _ := raw["skip-cert-verify"].(bool)
	if overridePassword != "" {
		password = overridePassword
	}
	if password == "" || fingerprint == "" || sni == "" || skipVerify {
		return io.ErrUnexpectedEOF
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	destinationPort, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	if destinationPort < 0 {
		return errors.New("target port out of range")
	}
	if destinationPort > math.MaxUint16 {
		return errors.New("target port out of range")
	}
	client, err := outbound.NewHysteria2(outbound.Hysteria2Option{Name: "e2e-hysteria2-probe", Server: nodes[0].Server, Port: nodes[0].Port, Password: password, SNI: sni, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(destinationPort)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	deadline, ok := probeCtx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	const payload = "hysteria2-route-check"
	if _, err = io.WriteString(conn, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// e2eHysteria2Snapshot records the status of both angels in the failure trace,
// so a failed run shows how the pair stood at each step, also when an angel is
// gone by the time the report is made. It never fails the test.
func e2eHysteria2Snapshot(t *testing.T, label, homeA, homeB string, secrets []string) {
	for _, angel := range []struct {
		name, home string
		full       bool
	}{{"A", homeA, false}, {"B", homeB, true}} {
		args := []string{"status", "--json"}
		if angel.full {
			args = []string{"status", "--full", "--json"}
		}
		code, stdout, stderr, err := angelExec(t.Context(), angel.home, args...)
		switch {
		case err != nil:
			e2eMark(t, "snapshot %s, angel %s: status did not run: %v", label, angel.name, err)
		case code != 0:
			e2eMark(t, "snapshot %s, angel %s: status exit %d: %s", label, angel.name, code, e2eHysteria2Sanitize(strings.TrimSpace(stdout+stderr), secrets))
		default:
			e2eMark(t, "snapshot %s, angel %s: %s", label, angel.name, e2eHysteria2Status(stdout, angel.full, secrets))
		}
	}
}
