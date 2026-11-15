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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
	"go.yaml.in/yaml/v3"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func TestE2EMieruLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st := &e2eMieruState{ctx: ctx}
	setupMieruServerA(t, st)
	readMieruLinks(t, st)
	subAddr := startMieruSubscriptions(t, st)
	setupMieruClientB(t, st, "http://"+subAddr)
	checkMieruInitialRoutes(t, st)
	oldAlice, newAlice := rotateMieruAlice(t, st)
	updateMieruAliceSubscription(t, st)
	restrictMieruAlice(t, st, newAlice)
	clearMieruRestriction(t, st)
	restartMieruServer(t, st, newAlice)
	checkMieruAfterRestart(t, st, oldAlice, newAlice)
	removeMieruEdge(t, st)
}

// e2eMieruState is the shared state of TestE2EMieruLifecycle. allSecrets grows
// as links and passwords are created, so every helper redacts with the
// current list.
type e2eMieruState struct {
	ctx                 context.Context
	backend             string
	alicePass           string
	bobPass             string
	aliceUp             *accountsUpstream
	bobUp               *accountsUpstream
	homeA               string
	homeB               string
	serverPort          int
	allSecrets          []string
	links               map[string]string
	linkMu              sync.RWMutex
	serverAngel         *upProc
	originalServerAngel *upProc
	clientAngel         *upProc
}

// setupMieruServerA writes A's fixture, each account on its own upstream, and
// adds the Mieru edge shared by alice and bob.
func setupMieruServerA(t *testing.T, st *e2eMieruState) {
	t.Helper()
	const alicePass = "mieru-e2e-alice-account-pass" //nolint:gosec // G101: fixture password of a test account, not a credential
	const bobPass = "mieru-e2e-bob-account-pass"     //nolint:gosec // G101: fixture password of a test account, not a credential
	st.alicePass, st.bobPass = alicePass, bobPass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice account password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob account password")
	}

	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: aliceHash, Provider: "alice-route"},
			{Login: "bob", Password: bobHash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", st.bobUp)})
	st.allSecrets = []string{alicePass, bobPass, aliceHash, bobHash}
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverAngel.Status())
	st.originalServerAngel = st.serverAngel
	st.serverPort = freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "mieru", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(st.serverPort), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "transport=TCP", "--param", "multiplexing=MULTIPLEXING_OFF")
	if code != 0 {
		t.Fatalf("Mieru server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 1)
	e2eMieruWaitStatus(t, st.ctx, st.homeA, true, st.allSecrets...)
}

// getMieruLink returns the mihomo Mieru link of account.
func getMieruLink(t *testing.T, home, account string) string {
	t.Helper()
	c, stdout, errout := serverRun(t, home, "link", "edge", "--account", account, "--format", "mihomo")
	if c != 0 {
		t.Fatalf("server link failed: %s", serverErrorSummary(stdout, errout))
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(stdout), &response) != nil || !response.OK || response.Data.Link == "" {
		t.Fatal("Mieru link missing")
	}
	return response.Data.Link
}

// readMieruLinks reads both Mieru links and adds their passwords to the
// redaction list.
func readMieruLinks(t *testing.T, st *e2eMieruState) {
	t.Helper()
	st.links = map[string]string{"alice": getMieruLink(t, st.homeA, "alice"), "bob": getMieruLink(t, st.homeA, "bob")}
	st.allSecrets = append(st.allSecrets, e2eMieruExtractPassword(t, st.links["alice"]), e2eMieruExtractPassword(t, st.links["bob"]))
}

// startMieruSubscriptions serves each account's current link on the loopback
// and returns the listener address.
func startMieruSubscriptions(t *testing.T, st *e2eMieruState) string {
	t.Helper()
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			st.linkMu.RLock()
			defer st.linkMu.RUnlock()
			_, _ = io.WriteString(w, st.links[account])
		})
	}
	sub := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen local subscription")
	}
	subDone := make(chan struct{})
	t.Cleanup(func() {
		_ = sub.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(subDone); _ = sub.Serve(subListener) }()
	return subListener.Addr().String()
}

// setupMieruClientB imports both subscriptions into B, adds one account per
// provider and starts B's angel.
func setupMieruClientB(t *testing.T, st *e2eMieruState, base string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		c, stdout, errout := accountsRun(t.Context(), t, st.homeB, "sub", "add", base+"/"+account, "--name", provider)
		if c != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(stdout, errout))
		}
		c, stdout, errout = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if c != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(stdout, errout))
		}
	}
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	assertStatusSuccess(t, st.clientAngel.Status())
}

// restartMieruClient restarts B's angel.
func restartMieruClient(t *testing.T, st *e2eMieruState) {
	t.Helper()
	code, stdout, stderr := accountsRun(t.Context(), t, st.homeB, "down")
	if code != 0 {
		t.Fatalf("client angel down failed: %s", serverErrorSummary(stdout, stderr))
	}
	st.clientAngel.Stop()
	st.clientAngel = startUp(t, st.homeB, "--port", "0")
	assertStatusSuccess(t, st.clientAngel.Status())
}

// driveMieru sends one echo through B as account. With retry it keeps trying for
// 30 s and needs own to count one more connection and other none.
func driveMieru(t *testing.T, st *e2eMieruState, account, pass string, own, other *accountsUpstream, retry bool) {
	t.Helper()
	probeCtx := st.ctx
	cancelRetry := func() {}
	if retry {
		probeCtx, cancelRetry = context.WithTimeout(st.ctx, 30*time.Second)
	}
	defer cancelRetry()
	startOwn, startOther := own.count.Load(), other.count.Load()
	attempts := 1
	if retry {
		attempts = 30
	}
	var last error
	for i := 0; i < attempts; i++ {
		last = e2eMieruSOCKSEcho(probeCtx, st.clientAngel.Status().Data.Listen, account, pass, st.backend, "mieru-e2e-echo")
		if last == nil && own.count.Load() > startOwn && other.count.Load() == startOther {
			return
		}
		if !retry {
			break
		}
		if last != nil {
			t.Logf("Mieru echo attempt failed: %s", e2eMieruRedact(last.Error(), st.allSecrets))
		} else {
			t.Log("Mieru echo attempt failed: recorder counts not updated")
		}
		select {
		case <-probeCtx.Done():
			i = attempts
		case <-time.After(time.Second):
		}
	}
	e2eMieruDiagnostics(context.WithoutCancel(st.ctx), t, st.homeA, st.homeB, st.allSecrets)
	if last != nil {
		t.Fatalf("%s client-core echo failed: %s", account, e2eMieruRedact(fmt.Sprint(last), st.allSecrets))
	}
	t.Fatalf("%s recorder mismatch (own %d->%d, other %d->%d)", account, startOwn, own.count.Load(), startOther, other.count.Load())
}

// checkMieruInitialRoutes checks the server credential directly, then drives
// both accounts through B.
func checkMieruInitialRoutes(t *testing.T, st *e2eMieruState) {
	t.Helper()
	if err := e2eMieruProbe(st.ctx, st.links["alice"], st.backend, "valid-mieru-credential", false); err != nil {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("correct server credential probe failed: %s", e2eMieruRedact(err.Error(), st.allSecrets))
	}
	if err := e2eMieruProbe(st.ctx, st.links["alice"], st.backend, "wrong-password", true); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("wrong server credential not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), st.allSecrets))
	}
	driveMieru(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, false)
	driveMieru(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, false)
}

// rotateMieruAlice rotates alice's credential, checks that the old one is
// refused and publishes the new link. It returns the old and the new link.
func rotateMieruAlice(t *testing.T, st *e2eMieruState) (string, string) {
	t.Helper()
	oldAlice := st.links["alice"]
	if err := e2eMieruProbe(st.ctx, oldAlice, st.backend, "pre-rotation", false); err != nil {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("correct Alice credential probe before rotation failed: %s", e2eMieruRedact(err.Error(), st.allSecrets))
	}
	code, out, stderr := serverRun(t, st.homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotation failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, 2)
	if err := e2eMieruProbe(st.ctx, oldAlice, st.backend, "old-password", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("old Alice credential not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), st.allSecrets))
	}
	newAlice := getMieruLink(t, st.homeA, "alice")
	if newAlice == oldAlice {
		t.Fatal("Alice refreshed link did not change after rotation")
	}
	st.allSecrets = append(st.allSecrets, e2eMieruExtractPassword(t, newAlice))
	st.linkMu.Lock()
	st.links["alice"] = newAlice
	st.linkMu.Unlock()
	return oldAlice, newAlice
}

// updateMieruAliceSubscription refreshes alice's subscription on B and drives
// alice through the refreshed link.
func updateMieruAliceSubscription(t *testing.T, st *e2eMieruState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeB, 1)
	driveMieru(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, false)
}

// restrictMieruAlice limits alice to 192.0.2.0/24: a direct probe from alice's
// link is refused without reaching a recorder, and bob still routes after B restarts.
func restrictMieruAlice(t *testing.T, st *e2eMieruState, newAlice string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeA, 3)
	// Restart to reset dead-node probe backoff after a changed client filter or intentional probe rejection.
	beforeAlice, beforeBob := st.aliceUp.count.Load(), st.bobUp.count.Load()
	if err := e2eMieruProbe(st.ctx, newAlice, st.backend, "filtered-client", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("filtered Alice direct probe was not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), st.allSecrets))
	}
	e2eMieruWaitDropped(t, st.ctx, st.homeA, 1, st.allSecrets...)
	if st.aliceUp.count.Load() != beforeAlice || st.bobUp.count.Load() != beforeBob {
		t.Fatal("filtered Alice request reached a recorder")
	}
	restartMieruClient(t, st)
	driveMieru(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, true)
}

// clearMieruRestriction removes alice's client filter and checks that alice
// routes again after B restarts.
func clearMieruRestriction(t *testing.T, st *e2eMieruState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice restriction failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, st.ctx, st.homeA, 4)
	restartMieruClient(t, st)
	driveMieru(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, true)
}

// restartMieruServer restarts A's angel and checks that the Mieru port closes
// and reopens and that both links survive the restart.
func restartMieruServer(t *testing.T, st *e2eMieruState, newAlice string) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed: %s", serverErrorSummary(out, stderr))
	}
	st.serverAngel.Stop()
	if err := waitServerPort(st.ctx, st.serverPort, false); err != nil {
		t.Fatalf("Mieru port remained open after down: %v", err)
	}
	assertNoSecrets(t, st.originalServerAngel.Stdout(), st.originalServerAngel.Stderr(), st.allSecrets...)
	st.serverAngel = startUp(t, st.homeA, "--port", "0")
	assertStatusSuccess(t, st.serverAngel.Status())
	restartMieruClient(t, st)
	if persisted := getMieruLink(t, st.homeA, "alice"); persisted != newAlice {
		t.Fatal("Alice link changed across server restart")
	}
	if persisted := getMieruLink(t, st.homeA, "bob"); persisted != st.links["bob"] {
		t.Fatal("Bob link changed across server restart")
	}
}

// checkMieruAfterRestart drives both accounts after the restart and checks that
// the new credential is accepted and the old one refused.
func checkMieruAfterRestart(t *testing.T, st *e2eMieruState, oldAlice, newAlice string) {
	t.Helper()
	driveMieru(t, st, "alice", st.alicePass, st.aliceUp, st.bobUp, true)
	driveMieru(t, st, "bob", st.bobPass, st.bobUp, st.aliceUp, true)
	if err := e2eMieruProbe(st.ctx, newAlice, st.backend, "post-restart-valid", false); err != nil {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("correct Alice credential probe after restart failed: %s", e2eMieruRedact(err.Error(), st.allSecrets))
	}
	if err := e2eMieruProbe(st.ctx, oldAlice, st.backend, "old-password", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t.Context(), t, st.homeA, st.homeB, st.allSecrets)
		t.Fatalf("old Alice credential after rotation not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), st.allSecrets))
	}
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.allSecrets...)
}

// removeMieruEdge removes the Mieru edge, checks that its port closes and stops
// both angels.
func removeMieruEdge(t *testing.T, st *e2eMieruState) {
	t.Helper()
	baseline := e2eMieruWaitReload(t, st.ctx, st.homeA, st.allSecrets...)
	code, out, stderr := serverRun(t, st.homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.allSecrets...)
	waitStatusReload(t, st.ctx, st.homeA, baseline+1)
	e2eMieruWaitStatus(t, st.ctx, st.homeA, false, st.allSecrets...)
	if err := waitServerPort(st.ctx, st.serverPort, false); err != nil {
		t.Fatalf("removed Mieru port remains open: %v", err)
	}
	st.serverAngel.Stop()
	st.clientAngel.Stop()
	assertNoSecrets(t, st.serverAngel.Stdout(), st.serverAngel.Stderr(), st.allSecrets...)
	assertNoSecrets(t, st.clientAngel.Stdout(), st.clientAngel.Stderr(), st.allSecrets...)
}

func e2eMieruProbe(ctx context.Context, body, target, payload string, wrongPassword bool) error {
	parsed, err := subscription.Parse([]byte(body))
	if err != nil || len(parsed) != 1 {
		return errors.New("Mieru link import failed")
	}
	node := parsed[0]
	username, _ := node.Raw["username"].(string)
	password, _ := node.Raw["password"].(string)
	transport, _ := node.Raw["transport"].(string)
	multiplexing, _ := node.Raw["multiplexing"].(string)
	if node.Server == "" || node.Port < 1 || username == "" || password == "" || transport != "TCP" {
		return errors.New("incomplete Mieru configuration")
	}
	if wrongPassword {
		password = "incorrect-mieru-password"
	}
	if multiplexing == "" {
		multiplexing = "MULTIPLEXING_OFF"
	}
	client, err := outbound.NewMieru(outbound.MieruOption{Name: "mieru-e2e-probe", Server: node.Server, Port: node.Port, Transport: transport, UserName: username, Password: password, Multiplexing: multiplexing})
	if err != nil {
		return fmt.Errorf("construct Mieru client: %w", err)
	}
	defer func() { _ = client.Close() }()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid echo endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("invalid echo port")
	}
	if port < 0 {
		return errors.New("invalid echo port")
	}
	if port > math.MaxUint16 {
		return errors.New("invalid echo port")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := probeCtx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(port)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	if _, err = io.WriteString(conn, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return errors.New("unexpected Mieru echo")
	}
	return nil
}

func e2eMieruSOCKSEcho(parent context.Context, proxy, login, password, target, payload string) error {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	write := func(data []byte) error {
		_, e := conn.Write(data)
		return e
	}
	read := func(n int) ([]byte, error) {
		data := make([]byte, n)
		_, e := io.ReadFull(conn, data)
		return data, e
	}
	if err = write([]byte{5, 1, 2}); err != nil {
		return err
	}
	reply, err := read(2)
	if err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 2 {
		return errors.New("unexpected SOCKS auth method")
	}
	loginLen := len(login)
	if loginLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	passLen := len(password)
	if passLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	auth := make([]byte, 0, 2+len(login)+1+len(password))
	auth = append(auth, 1, byte(loginLen))
	auth = append(auth, login...)
	auth = append(auth, byte(passLen))
	auth = append(auth, password...)
	if err = write(auth); err != nil {
		return err
	}
	reply, err = read(2)
	if err != nil {
		return err
	}
	if reply[1] != 0 {
		return errors.New("client account authentication refused")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid echo endpoint")
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil {
		return errors.New("invalid echo target")
	}
	if port < 1 {
		return errors.New("invalid echo target")
	}
	if port > math.MaxUint16 {
		return errors.New("invalid echo target")
	}
	request := make([]byte, 0, 4+len(ip)+2)
	request = append(request, 5, 1, 0, 1)
	request = append(request, ip...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if err = write(request); err != nil {
		return err
	}
	reply, err = read(10)
	if err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("SOCKS CONNECT refused (%d)", reply[1])
	}
	if err = write([]byte(payload)); err != nil {
		return err
	}
	got, err := read(len(payload))
	if err != nil {
		return err
	}
	if string(got) != payload {
		return errors.New("unexpected routed echo")
	}
	return nil
}

func e2eMieruExtractPassword(t *testing.T, body string) string {
	t.Helper()
	var data struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(body), &data); err != nil || len(data.Proxies) != 1 {
		t.Fatal("decode Mieru Mihomo config")
	}
	password, _ := data.Proxies[0]["password"].(string)
	if password == "" {
		t.Fatal("Mieru password missing")
	}
	return password
}

// Mieru is probe resistant: a client with a wrong credential gets no answer at
// all and sees a timeout, so a timeout counts as a refusal here. Every refusal
// check follows a successful probe that proves the server is up.
func e2eMieruRejectedError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{"forcibly closed", "connection reset", "connection was aborted", "closed pipe", "timeout"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func e2eMieruWaitStatus(t *testing.T, ctx context.Context, home string, wantPresent bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var data struct {
				Data struct {
					Servers []struct {
						Name        string   `json:"name"`
						Protocol    string   `json:"protocol"`
						Listen      string   `json:"listen"`
						Accounts    []string `json:"accounts"`
						Connections int      `json:"connections"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &data) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				present := false
				for _, s := range data.Data.Servers {
					if s.Name == "edge" {
						present = true
						if wantPresent && (s.Protocol != "mieru" || s.Listen == "" || s.Connections < 0 || len(s.Accounts) != 2) {
							t.Fatal("Mieru status incomplete")
						}
					}
				}
				if present == wantPresent {
					return
				}
			}
		} else if !e2eMieruAngelNotRunning(stderr) {
			t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Mieru status wait expired")
			t.Fatalf("Mieru status did not become present=%t", wantPresent)
		case <-ticker.C:
		}
	}
}

func e2eMieruWaitReload(t *testing.T, ctx context.Context, home string, secrets ...string) uint64 {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var status liveStatus
			if json.Unmarshal([]byte(out), &status) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				return status.Data.Reload.Count
			}
		} else if !e2eMieruAngelNotRunning(stderr) {
			t.Fatalf("status failed: %s", serverErrorSummary(out, stderr))
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Mieru reload status wait expired")
			t.Fatal("status did not become available")
		case <-ticker.C:
		}
	}
}

func e2eMieruWaitDropped(t *testing.T, ctx context.Context, home string, minimum uint64, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var data struct {
				Data struct {
					Servers []struct {
						Name    string `json:"name"`
						Dropped uint64 `json:"dropped_clients"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &data) == nil {
				assertNoSecrets(t, out, stderr, secrets...)
				for _, s := range data.Data.Servers {
					if s.Name == "edge" && s.Dropped >= minimum {
						return
					}
				}
			}
		} else if !e2eMieruAngelNotRunning(stderr) {
			t.Fatalf("drop status failed: %s", serverErrorSummary(out, stderr))
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Mieru dropped status wait expired")
			t.Fatal("Mieru dropped-client counter did not increment")
		case <-ticker.C:
		}
	}
}

func e2eMieruAngelNotRunning(stderr string) bool {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(stderr), &envelope) == nil && envelope.Error.Code == "ANGEL_NOT_RUNNING"
}

func e2eMieruDiagnostics(ctx context.Context, t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	for _, home := range []struct{ label, path string }{{"A", homeA}, {"B", homeB}} {
		for _, args := range [][]string{{"status", "--full"}, {"logs", "--since", "2m", "--tail", "50"}, {"node", "list"}} {
			code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home.path, args...)
			safe := e2eMieruRedact(out+"\n"+stderr, secrets)
			if len(safe) > 12*1024 {
				safe = safe[:12*1024] + "\n[truncated]"
			}
			t.Logf("Mieru diagnostic home=%s command=%s exit=%d:\n%s", home.label, strings.Join(args, " "), code, safe)
		}
	}
}

func e2eMieruRedact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return regexp.MustCompile(`(?i)\b(?:mierus|https?|socks5)://[^\s"'<>]+`).ReplaceAllString(text, "[redacted-url]")
}
