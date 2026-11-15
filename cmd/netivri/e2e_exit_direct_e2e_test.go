//go:build e2e

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

type exitDirectRecorder struct {
	listener net.Listener
	backend  string
	count    atomic.Int64
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func startE2EExitDirectRecorder(t *testing.T, backend string) *exitDirectRecorder {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("recorder listen:", err)
	}
	r := &exitDirectRecorder{listener: ln, backend: backend, conns: make(map[net.Conn]struct{})}
	acceptDone := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns[c] = struct{}{}
			r.mu.Unlock()
			r.wg.Add(1)
			go r.serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		r.mu.Lock()
		for c := range r.conns {
			_ = c.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
	return r
}

func (r *exitDirectRecorder) serve(c net.Conn) {
	defer r.wg.Done()
	defer func() { r.mu.Lock(); delete(r.conns, c); r.mu.Unlock(); _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
		return
	}
	methods := make([]byte, int(h[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		b := make([]byte, int(n[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	var pb [2]byte
	if _, err := io.ReadFull(c, pb[:]); err != nil {
		return
	}
	dest := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
	up, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", dest)
	if err != nil {
		_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = up.Close() }()
	_ = up.SetDeadline(time.Now().Add(20 * time.Second))
	if dest == r.backend {
		r.count.Add(1)
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}

// exitDirectState is the shared state of TestE2EExitDirectServerAccountRouting.
type exitDirectState struct {
	ctx         context.Context
	backend     string
	recorder    *exitDirectRecorder
	alicePass   string
	bobPass     string
	aliceHash   string
	bobHash     string
	secrets     []string
	homeA       string
	serverAngel *upProc
	clientAngel *upProc
	links       map[string]string
}

func TestE2EExitDirectServerAccountRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := &exitDirectState{ctx: ctx}
	setupExitDirectServer(t, st)
	checkExitDirectServerTraffic(t, st)
	st.links = map[string]string{"alice": exitDirectLink(t, st.homeA, "alice"), "bob": exitDirectLink(t, st.homeA, "bob")}
	subAddr := startExitDirectSubscriptions(t, st.links)
	setupExitDirectClient(t, st, subAddr)
	checkExitDirectClientTraffic(t, st)

	held := e2eExitDirectOpenVMess(t, st.links["bob"], st.backend)
	defer func() { _ = held.Close() }()
	e2eExitDirectSendExpect(t, held, "established-before-reload")
	checkExitDirectRouteModes(t, st, held)
	checkExitDirectRejectedDirect(t, st)
	checkExitDirectDirectMode(t, st, held)
	_ = held.Close()

	checkExitDirectValidation(t, st)
	checkExitDirectPreWriteCountry(t, st)
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
}

// setupExitDirectServer starts A's recorder and server angel, then adds the
// VMess edge for alice and bob.
func setupExitDirectServer(t *testing.T, st *exitDirectState) {
	t.Helper()
	st.backend = accountsEcho(t)
	st.recorder = startE2EExitDirectRecorder(t, st.backend)
	const alicePass, bobPass = "exit-direct-alice-password", "exit-direct-bob-password" //nolint:gosec // G101: fixture passwords of test accounts, not credentials
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob password")
	}
	st.alicePass, st.bobPass = alicePass, bobPass
	st.aliceHash, st.bobHash = aliceHash, bobHash
	st.secrets = []string{alicePass, bobPass, aliceHash, bobHash}
	st.homeA = t.TempDir()
	writeConfig(t, st.homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "route"}, {Login: "bob", Password: bobHash, Exit: "direct"}}
		cfg.Subscriptions = []config.Subscription{{Name: "route", URL: "http://route.invalid/sub"}}
	})
	accountsSaveSnapshot(t, st.homeA, "route", []subscription.Node{e2eExitDirectSocksNode("exit-direct-node", st.recorder.listener.Addr().String())})
	serverAngel := startUp(t, st.homeA, "--port", "0")
	st.serverAngel = serverAngel
	t.Cleanup(func() {
		serverAngel.Stop()
		assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), st.secrets...)
	})
	port := freePort(t)
	code, out, stderr := serverRun(t, st.homeA, "add", "--protocol", "vmess", "--name", "exit-direct-edge", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	if code != 0 {
		t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
	}
	waitExitDirectStatus(t, st.ctx, st.homeA, 1, st.secrets...)
}

// checkExitDirectServerTraffic checks that alice's mixed-port traffic uses A's
// route and bob's direct route does not.
func checkExitDirectServerTraffic(t *testing.T, st *exitDirectState) {
	t.Helper()
	before := st.recorder.count.Load()
	accountsClient(t, st.serverAngel.Status().Data.Listen, "alice", st.alicePass, st.backend)
	if st.recorder.count.Load() <= before {
		t.Fatal("Alice mixed-port traffic did not use A route recorder")
	}
	before = st.recorder.count.Load()
	accountsClient(t, st.serverAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() != before {
		t.Fatal("Bob mixed-port direct traffic reached A route recorder")
	}
}

// exitDirectLink returns the VMess URI that `server link` prints for account.
func exitDirectLink(t *testing.T, home, account string) string {
	t.Helper()
	code, stdout, stderr := serverRun(t, home, "link", "exit-direct-edge", "--account", account, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link: %s", serverErrorSummary(stdout, stderr))
	}
	var e struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(stdout), &e) != nil || e.Data.Link == "" {
		t.Fatal("server link omitted URI")
	}
	return e.Data.Link
}

// startExitDirectSubscriptions serves each account's link on the loopback and
// returns the listener address.
func startExitDirectSubscriptions(t *testing.T, links map[string]string) string {
	t.Helper()
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, links[account]) })
	}
	sub := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	subListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("subscription listener:", err)
	}
	subDone := make(chan struct{})
	go func() { defer close(subDone); _ = sub.Serve(subListener) }()
	t.Cleanup(func() {
		_ = sub.Close()
		select {
		case <-subDone:
		case <-time.After(e2eKillGrace):
			t.Error("subscription server did not stop")
		}
	})
	return subListener.Addr().String()
}

// setupExitDirectClient imports both links into B as subscriptions and starts
// B's angel: alice's provider routes to A, bob's provider is direct.
func setupExitDirectClient(t *testing.T, st *exitDirectState, subAddr string) {
	t.Helper()
	homeB := t.TempDir()
	for _, account := range []string{"alice", "bob"} {
		code, out, stderr := accountsRun(t.Context(), t, homeB, "sub", "add", "http://"+subAddr+"/"+account, "--name", account+"-edge")
		if code != 0 {
			t.Fatalf("import %s: %s", account, serverErrorSummary(out, stderr))
		}
	}
	// Keep the subscriptions `sub add` saved: writeConfig would start from the
	// default config and drop them, leaving the providers below unknown.
	cfgB := loadConfigFile(t, homeB)
	cfgB.Accounts = []config.Account{{Login: "alice", Password: st.aliceHash, Provider: "alice-edge"}, {Login: "bob", Password: st.bobHash, Provider: "bob-edge"}}
	if err := config.Save(config.ConfigPath(homeB), cfgB); err != nil {
		t.Fatalf("save client config: %v", err)
	}
	clientAngel := startUp(t, homeB, "--port", "0")
	st.clientAngel = clientAngel
	t.Cleanup(func() {
		clientAngel.Stop()
		assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), st.secrets...)
	})
}

// checkExitDirectClientTraffic checks that B's alice reaches A's route recorder
// and B's bob does not.
func checkExitDirectClientTraffic(t *testing.T, st *exitDirectState) {
	t.Helper()
	before := st.recorder.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, "alice", st.alicePass, st.backend)
	if st.recorder.count.Load() <= before {
		t.Fatal("Alice did not use A route recorder")
	}
	before = st.recorder.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() != before {
		t.Fatal("Bob direct traffic reached A's route recorder")
	}
}

// checkExitDirectRouteModes moves bob to route by exit, then by provider; the
// established tunnel must survive both reloads.
func checkExitDirectRouteModes(t *testing.T, st *exitDirectState, held net.Conn) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-exit", "bob", "route")
	if code != 0 {
		t.Fatalf("set-exit route: %s", serverErrorSummary(out, stderr))
	}
	waitExitDirectStatus(t, st.ctx, st.homeA, 2, st.secrets...)
	// No provider/country means route mode follows A's default DIRECT route.
	e2eExitDirectSendExpect(t, held, "open-after-default-route")
	before := st.recorder.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() != before {
		t.Fatal("empty route did not use default direct exit")
	}

	code, out, stderr = accountsRun(t.Context(), t, st.homeA, "account", "set-provider", "bob", "route")
	if code != 0 {
		t.Fatalf("set-provider route: %s", serverErrorSummary(out, stderr))
	}
	waitExitDirectStatus(t, st.ctx, st.homeA, 3, st.secrets...)
	e2eExitDirectSendExpect(t, held, "open-after-provider-route")
	before = st.recorder.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() <= before {
		t.Fatal("Bob routed mode missed A recorder")
	}
}

// checkExitDirectRejectedDirect checks that direct with a provider fails
// validation and leaves bob unchanged.
func checkExitDirectRejectedDirect(t *testing.T, st *exitDirectState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-exit", "bob", "direct")
	if code == 0 || decodeErrEnvelope(t, stderr).Error.Code != "CONFIG_INVALID" {
		t.Fatalf("direct with provider should fail: %s", serverErrorSummary(out, stderr))
	}
	cfg := loadConfigFile(t, st.homeA)
	bob, _ := cfg.Account("bob")
	if bob == nil || bob.Provider != "route" || bob.Exit != "route" {
		t.Fatal("failed direct validation mutated Bob")
	}
}

// checkExitDirectDirectMode clears bob's provider, then sets bob to direct; the
// established tunnel survives and only bob's route changes.
func checkExitDirectDirectMode(t *testing.T, st *exitDirectState, held net.Conn) {
	t.Helper()
	// Remove the redundant set-exit route: Bob is already route. Explicitly
	// clear the provider then apply direct; each effective update increments once.
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-provider", "bob", "--none")
	if code != 0 {
		t.Fatalf("clear Bob provider: %s", serverErrorSummary(out, stderr))
	}
	waitExitDirectStatus(t, st.ctx, st.homeA, 4, st.secrets...)
	code, out, stderr = accountsRun(t.Context(), t, st.homeA, "account", "set-exit", "bob", "direct")
	if code != 0 {
		t.Fatalf("set-exit direct: %s", serverErrorSummary(out, stderr))
	}
	waitExitDirectStatus(t, st.ctx, st.homeA, 5, st.secrets...)
	e2eExitDirectSendExpect(t, held, "open-after-direct")
	before := st.recorder.count.Load()
	accountsClient(t, st.clientAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() != before {
		t.Fatal("Bob direct mode still used A recorder")
	}
	before = st.recorder.count.Load()
	accountsClient(t, st.serverAngel.Status().Data.Listen, "alice", st.alicePass, st.backend)
	if st.recorder.count.Load() <= before {
		t.Fatal("Alice mixed-port route stopped using A recorder after Bob became direct")
	}
	before = st.recorder.count.Load()
	accountsClient(t, st.serverAngel.Status().Data.Listen, "bob", st.bobPass, st.backend)
	if st.recorder.count.Load() != before {
		t.Fatal("Bob mixed-port direct traffic used A recorder after direct reconfiguration")
	}
}

// checkExitDirectValidation stops A's server angel, adds the validation
// accounts and checks that invalid account commands fail with their codes.
func checkExitDirectValidation(t *testing.T, st *exitDirectState) {
	t.Helper()
	// CLI validation accounts are created only after connection-survival checks;
	// account additions restart the listener and intentionally end old sessions.
	st.serverAngel.Stop()
	for _, args := range [][]string{{"add", "exit-direct-provider", "--provider", "route"}, {"add", "exit-direct-country", "--country", "DE"}, {"add", "exit-direct-pre", "--pre-socks5", "socks5://127.0.0.1:1080"}} {
		code, out, stderr := accountsRun(t.Context(), t, st.homeA, append([]string{"account"}, args...)...)
		if code != 0 {
			t.Fatalf("setup validation account: %s", serverErrorSummary(out, stderr))
		}
	}
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "bob", "unknown"}, "CONFIG_INVALID")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "missing", "direct"}, "ACCOUNT_NOT_FOUND")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "bad/name", "direct"}, "ACCOUNT_INVALID")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "bob", "direct", "extra"}, "USAGE")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "--all", "direct", "extra"}, "USAGE")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "exit-direct-provider", "direct"}, "CONFIG_INVALID")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "exit-direct-country", "direct"}, "CONFIG_INVALID")
	checkExitDirectCLIError(t, st.homeA, []string{"set-exit", "exit-direct-pre", "direct"}, "CONFIG_INVALID")
}

// checkExitDirectCLIError checks that an account command fails with the error code want.
func checkExitDirectCLIError(t *testing.T, home string, args []string, want string) {
	t.Helper()
	code, stdout, stderr := accountsRun(t.Context(), t, home, append([]string{"account"}, args...)...)
	if code == 0 {
		t.Fatalf("account %v unexpectedly succeeded", args)
	}
	env := decodeErrEnvelope(t, stderr)
	if env.Error.Code != want {
		t.Fatalf("account %v code=%s want %s stdout=%s", args, env.Error.Code, want, stdout)
	}
}

// checkExitDirectPreWriteCountry checks that an unknown country fails before
// any write and leaves alice and bob unchanged.
func checkExitDirectPreWriteCountry(t *testing.T, st *exitDirectState) {
	t.Helper()
	code, out, stderr := accountsRun(t.Context(), t, st.homeA, "account", "set-country", "alice", "ZZ")
	if code == 0 || decodeErrEnvelope(t, stderr).Error.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("set-country alice ZZ should fail before write: %s", serverErrorSummary(out, stderr))
	}
	cfg := loadConfigFile(t, st.homeA)
	alice, _ := cfg.Account("alice")
	bob, _ := cfg.Account("bob")
	if alice == nil || alice.ExitCountry != "" || alice.Provider != "route" {
		t.Fatal("pre-write country validation mutated Alice")
	}
	if bob == nil || bob.Exit != "direct" || bob.Provider != "" {
		t.Fatal("failed CLI validation mutated Bob")
	}
}

func waitExitDirectStatus(t *testing.T, ctx context.Context, home string, reloadCount uint64, secrets ...string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastStatus string
	for {
		code, out, stderr := accountsRun(context.WithoutCancel(ctx), t, home, "status")
		if code == 0 {
			var env struct {
				Data struct {
					Reload *struct {
						Count     uint64 `json:"count"`
						LastError string `json:"last_error"`
					} `json:"reload"`
					Servers []struct {
						Name           string `json:"name"`
						Protocol       string `json:"protocol"`
						Listen         string `json:"listen"`
						Connections    int    `json:"connections"`
						DroppedClients uint64 `json:"dropped_clients"`
					} `json:"servers"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(out), &env); err == nil {
				lastStatus = exitDirectStatusSummary(env.Data.Reload, env.Data.Servers)
				serverMatches, statusErr := serverStatusMatches(out, "exit-direct-edge", "vmess", true)
				if env.Data.Reload != nil && env.Data.Reload.Count >= reloadCount && env.Data.Reload.LastError == "" && statusErr == nil && serverMatches {
					return
				}
			} else {
				lastStatus = "status JSON did not decode"
			}
		} else {
			lastStatus = fmt.Sprintf("status exit %d: %s", code, serverErrorSummary(out, stderr))
		}
		select {
		case <-ctx.Done():
			redactedStatus := redactExitDirectDiagnostic(lastStatus, secrets)
			redactedAngel := redactExitDirectDiagnostic(upProcReport(home), secrets)
			t.Fatalf("status did not reach reload count %d and server present=true (last status: %s)\n%s", reloadCount, redactedStatus, redactedAngel)
		case <-ticker.C:
		}
	}
}

func redactExitDirectDiagnostic(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	lines := strings.Split(text, "\n")
	safe := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "://") || strings.Contains(strings.ToLower(line), "password") || strings.Contains(strings.ToLower(line), "secret") {
			line = "[redacted diagnostic line]"
		}
		safe = append(safe, line)
	}
	return strings.Join(safe, "\n")
}

func exitDirectStatusSummary(reload *struct {
	Count     uint64 `json:"count"`
	LastError string `json:"last_error"`
}, servers []struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	Listen         string `json:"listen"`
	Connections    int    `json:"connections"`
	DroppedClients uint64 `json:"dropped_clients"`
}) string {
	if reload == nil {
		return fmt.Sprintf("reload: missing\nservers: %+v", servers)
	}
	return fmt.Sprintf("reload count: %d\nreload last_error: %s\nservers: %+v", reload.Count, reload.LastError, servers)
}

func e2eExitDirectSocksNode(id, addr string) subscription.Node {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	return subscription.Node{ID: id, Name: id, Type: "socks5", Server: host, Port: port, Raw: map[string]any{"name": id, "type": "socks5", "server": host, "port": port}}
}

func e2eExitDirectOpenVMess(t *testing.T, uri, target string) net.Conn {
	t.Helper()
	nodes, err := subscription.Parse([]byte(uri))
	if err != nil || len(nodes) != 1 {
		t.Fatal("parse Bob VMess link")
	}
	n := nodes[0]
	uuid, _ := n.Raw["uuid"].(string)
	cipher, _ := n.Raw["cipher"].(string)
	client, err := outbound.NewVmess(outbound.VmessOption{Name: "exit-direct-established", Server: n.Server, Port: n.Port, UUID: uuid, AlterID: 0, Cipher: cipher, Network: "tcp"})
	if err != nil {
		t.Fatal("construct VMess client:", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: e2eUint16(t, p)})
	if err != nil {
		t.Fatal("open VMess connection:", err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Minute)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn
}

func e2eExitDirectSendExpect(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write established tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read established tunnel: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("established echo=%q want %q", got, payload)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
}
