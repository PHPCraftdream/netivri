//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestE2EServerSudokuLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const phaseTimeout = 35 * time.Second
	const pass = "sudoku-e2e-account-password"
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: hash, Provider: "alice-route"},
			{Login: "bob", Password: hash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", bobUp)})
	serverA := startUp(t, homeA, "--port", "0")
	waitReload := func(home string, count uint64) {
		waitCtx, cancelWait := context.WithTimeout(ctx, phaseTimeout)
		defer cancelWait()
		waitStatusReload(t, waitCtx, home, count)
	}
	waitStatus := func(name string, present bool, secrets ...string) {
		waitCtx, cancelWait := context.WithTimeout(ctx, phaseTimeout)
		defer cancelWait()
		e2eSudokuWaitStatus(t, waitCtx, homeA, name, present, secrets...)
	}
	waitPortClosed := func(port int) error {
		waitCtx, cancelWait := context.WithTimeout(ctx, phaseTimeout)
		defer cancelWait()
		return waitServerPort(waitCtx, port, false)
	}
	ports := map[string]int{"sudoku-alice": freePort(t), "sudoku-bob": freePort(t)}
	for ports["sudoku-alice"] == ports["sudoku-bob"] {
		ports["sudoku-bob"] = freePort(t)
	}
	secrets := []string{pass, hash}
	for i, entry := range []struct{ name, account string }{{"sudoku-alice", "alice"}, {"sudoku-bob", "bob"}} {
		code, out, stderr := serverRun(t, homeA, "add", "--protocol", "sudoku", "--name", entry.name, "--account", entry.account, "--port", strconv.Itoa(ports[entry.name]), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
		if code != 0 {
			t.Fatalf("server add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		waitReload(homeA, uint64(i+1))
		waitStatus(entry.name, true, secrets...)
	}

	link := func(name string) string {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, "link", name, "--format", "mihomo")
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
		secrets = append(secrets, key, response.Data.Link)
		assertNoSecrets(t, out, stderr, pass, hash)
		return response.Data.Link
	}
	var linksMu sync.RWMutex
	links := map[string]string{"alice": link("sudoku-alice"), "bob": link("sudoku-bob")}
	subMux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		account := account
		subMux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			linksMu.RLock()
			defer linksMu.RUnlock()
			_, _ = io.WriteString(w, links[account]+"\n")
		})
	}
	subHTTP := &http.Server{Handler: subMux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for subscription")
	}
	subDone := make(chan struct{})
	t.Cleanup(func() {
		_ = subHTTP.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	go func() { defer close(subDone); _ = subHTTP.Serve(subListener) }()
	homeB := t.TempDir()
	baseURL := "http://" + subListener.Addr().String()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		code, out, stderr := accountsRun(t, homeB, "sub", "add", baseURL+"/"+account, "--name", provider)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		code, out, stderr = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if code != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
	}
	clientB := startUp(t, homeB, "--port", "0")
	restartB := func() {
		t.Helper()
		code, out, stderr := accountsRun(t, homeB, "down")
		if code != 0 {
			t.Fatalf("B down failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		clientB.Stop()
		clientB = startUp(t, homeB, "--port", "0")
	}
	drive := func(account string, own, other *accountsUpstream, retry bool) error {
		phaseCtx, cancelDrive := context.WithTimeout(ctx, phaseTimeout)
		defer cancelDrive()
		return e2eSudokuDrive(t, phaseCtx, homeA, homeB, clientB, backend, account, pass, own, other, retry, secrets...)
	}
	if err := drive("alice", aliceUp, bobUp, false); err != nil {
		t.Fatalf("Alice routed echo: %v", err)
	}
	if err := drive("bob", bobUp, aliceUp, false); err != nil {
		t.Fatalf("Bob routed echo: %v", err)
	}
	probe := func(text, key string) error {
		phaseCtx, cancelProbe := context.WithTimeout(ctx, phaseTimeout)
		defer cancelProbe()
		return e2eSudokuProbe(phaseCtx, text, key, backend)
	}
	if err := probe(links["alice"], ""); err != nil {
		t.Fatalf("valid key probe failed: %v", err)
	}
	wrongRaw := make([]byte, 32)
	if _, err := rand.Read(wrongRaw); err != nil {
		t.Fatal("generate wrong key")
	}
	wrongKey := base64.RawURLEncoding.EncodeToString(wrongRaw)
	secrets = append(secrets, wrongKey)
	if err := probe(links["alice"], wrongKey); !errors.Is(err, e2eSudokuRejected) {
		t.Fatalf("wrong key was not rejected: %v", err)
	}

	baseline := liveStatusOf(t, homeA).Data.Reload.Count
	oldAlice := links["alice"]
	code, out, stderr := serverRun(t, homeA, "rotate", "sudoku-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotate failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitReload(homeA, baseline+1)
	linksMu.Lock()
	links["alice"] = link("sudoku-alice")
	linksMu.Unlock()
	if err := probe(oldAlice, ""); !errors.Is(err, e2eSudokuRejected) {
		t.Fatalf("old key not rejected after rotate: %v", err)
	}
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription refresh failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitReload(homeB, 1)
	restartB()
	if err := drive("alice", aliceUp, bobUp, false); err != nil {
		t.Fatalf("refreshed Alice link: %v", err)
	}

	baseline = liveStatusOf(t, homeA).Data.Reload.Count
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitReload(homeA, baseline+1)
	if err := probe(links["alice"], ""); !errors.Is(err, e2eSudokuRejected) {
		t.Fatalf("restricted Alice probe not refused: %v", err)
	}
	restartB()
	if err := drive("bob", bobUp, aliceUp, true); err != nil {
		t.Fatalf("Bob route with Alice restricted: %v", err)
	}
	baseline = liveStatusOf(t, homeA).Data.Reload.Count
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	waitReload(homeA, baseline+1)
	restartB()
	if err := drive("alice", aliceUp, bobUp, true); err != nil {
		t.Fatalf("Alice route with restriction cleared: %v", err)
	}
	if err := drive("bob", bobUp, aliceUp, true); err != nil {
		t.Fatalf("Bob route after allowlist clear: %v", err)
	}

	stableAlice, stableBob := links["alice"], links["bob"]
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("A down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secrets...)
	serverA.Stop()
	if err := waitPortClosed(ports["sudoku-alice"]); err != nil {
		t.Fatalf("Alice listener not closed: %v", err)
	}
	if err := waitPortClosed(ports["sudoku-bob"]); err != nil {
		t.Fatalf("Bob listener not closed: %v", err)
	}
	serverA = startUp(t, homeA, "--port", "0")
	if link("sudoku-alice") != stableAlice || link("sudoku-bob") != stableBob {
		t.Fatal("server link changed across restart")
	}
	restartB()
	if err := drive("alice", aliceUp, bobUp, true); err != nil {
		t.Fatalf("Alice route after A restart: %v", err)
	}
	if err := drive("bob", bobUp, aliceUp, true); err != nil {
		t.Fatalf("Bob route after A restart: %v", err)
	}

	for _, name := range []string{"sudoku-alice", "sudoku-bob"} {
		baseline = liveStatusOf(t, homeA).Data.Reload.Count
		code, out, stderr = serverRun(t, homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove %s failed: %s", name, serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secrets...)
		waitReload(homeA, baseline+1)
		waitStatus(name, false, secrets...)
		if err := waitPortClosed(ports[name]); err != nil {
			t.Fatalf("removed %s port remains open: %v", name, err)
		}
	}
	serverA.Stop()
	clientB.Stop()
	assertNoSecrets(t, serverA.Stdout(), serverA.Stderr(), secrets...)
	assertNoSecrets(t, clientB.Stdout(), clientB.Stderr(), secrets...)
}

var e2eSudokuRejected = errors.New("Sudoku connection rejected")

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
	defer client.Close()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("echo target invalid")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("echo port invalid")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	conn, err := client.DialContext(probeCtx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(port)})
	if err != nil {
		return e2eSudokuProbeFailure(err, probeCtx)
	}
	defer conn.Close()
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
		return e2eSudokuRejected
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{"eof", "reset", "broken pipe", "forcibly closed", "aborted", "10053", "10054"} {
		if strings.Contains(lower, marker) {
			return e2eSudokuRejected
		}
	}
	return errors.New("unclassified Sudoku network failure")
}

func e2eSudokuAccountClient(proxy, user, pass, target string) error {
	conn, err := net.DialTimeout("tcp", proxy, 2*time.Second)
	if err != nil {
		return errors.New("B proxy dial failed")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return errors.New("SOCKS greeting write failed")
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS auth method rejected")
	}
	authRequest := []byte{1, byte(len(user))}
	authRequest = append(authRequest, user...)
	authRequest = append(authRequest, byte(len(pass)))
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
	request := []byte{5, 1, 0, 1}
	request = append(request, ip...)
	request = append(request, byte(port>>8), byte(port))
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
		last = e2eSudokuAccountClient(client.Status().Data.Listen, user, pass, backend)
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
			return fmt.Errorf("routed connection cancelled")
		case <-ticker.C:
		}
	}
	diagnostics := e2eSudokuDiagnostics(t, ctx, homeA, homeB, secrets...)
	t.Logf("Sudoku route diagnostics (last: %v): %s", last, diagnostics)
	return errors.New("routed connection failed")
}

func e2eSudokuDiagnostics(t *testing.T, ctx context.Context, homeA, homeB string, secrets ...string) string {
	t.Helper()
	codeA, outA, errA := accountsRun(t, homeA, "status")
	codeB, outB, errB := accountsRun(t, homeB, "status")
	assertNoSecrets(t, outA, errA, secrets...)
	assertNoSecrets(t, outB, errB, secrets...)
	return fmt.Sprintf("A status exit %d; %s; B status exit %d; %s; B node list: %s; deadline %v", codeA, e2eSudokuServers(outA), codeB, e2eSudokuServers(outB), e2eSudokuNodeHealth(t, homeB, secrets...), ctx.Err()) + "; A logs: " + serverE2ELogTail(t, homeA) + "; B logs: " + serverE2ELogTail(t, homeB)
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

func e2eSudokuNodeHealth(t *testing.T, home string, secrets ...string) string {
	t.Helper()
	var summary []string
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr := accountsRun(t, home, "account", "show", account)
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
		code, out, stderr := accountsRun(t, home, "status")
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
					serverE2EFailureDiagnostics(t, home, "Sudoku reload failed")
					t.Fatalf("Sudoku reload failed: %s", e2eRedactDiagnostic(env.Data.Reload.LastError))
				}
			}
		}
		select {
		case <-ctx.Done():
			serverE2EFailureDiagnostics(t, home, fmt.Sprintf("Sudoku status wait expired (%v)", ctx.Err()))
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
