//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// e2eSudokuPhaseTimeout bounds each wait and drive of the Sudoku lifecycle.
const e2eSudokuPhaseTimeout = 35 * time.Second

func TestE2EServerSudokuLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	st := &e2eSudokuState{ctx: ctx}
	setupSudokuServerA(t, st)
	readSudokuLinks(t, st)
	subAddr := startSudokuSubscriptions(t, st)
	setupSudokuClientB(t, st, "http://"+subAddr)
	checkSudokuInitialRoutes(t, st)
	rotateSudokuAlice(t, st)
	restrictSudokuAlice(t, st)
	clearSudokuRestriction(t, st)
	restartSudokuServer(t, st)
	removeSudokuEdges(t, st)
}

// e2eSudokuState is the shared state of TestE2EServerSudokuLifecycle. secrets
// grows as keys and links are created.
type e2eSudokuState struct {
	ctx     context.Context
	pass    string
	hash    string
	backend string
	aliceUp *accountsUpstream
	bobUp   *accountsUpstream
	homeA   string
	homeB   string
	ports   map[string]int
	secrets []string
	links   map[string]string
	linksMu sync.RWMutex
	serverA *upProc
	clientB *upProc
}

// setupSudokuServerA writes A's fixture, starts A's angel and adds one Sudoku
// edge per account.
func setupSudokuServerA(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	const pass = "sudoku-e2e-account-password" //nolint:gosec // G101: fixture password of a test account, not a credential
	st.pass = pass
	st.backend = accountsEcho(t)
	st.aliceUp, st.bobUp = accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	st.hash = hash
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "alice-route"},
			{Login: "bob", Password: hash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, st.homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", st.aliceUp)})
	accountsSaveSnapshot(t, st.homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", st.bobUp)})
	st.serverA = startUp(t, st.homeA, "--port", "0")
	st.ports = map[string]int{"sudoku-alice": freePort(t), "sudoku-bob": freePort(t)}
	for st.ports["sudoku-alice"] == st.ports["sudoku-bob"] {
		st.ports["sudoku-bob"] = freePort(t)
	}
	st.secrets = []string{pass, hash}
	for i, entry := range []struct{ name, account string }{{"sudoku-alice", "alice"}, {"sudoku-bob", "bob"}} {
		code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "sudoku", "--name", entry.name, "--account", entry.account, "--port", strconv.Itoa(st.ports[entry.name]), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
		if code != 0 {
			t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		waitSudokuReload(t, st, st.homeA, uint64(i+1))
		waitSudokuStatus(t, st, entry.name, true, st.secrets...)
	}
}

// waitSudokuReload waits until home has reloaded count times, within the phase timeout.
func waitSudokuReload(t *testing.T, st *e2eSudokuState, home string, count uint64) {
	t.Helper()
	waitCtx, cancelWait := context.WithTimeout(st.ctx, e2eSudokuPhaseTimeout)
	defer cancelWait()
	waitStatusReload(t, waitCtx, home, count)
}

// waitSudokuStatus waits until the named edge is present or absent on A, within the phase timeout.
func waitSudokuStatus(t *testing.T, st *e2eSudokuState, name string, present bool, secrets ...string) {
	t.Helper()
	waitCtx, cancelWait := context.WithTimeout(st.ctx, e2eSudokuPhaseTimeout)
	defer cancelWait()
	e2eSudokuWaitStatus(t, waitCtx, st.homeA, name, present, secrets...)
}

// waitSudokuPortClosed waits until port stops listening, within the phase timeout.
func waitSudokuPortClosed(st *e2eSudokuState, port int) error {
	waitCtx, cancelWait := context.WithTimeout(st.ctx, e2eSudokuPhaseTimeout)
	defer cancelWait()
	return waitServerPort(waitCtx, port, false)
}

// sudokuLink returns the Mihomo Sudoku link of the named edge and adds its key
// and link to the redaction list.
func sudokuLink(t *testing.T, st *e2eSudokuState, name string) string {
	t.Helper()
	code, out, stderr := serverRun(t, st.homeA, "link", name, "--format", "mihomo")
	if code != 0 {
		t.Fatalf("link failed: %s", serverErrorSummary(out, stderr))
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &response) != nil || !response.OK || response.Data.Link == "" {
		t.Fatal("Mihomo YAML link missing")
	}
	key := e2eSudokuKey(response.Data.Link)
	if key == "" {
		t.Fatal("Sudoku YAML omitted key")
	}
	st.secrets = append(st.secrets, key, response.Data.Link)
	assertNoSecrets(t, out, stderr, st.pass, st.hash)
	return response.Data.Link
}

// readSudokuLinks reads the links of alice and bob.
func readSudokuLinks(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	st.links = map[string]string{"alice": sudokuLink(t, st, "sudoku-alice"), "bob": sudokuLink(t, st, "sudoku-bob")}
}

// startSudokuSubscriptions serves each account's current link on the loopback
// and returns the listener address.
func startSudokuSubscriptions(t *testing.T, st *e2eSudokuState) string {
	t.Helper()
	subMux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		subMux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			st.linksMu.RLock()
			defer st.linksMu.RUnlock()
			_, _ = io.WriteString(w, st.links[account]+"\n")
		})
	}
	return serveLoopbackSubscriptions(t, subMux)
}

// setupSudokuClientB imports both subscriptions into B, adds one account per
// provider and starts B's angel.
func setupSudokuClientB(t *testing.T, st *e2eSudokuState, baseURL string) {
	t.Helper()
	st.homeB = t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr := accountsRun(t.Context(), t, st.homeB, "sub", "add", baseURL+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		code, out, stderr = accountsRun(t.Context(), t, st.homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
	}
	st.clientB = startUp(t, st.homeB, "--port", "0")
}

// restartSudokuClient restarts B's angel.
func restartSudokuClient(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeB, "down")
	if code != 0 {
		t.Fatalf("B down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.clientB.Stop()
	st.clientB = startUp(t, st.homeB, "--port", "0")
}

// driveSudoku runs one routed echo through B for account, within the phase timeout.
func driveSudoku(t *testing.T, st *e2eSudokuState, account string, own, other *accountsUpstream, retry bool) error {
	phaseCtx, cancelDrive := context.WithTimeout(st.ctx, e2eSudokuPhaseTimeout)
	defer cancelDrive()
	return e2eSudokuDrive(t, phaseCtx, st.homeA, st.homeB, st.clientB, st.backend, account, st.pass, own, other, retry, st.secrets...)
}

// probeSudoku runs one Sudoku probe against the backend, within the phase timeout.
func probeSudoku(st *e2eSudokuState, text, key string) error {
	phaseCtx, cancelProbe := context.WithTimeout(st.ctx, e2eSudokuPhaseTimeout)
	defer cancelProbe()
	return e2eSudokuProbe(phaseCtx, text, key, st.backend)
}

// checkSudokuInitialRoutes checks both routed echoes, then that a valid key
// connects and a wrong one is rejected.
func checkSudokuInitialRoutes(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	if err := driveSudoku(t, st, "alice", st.aliceUp, st.bobUp, false); err != nil {
		t.Fatalf("Alice routed echo: %v", err)
	}
	if err := driveSudoku(t, st, "bob", st.bobUp, st.aliceUp, false); err != nil {
		t.Fatalf("Bob routed echo: %v", err)
	}
	if err := probeSudoku(st, st.links["alice"], ""); err != nil {
		t.Fatalf("valid key probe failed: %v", err)
	}
	wrongRaw := make([]byte, 32)
	if _, err := rand.Read(wrongRaw); err != nil {
		t.Fatal("generate wrong key")
	}
	wrongKey := base64.RawURLEncoding.EncodeToString(wrongRaw)
	st.secrets = append(st.secrets, wrongKey)
	if err := probeSudoku(st, st.links["alice"], wrongKey); !errors.Is(err, errE2ESudokuRejected) {
		t.Fatalf("wrong key was not rejected: %v", err)
	}
}

// rotateSudokuAlice rotates alice's key, checks that the old link is refused and
// publishes the new link through B's refreshed subscription.
func rotateSudokuAlice(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	baseline := liveStatusOf(t, st.homeA).Data.Reload.Count
	oldAlice := st.links["alice"]
	code, out, stderr := serverRun(t, st.homeA, "rotate", "sudoku-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitSudokuReload(t, st, st.homeA, baseline+1)
	st.linksMu.Lock()
	st.links["alice"] = sudokuLink(t, st, "sudoku-alice")
	st.linksMu.Unlock()
	if err := probeSudoku(st, oldAlice, ""); !errors.Is(err, errE2ESudokuRejected) {
		t.Fatalf("old key not rejected after rotate: %v", err)
	}
	code, out, stderr = accountsRun(t.Context(), t, st.homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription refresh failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitSudokuReload(t, st, st.homeB, 1)
	restartSudokuClient(t, st)
	if err := driveSudoku(t, st, "alice", st.aliceUp, st.bobUp, false); err != nil {
		t.Fatalf("refreshed Alice link: %v", err)
	}
}

// restrictSudokuAlice limits alice to 192.0.2.0/24: alice's link is refused, and
// bob still routes after B restarts.
func restrictSudokuAlice(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	baseline := liveStatusOf(t, st.homeA).Data.Reload.Count
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitSudokuReload(t, st, st.homeA, baseline+1)
	if err := probeSudoku(st, st.links["alice"], ""); !errors.Is(err, errE2ESudokuRejected) {
		t.Fatalf("restricted Alice probe not refused: %v", err)
	}
	restartSudokuClient(t, st)
	if err := driveSudoku(t, st, "bob", st.bobUp, st.aliceUp, true); err != nil {
		t.Fatalf("Bob route with Alice restricted: %v", err)
	}
}

// clearSudokuRestriction clears alice's client filter and checks that both
// accounts route again after B restarts.
func clearSudokuRestriction(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	baseline := liveStatusOf(t, st.homeA).Data.Reload.Count
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	waitSudokuReload(t, st, st.homeA, baseline+1)
	restartSudokuClient(t, st)
	if err := driveSudoku(t, st, "alice", st.aliceUp, st.bobUp, true); err != nil {
		t.Fatalf("Alice route with restriction cleared: %v", err)
	}
	if err := driveSudoku(t, st, "bob", st.bobUp, st.aliceUp, true); err != nil {
		t.Fatalf("Bob route after allowlist clear: %v", err)
	}
}

// restartSudokuServer restarts A's angel and checks that both listeners close and
// reopen, the links are unchanged and both accounts route again.
func restartSudokuServer(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	stableAlice, stableBob := st.links["alice"], st.links["bob"]
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "down")
	if code != 0 {
		t.Fatalf("A down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, st.secrets...)
	st.serverA.Stop()
	if err := waitSudokuPortClosed(st, st.ports["sudoku-alice"]); err != nil {
		t.Fatalf("Alice listener not closed: %v", err)
	}
	if err := waitSudokuPortClosed(st, st.ports["sudoku-bob"]); err != nil {
		t.Fatalf("Bob listener not closed: %v", err)
	}
	st.serverA = startUp(t, st.homeA, "--port", "0")
	if sudokuLink(t, st, "sudoku-alice") != stableAlice || sudokuLink(t, st, "sudoku-bob") != stableBob {
		t.Fatal("server link changed across restart")
	}
	restartSudokuClient(t, st)
	if err := driveSudoku(t, st, "alice", st.aliceUp, st.bobUp, true); err != nil {
		t.Fatalf("Alice route after A restart: %v", err)
	}
	if err := driveSudoku(t, st, "bob", st.bobUp, st.aliceUp, true); err != nil {
		t.Fatalf("Bob route after A restart: %v", err)
	}
}

// removeSudokuEdges removes both edges, checks that their listeners close and
// stops both angels.
func removeSudokuEdges(t *testing.T, st *e2eSudokuState) {
	t.Helper()
	for _, name := range []string{"sudoku-alice", "sudoku-bob"} {
		baseline := liveStatusOf(t, st.homeA).Data.Reload.Count
		code, out, stderr := serverRun(t, st.homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove %s failed: %s", name, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, st.secrets...)
		waitSudokuReload(t, st, st.homeA, baseline+1)
		waitSudokuStatus(t, st, name, false, st.secrets...)
		if err := waitSudokuPortClosed(st, st.ports[name]); err != nil {
			t.Fatalf("removed %s port remains open: %v", name, err)
		}
	}
	st.serverA.Stop()
	st.clientB.Stop()
	assertNoSecrets(t, st.serverA.Stdout(), st.serverA.Stderr(), st.secrets...)
	assertNoSecrets(t, st.clientB.Stdout(), st.clientB.Stderr(), st.secrets...)
}

var errE2ESudokuRejected = errors.New("sudoku connection rejected")

func e2eSudokuKey(text string) string {
	nodes, err := subscription.Parse([]byte(text))
	if err != nil || len(nodes) != 1 {
		return ""
	}
	key, _ := nodes[0].Raw["key"].(string)
	return key
}

func e2eSudokuProbe(ctx context.Context, text, key, target string) error {
	nodes, err := subscription.Parse([]byte(text))
	if err != nil || len(nodes) != 1 {
		return errors.New("Mihomo YAML parse failed")
	}
	node := nodes[0]
	if key == "" {
		key, _ = node.Raw["key"].(string)
	}
	if key == "" {
		return errors.New("Sudoku YAML key missing")
	}
	client, err := outbound.NewSudoku(outbound.SudokuOption{
		Name:         "sudoku-e2e-probe",
		Server:       node.Server,
		Port:         node.Port,
		Key:          key,
		HTTPMask:     e2eSudokuBoolPtr(true),
		HTTPMaskMode: "legacy",
	})
	if err != nil {
		return errors.New("Sudoku client setup failed")
	}
	defer func() { _ = client.Close() }()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("echo target invalid")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("echo port invalid")
	}
	if port < 0 {
		return errors.New("echo port invalid")
	}
	if port > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(port)})
	if err != nil {
		return e2eSudokuProbeFailure(err, probeCtx)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("sudoku-e2e-probe")
	if _, err = conn.Write(payload); err != nil {
		return e2eSudokuProbeFailure(err, probeCtx)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return e2eSudokuProbeFailure(err, probeCtx)
	}
	if string(got) != string(payload) {
		return errors.New("unexpected echo")
	}
	return nil
}

func e2eSudokuProbeFailure(err error, ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("Sudoku probe timed out")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("Sudoku probe timed out")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, syscall.Errno(10053)) {
		return errE2ESudokuRejected
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{"eof", "reset", "broken pipe", "forcibly closed", "aborted", "10053", "10054"} {
		if strings.Contains(lower, marker) {
			return errE2ESudokuRejected
		}
	}
	return errors.New("unclassified Sudoku network failure")
}

func e2eSudokuAccountClient(ctx context.Context, proxy, user, pass, target string) error {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return errors.New("B proxy dial failed")
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return errors.New("SOCKS greeting write failed")
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS auth method rejected")
	}
	userLen := len(user)
	if userLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	passLen := len(pass)
	if passLen > 255 {
		return errors.New("SOCKS credentials too long")
	}
	authRequest := make([]byte, 0, 2+len(user)+1+len(pass))
	authRequest = append(authRequest, 1, byte(userLen))
	authRequest = append(authRequest, user...)
	authRequest = append(authRequest, byte(passLen))
	authRequest = append(authRequest, pass...)
	if _, err = conn.Write(authRequest); err != nil {
		return errors.New("SOCKS auth write failed")
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil || authReply[1] != 0 {
		return errors.New("SOCKS authentication rejected")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("echo target invalid")
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return errors.New("echo target is not IPv4")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("echo port invalid")
	}
	if port < 0 {
		return errors.New("echo port invalid")
	}
	if port > math.MaxUint16 {
		return errors.New("echo port invalid")
	}
	request := make([]byte, 0, 4+len(ip)+2)
	request = append(request, 5, 1, 0, 1)
	request = append(request, ip...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err = conn.Write(request); err != nil {
		return errors.New("SOCKS connect write failed")
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[1] != 0 {
		return errors.New("SOCKS connect rejected")
	}
	payload := []byte("sudoku-account-route")
	if _, err = conn.Write(payload); err != nil {
		return errors.New("SOCKS payload write failed")
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
		return errors.New("SOCKS echo failed")
	}
	return nil
}

func e2eSudokuDrive(t *testing.T, ctx context.Context, homeA, homeB string, client *upProc, backend, user, pass string, own, other *accountsUpstream, retry bool, secrets ...string) error {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	beforeOwn, beforeOther := own.count.Load(), other.count.Load()
	if !retry {
		deadline = time.Now()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = e2eSudokuAccountClient(ctx, client.Status().Data.Listen, user, pass, backend)
		if last == nil && own.count.Load() > beforeOwn && other.count.Load() == beforeOther {
			return nil
		}
		if last == nil {
			last = errors.New("account recorder isolation mismatch")
			break
		}
		if !retry || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("routed connection cancelled")
		case <-ticker.C:
		}
	}
	diagnostics := e2eSudokuDiagnostics(t, ctx, homeA, homeB, secrets...)
	t.Logf("Sudoku route diagnostics (last: %v): %s", last, diagnostics)
	return errors.New("routed connection failed")
}

func e2eSudokuDiagnostics(t *testing.T, ctx context.Context, homeA, homeB string, secrets ...string) string {
	t.Helper()
	codeA, outA, errA := accountsRun(context.WithoutCancel(ctx), t, homeA, "status")
	codeB, outB, errB := accountsRun(context.WithoutCancel(ctx), t, homeB, "status")
	assertNoSecrets(t, outA, errA, secrets...)
	assertNoSecrets(t, outB, errB, secrets...)
	return fmt.Sprintf("A status exit %d; %s; B status exit %d; %s; B node list: %s; deadline %v", codeA, e2eSudokuServers(outA), codeB, e2eSudokuServers(outB), e2eSudokuNodeHealth(context.WithoutCancel(ctx), t, homeB, secrets...), ctx.Err()) + "; A logs: " + serverE2ELogTail(ctx, t, homeA) + "; B logs: " + serverE2ELogTail(ctx, t, homeB)
}

func e2eSudokuServers(raw string) string {
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
	if json.Unmarshal([]byte(raw), &env) != nil {
		return "unavailable"
	}
	return fmt.Sprintf("%+v", env.Data.Servers)
}

func e2eSudokuNodeHealth(ctx context.Context, t *testing.T, home string, secrets ...string) string {
	t.Helper()
	var summary []string
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "account", "show", account)
		assertNoSecrets(t, out, stderr, secrets...)
		if code != 0 {
			summary = append(summary, account+": unavailable")
			continue
		}
		var env struct {
			Data struct {
				Tiers []struct {
					Nodes []struct {
						Provider string `json:"provider"`
						Name     string `json:"name"`
						Alive    *bool  `json:"alive"`
						Down     *bool  `json:"down"`
						DelayMS  *int64 `json:"delay_ms"`
					} `json:"nodes"`
				} `json:"tiers"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &env) != nil {
			summary = append(summary, account+": unavailable")
			continue
		}
		var nodes []string
		for _, tier := range env.Data.Tiers {
			for _, n := range tier.Nodes {
				nodes = append(nodes, fmt.Sprintf("%s/%s alive=%s down=%s delay_ms=%s", n.Provider, n.Name, e2eSudokuOptional(n.Alive), e2eSudokuOptional(n.Down), e2eSudokuOptional(n.DelayMS)))
			}
		}
		summary = append(summary, account+": "+strings.Join(nodes, ", "))
	}
	return strings.Join(summary, "; ")
}

func e2eSudokuWaitStatus(t *testing.T, ctx context.Context, home, name string, present bool, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Reload struct {
						Count     uint64 `json:"count"`
						LastError string `json:"last_error"`
					} `json:"reload"`
					Servers []struct {
						Name        string `json:"name"`
						Protocol    string `json:"protocol"`
						Listen      string `json:"listen"`
						Connections int    `json:"connections"`
					} `json:"servers"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(out), &env) == nil {
				found := false
				for _, server := range env.Data.Servers {
					if server.Name == name {
						found = true
						if present && (server.Protocol != "sudoku" || server.Listen == "") {
							t.Fatal("Sudoku status incomplete")
						}
					}
				}
				assertNoSecrets(t, out, stderr, secrets...)
				if found == present {
					return
				}
				if env.Data.Reload.LastError != "" {
					serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, "Sudoku reload failed")
					t.Fatalf("Sudoku reload failed: %s", e2eRedactDiagnostic(env.Data.Reload.LastError))
				}
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(context.WithoutCancel(ctx), t, home, fmt.Sprintf("Sudoku status wait expired (%v)", ctx.Err()))
			t.Fatal("Sudoku status mismatch")
		case <-ticker.C:
		}
	}
}

func e2eSudokuBoolPtr(value bool) *bool { return &value }

func e2eSudokuOptional[T any](v *T) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprint(*v)
}
