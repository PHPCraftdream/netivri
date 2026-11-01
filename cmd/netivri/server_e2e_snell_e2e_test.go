//go:build e2e

package main

import (
	"context"
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

func TestE2ESnellLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const pass = "snell-e2e-account-password"
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	hash, err := auth.Hash(pass)
	if err != nil {
		t.Fatal("hash account password")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "alice-route"}, {Login: "bob", Password: hash, Provider: "bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "alice-route", URL: "http://alice.invalid/sub"}, {Name: "bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-node", "", bobUp)})
	serverProc := startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverProc.Status())
	secretCheck := []string{pass, hash}
	t.Cleanup(func() {
		serverProc.Stop()
		assertNoSecrets(t, serverProc.Stdout(), serverProc.Stderr(), secretCheck...)
	})
	ports := []int{freePort(t)}
	for i, account := range []string{"alice", "bob"} {
		name := "snell-" + account
		code, out, stderr := serverRun(t, homeA, "add", "--protocol", "snell", "--name", name, "--account", account, "--port", strconv.Itoa(ports[i]), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "version=2", "--param", "obfs=none")
		if code != 0 {
			t.Fatalf("Snell server add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secretCheck...)
		waitStatusReload(t, ctx, homeA, uint64(i+1))
		if i == 0 {
			ports = append(ports, freePort(t))
		}
	}
	for _, name := range []string{"snell-alice", "snell-bob"} {
		e2eSnellWaitStatus(t, ctx, homeA, name, true)
	}
	getLink := func(name, account string) string {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, "link", name, "--account", account, "--format", "mihomo")
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
		assertNoSecrets(t, stderr, stderr, secretCheck...)
		return env.Data.Link
	}
	aliceFeed := &e2eSnellFeed{body: getLink("snell-alice", "alice")}
	bobFeed := &e2eSnellFeed{body: getLink("snell-bob", "bob")}
	secretCheck = append(secretCheck, e2eSnellPSK(t, aliceFeed.get()), e2eSnellPSK(t, bobFeed.get()))
	aliceURL, bobURL := aliceFeed.start(t), bobFeed.start(t)
	homeB := t.TempDir()
	for _, item := range []struct{ name, url string }{{"alice-edge", aliceURL}, {"bob-edge", bobURL}} {
		code, out, stderr := accountsRun(t, homeB, "sub", "add", item.url, "--name", item.name)
		if code != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secretCheck...)
	}
	for _, item := range []struct{ account, provider string }{{"alice", "alice-edge"}, {"bob", "bob-edge"}} {
		code, out, stderr := accountsRun(t, homeB, "account", "add", item.account, "--provider", item.provider)
		if code != 0 {
			t.Fatalf("client account add failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secretCheck...)
	}
	clientProc := startUp(t, homeB, "--port", "0")
	assertStatusSuccess(t, clientProc.Status())
	t.Cleanup(func() {
		clientProc.Stop()
		assertNoSecrets(t, clientProc.Stdout(), clientProc.Stderr(), secretCheck...)
	})
	diagnostic := func() string { return e2eSnellDiagnostics(t, homeA, homeB, secretCheck...) }
	driveAccount := func(account string, retry bool) error {
		beforeA, beforeB := aliceUp.count.Load(), bobUp.count.Load()
		deadline := time.Now().Add(30 * time.Second)
		if !retry {
			deadline = time.Now()
		}
		for {
			err := e2eSnellAccountsEcho(clientProc.Status().Data.Listen, account, pass, backend)
			if err == nil {
				if account == "alice" && (aliceUp.count.Load() <= beforeA || bobUp.count.Load() != beforeB) {
					return errors.New("Alice recorder routing mismatch")
				}
				if account == "bob" && (bobUp.count.Load() <= beforeB || aliceUp.count.Load() != beforeA) {
					return errors.New("Bob recorder routing mismatch")
				}
				return nil
			}
			if !retry || time.Now().After(deadline) {
				return errors.New("account echo failed")
			}
			select {
			case <-ctx.Done():
				return errors.New("account echo deadline expired")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	drive := func(retry bool) error {
		if err := driveAccount("alice", retry); err != nil {
			return err
		}
		return driveAccount("bob", retry)
	}
	if err := drive(false); err != nil {
		t.Fatalf("initial drive failed: %v\n%s", err, diagnostic())
	}
	aliceLink := getLink("snell-alice", "alice")
	if err := e2eSnellProbe(ctx, aliceLink, backend, ""); err != nil {
		t.Fatalf("valid Snell probe failed\n%s", diagnostic())
	}
	if err := e2eSnellProbe(ctx, aliceLink, backend, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !e2eSnellAuthRejected(err) {
		t.Fatalf("wrong PSK rejection failed\n%s", diagnostic())
	}
	code, out, stderr := serverRun(t, homeA, "rotate", "snell-alice", "--account", "alice")
	if code != 0 {
		t.Fatalf("rotation failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 3)
	if err := e2eSnellProbe(ctx, aliceLink, backend, ""); !e2eSnellAuthRejected(err) {
		t.Fatalf("old PSK rejection failed\n%s", diagnostic())
	}
	rotatedLink := getLink("snell-alice", "alice")
	rotatedPSK := e2eSnellPSK(t, rotatedLink)
	secretCheck = append(secretCheck, rotatedPSK)
	aliceFeed.set(rotatedLink)
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-edge")
	if code != 0 {
		t.Fatalf("subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeB, 1)
	if err := drive(false); err != nil {
		t.Fatalf("post-rotation drive failed: %v\n%s", err, diagnostic())
	}
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("client restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 4)
	if err := e2eSnellProbe(ctx, rotatedLink, backend, ""); !e2eSnellAuthRejected(err) {
		t.Fatalf("restricted Alice probe failed\n%s", diagnostic())
	}
	e2eSnellRestartClient(t, homeB, &clientProc, secretCheck...)
	if err := driveAccount("bob", true); err != nil {
		t.Fatalf("restricted Bob echo failed: %v\n%s", err, diagnostic())
	}
	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear restriction failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	waitStatusReload(t, ctx, homeA, 5)
	e2eSnellRestartClient(t, homeB, &clientProc, secretCheck...)
	if err := driveAccount("alice", true); err != nil {
		t.Fatalf("post-clear Alice echo failed: %v\n%s", err, diagnostic())
	}
	if err := driveAccount("bob", true); err != nil {
		t.Fatalf("post-clear Bob echo failed: %v\n%s", err, diagnostic())
	}
	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("server down failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, secretCheck...)
	oldServer := serverProc
	oldServer.Stop()
	assertNoSecrets(t, oldServer.Stdout(), oldServer.Stderr(), secretCheck...)
	serverProc = startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverProc.Status())
	e2eSnellRestartClient(t, homeB, &clientProc, secretCheck...)
	if got := getLink("snell-alice", "alice"); got != rotatedLink {
		t.Fatalf("link persistence failed\n%s", diagnostic())
	}
	if err := driveAccount("alice", true); err != nil {
		t.Fatalf("post-A-restart Alice echo failed: %v\n%s", err, diagnostic())
	}
	if err := driveAccount("bob", true); err != nil {
		t.Fatalf("post-A-restart Bob echo failed: %v\n%s", err, diagnostic())
	}
	for i, name := range []string{"snell-alice", "snell-bob"} {
		code, out, stderr = serverRun(t, homeA, "remove", name)
		if code != 0 {
			t.Fatalf("remove failed: %s", serverErrorSummary(out, stderr))
		}
		assertNoSecrets(t, out, stderr, secretCheck...)
		waitStatusReload(t, ctx, homeA, uint64(i+1))
	}
	for _, name := range []string{"snell-alice", "snell-bob"} {
		e2eSnellWaitStatus(t, ctx, homeA, name, false)
	}
	for _, port := range ports {
		if err := waitServerPort(ctx, port, false); err != nil {
			t.Fatalf("removed port check failed\n%s", diagnostic())
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
	client, err := outbound.NewSnell(outbound.SnellOption{Name: "e2e-snell-probe", Server: n.Server, Port: n.Port, Psk: psk, Version: version, UDP: false})
	if err != nil {
		return errors.New("construct Snell probe")
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(n.Server, strconv.Itoa(n.Port)))
	if err != nil {
		return err
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(4 * time.Second))
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("invalid echo target IP")
	}
	conn, err := client.StreamConnContext(ctx, raw, &C.Metadata{NetWork: C.TCP, DstIP: ip, DstPort: uint16(dp)})
	if err != nil {
		return err
	}
	defer conn.Close()
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
func e2eSnellAccountsEcho(proxy, user, pass, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return errors.New("SOCKS connect failed")
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = c.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 2} {
		return errors.New("SOCKS method negotiation failed")
	}
	credentials := append([]byte{1, byte(len(user))}, []byte(user)...)
	credentials = append(credentials, byte(len(pass)))
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
	if err != nil || ip == nil || port < 1 || port > 65535 {
		return errors.New("invalid echo destination")
	}
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err = c.Write(req); err != nil {
		return err
	}
	var hdr [4]byte
	if _, err = io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 || hdr[1] != 0 {
		return errors.New("SOCKS CONNECT failed")
	}
	length := 0
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
	code, out, stderr := accountsRun(t, homeA, "status")
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
			var rows []string
			for _, s := range env.Data.Servers {
				rows = append(rows, fmt.Sprintf("%s %s %s connections=%d dropped=%d", s.Name, s.Protocol, s.Listen, s.Connections, s.Dropped))
			}
			status = strings.Join(rows, "; ")
		}
	}
	logs := serverE2ELogTail(t, homeA)
	codeB, outB, stderrB := accountsRun(t, homeB, "status", "--full")
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
			var rows []string
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
		code, out, stderr := accountsRun(t, home, "status")
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
			t.Fatalf("Snell status did not converge for %s", name)
		case <-tick.C:
		}
	}
}
func e2eSnellRestartClient(t *testing.T, home string, p **upProc, secrets ...string) {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "down")
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
