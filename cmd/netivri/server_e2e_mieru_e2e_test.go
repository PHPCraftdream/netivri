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
	"regexp"
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
	"go.yaml.in/yaml/v3"
)

func TestE2EMieruLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const alicePass = "mieru-e2e-alice-account-pass"
	const bobPass = "mieru-e2e-bob-account-pass"
	backend := accountsEcho(t)
	aliceUp, bobUp := accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePass)
	if err != nil {
		t.Fatal("hash Alice account password")
	}
	bobHash, err := auth.Hash(bobPass)
	if err != nil {
		t.Fatal("hash Bob account password")
	}

	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{
			{Login: "alice", Password: aliceHash, Provider: "alice-route"},
			{Login: "bob", Password: bobHash, Provider: "bob-route"},
		}
		cfg.Subscriptions = []config.Subscription{
			{Name: "alice-route", URL: "http://alice.invalid/sub"},
			{Name: "bob-route", URL: "http://bob.invalid/sub"},
		}
	})
	accountsSaveSnapshot(t, homeA, "alice-route", []subscription.Node{accountsSocksNode("alice-route-node", "", aliceUp)})
	accountsSaveSnapshot(t, homeA, "bob-route", []subscription.Node{accountsSocksNode("bob-route-node", "", bobUp)})
	allSecrets := []string{alicePass, bobPass, aliceHash, bobHash}
	serverAngel := startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverAngel.Status())
	originalServerAngel := serverAngel
	serverPort := freePort(t)
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "mieru", "--name", "edge", "--account", "alice,bob", "--port", strconv.Itoa(serverPort), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "transport=TCP", "--param", "multiplexing=MULTIPLEXING_OFF")
	if code != 0 {
		t.Fatalf("Mieru server add failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 1)
	e2eMieruWaitStatus(t, ctx, homeA, true, allSecrets...)

	getLink := func(account, format string) string {
		t.Helper()
		c, stdout, errout := serverRun(t, homeA, "link", "edge", "--account", account, "--format", format)
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
	links := map[string]string{"alice": getLink("alice", "mihomo"), "bob": getLink("bob", "mihomo")}
	allSecrets = append(allSecrets, e2eMieruExtractPassword(t, links["alice"]), e2eMieruExtractPassword(t, links["bob"]))

	var linkMu sync.RWMutex
	mux := http.NewServeMux()
	for _, account := range []string{"alice", "bob"} {
		account := account
		mux.HandleFunc("/"+account, func(w http.ResponseWriter, _ *http.Request) {
			linkMu.RLock()
			defer linkMu.RUnlock()
			_, _ = io.WriteString(w, links[account])
		})
	}
	sub := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	subListener, err := net.Listen("tcp", "127.0.0.1:0")
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

	homeB := t.TempDir()
	base := "http://" + subListener.Addr().String()
	for _, account := range []string{"alice", "bob"} {
		provider := account + "-provider"
		c, stdout, errout := accountsRun(t, homeB, "sub", "add", base+"/"+account, "--name", provider)
		if c != 0 {
			t.Fatalf("subscription add failed: %s", serverErrorSummary(stdout, errout))
		}
		c, stdout, errout = accountsRun(t, homeB, "account", "add", account, "--provider", provider)
		if c != 0 {
			t.Fatalf("account add failed: %s", serverErrorSummary(stdout, errout))
		}
	}
	clientAngel := startUp(t, homeB, "--port", "0")
	assertStatusSuccess(t, clientAngel.Status())
	restartClient := func() {
		code, stdout, stderr := accountsRun(t, homeB, "down")
		if code != 0 {
			t.Fatalf("client angel down failed: %s", serverErrorSummary(stdout, stderr))
		}
		clientAngel.Stop()
		clientAngel = startUp(t, homeB, "--port", "0")
		assertStatusSuccess(t, clientAngel.Status())
	}
	drive := func(account, pass string, own, other *accountsUpstream, retry bool) {
		t.Helper()
		probeCtx := ctx
		cancelRetry := func() {}
		if retry {
			probeCtx, cancelRetry = context.WithTimeout(ctx, 30*time.Second)
		}
		defer cancelRetry()
		startOwn, startOther := own.count.Load(), other.count.Load()
		attempts := 1
		if retry {
			attempts = 30
		}
		var last error
		for i := 0; i < attempts; i++ {
			last = e2eMieruSOCKSEcho(probeCtx, clientAngel.Status().Data.Listen, account, pass, backend, "mieru-e2e-echo")
			if last == nil && own.count.Load() > startOwn && other.count.Load() == startOther {
				return
			}
			if !retry {
				break
			}
			if last != nil {
				t.Logf("Mieru echo attempt failed: %s", e2eMieruRedact(last.Error(), allSecrets))
			} else {
				t.Log("Mieru echo attempt failed: recorder counts not updated")
			}
			select {
			case <-probeCtx.Done():
				i = attempts
			case <-time.After(time.Second):
			}
		}
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		if last != nil {
			t.Fatalf("%s client-core echo failed: %s", account, e2eMieruRedact(fmt.Sprint(last), allSecrets))
		}
		t.Fatalf("%s recorder mismatch (own %d->%d, other %d->%d)", account, startOwn, own.count.Load(), startOther, other.count.Load())
	}

	if err := e2eMieruProbe(ctx, links["alice"], backend, "valid-mieru-credential", false); err != nil {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("correct server credential probe failed: %s", e2eMieruRedact(err.Error(), allSecrets))
	}
	if err := e2eMieruProbe(ctx, links["alice"], backend, "wrong-password", true); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("wrong server credential not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), allSecrets))
	}
	drive("alice", alicePass, aliceUp, bobUp, false)
	drive("bob", bobPass, bobUp, aliceUp, false)

	oldAlice := links["alice"]
	if err := e2eMieruProbe(ctx, oldAlice, backend, "pre-rotation", false); err != nil {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("correct Alice credential probe before rotation failed: %s", e2eMieruRedact(err.Error(), allSecrets))
	}
	code, out, stderr = serverRun(t, homeA, "rotate", "edge", "--account", "alice")
	if code != 0 {
		t.Fatalf("server rotation failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, 2)
	if err := e2eMieruProbe(ctx, oldAlice, backend, "old-password", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("old Alice credential not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), allSecrets))
	}
	newAlice := getLink("alice", "mihomo")
	if newAlice == oldAlice {
		t.Fatal("Alice refreshed link did not change after rotation")
	}
	allSecrets = append(allSecrets, e2eMieruExtractPassword(t, newAlice))
	linkMu.Lock()
	links["alice"] = newAlice
	linkMu.Unlock()
	code, out, stderr = accountsRun(t, homeB, "sub", "update", "alice-provider")
	if code != 0 {
		t.Fatalf("Alice subscription update failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeB, 1)
	drive("alice", alicePass, aliceUp, bobUp, false)

	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "192.0.2.0/24")
	if code != 0 {
		t.Fatalf("restrict Alice clients failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 3)
	// Restart to reset dead-node probe backoff after a changed client filter or intentional probe rejection.
	beforeAlice, beforeBob := aliceUp.count.Load(), bobUp.count.Load()
	if err := e2eMieruProbe(ctx, newAlice, backend, "filtered-client", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("filtered Alice direct probe was not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), allSecrets))
	}
	e2eMieruWaitDropped(t, ctx, homeA, 1, allSecrets...)
	if aliceUp.count.Load() != beforeAlice || bobUp.count.Load() != beforeBob {
		t.Fatal("filtered Alice request reached a recorder")
	}
	restartClient()
	drive("bob", bobPass, bobUp, aliceUp, true)

	code, out, stderr = accountsRun(t, homeA, "account", "set-clients", "alice", "--none")
	if code != 0 {
		t.Fatalf("clear Alice restriction failed: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, 4)
	restartClient()
	drive("alice", alicePass, aliceUp, bobUp, true)

	code, out, stderr = accountsRun(t, homeA, "down")
	if code != 0 {
		t.Fatalf("angel down failed: %s", serverErrorSummary(out, stderr))
	}
	serverAngel.Stop()
	if err := waitServerPort(ctx, serverPort, false); err != nil {
		t.Fatalf("Mieru port remained open after down: %v", err)
	}
	assertNoSecrets(t, originalServerAngel.Stdout(), originalServerAngel.Stderr(), allSecrets...)
	serverAngel = startUp(t, homeA, "--port", "0")
	assertStatusSuccess(t, serverAngel.Status())
	restartClient()
	if persisted := getLink("alice", "mihomo"); persisted != newAlice {
		t.Fatal("Alice link changed across server restart")
	}
	if persisted := getLink("bob", "mihomo"); persisted != links["bob"] {
		t.Fatal("Bob link changed across server restart")
	}
	drive("alice", alicePass, aliceUp, bobUp, true)
	drive("bob", bobPass, bobUp, aliceUp, true)
	if err := e2eMieruProbe(ctx, newAlice, backend, "post-restart-valid", false); err != nil {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("correct Alice credential probe after restart failed: %s", e2eMieruRedact(err.Error(), allSecrets))
	}
	if err := e2eMieruProbe(ctx, oldAlice, backend, "old-password", false); !e2eMieruRejectedError(err) {
		e2eMieruDiagnostics(t, homeA, homeB, allSecrets)
		t.Fatalf("old Alice credential after rotation not explicitly rejected: %s", e2eMieruRedact(fmt.Sprint(err), allSecrets))
	}
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), allSecrets...)
	baseline := e2eMieruWaitReload(t, ctx, homeA, allSecrets...)
	code, out, stderr = serverRun(t, homeA, "remove", "edge")
	if code != 0 {
		t.Fatalf("server remove failed: %s", serverErrorSummary(out, stderr))
	}
	assertNoSecrets(t, out, stderr, allSecrets...)
	waitStatusReload(t, ctx, homeA, baseline+1)
	e2eMieruWaitStatus(t, ctx, homeA, false, allSecrets...)
	if err := waitServerPort(ctx, serverPort, false); err != nil {
		t.Fatalf("removed Mieru port remains open: %v", err)
	}
	serverAngel.Stop()
	clientAngel.Stop()
	assertNoSecrets(t, serverAngel.Stdout(), serverAngel.Stderr(), allSecrets...)
	assertNoSecrets(t, clientAngel.Stdout(), clientAngel.Stderr(), allSecrets...)
}

func e2eMieruProbe(ctx context.Context, body, target, payload string, wrongPassword bool) error {
	parsed, err := subscription.Parse([]byte(body))
	if err != nil || len(parsed) != 1 {
		return fmt.Errorf("Mieru link import failed")
	}
	node := parsed[0]
	username, _ := node.Raw["username"].(string)
	password, _ := node.Raw["password"].(string)
	transport, _ := node.Raw["transport"].(string)
	multiplexing, _ := node.Raw["multiplexing"].(string)
	if node.Server == "" || node.Port < 1 || username == "" || password == "" || transport != "TCP" {
		return fmt.Errorf("incomplete Mieru configuration")
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
	defer client.Close()
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("invalid echo endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("invalid echo port")
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
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	if _, err = io.WriteString(conn, payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return fmt.Errorf("unexpected Mieru echo")
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
	defer conn.Close()
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
		return fmt.Errorf("unexpected SOCKS auth method")
	}
	if len(login) > 255 || len(password) > 255 {
		return fmt.Errorf("SOCKS credentials too long")
	}
	auth := []byte{1, byte(len(login))}
	auth = append(auth, login...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	if err = write(auth); err != nil {
		return err
	}
	reply, err = read(2)
	if err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("client account authentication refused")
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("invalid echo endpoint")
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.Atoi(portText)
	if err != nil || ip == nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid echo target")
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, ip...)
	request = append(request, byte(port>>8), byte(port))
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
		return fmt.Errorf("unexpected routed echo")
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
		code, out, stderr := accountsRun(t, home, "status")
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
			serverE2EFailureDiagnostics(t, home, "Mieru status wait expired")
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
		code, out, stderr := accountsRun(t, home, "status")
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
			serverE2EFailureDiagnostics(t, home, "Mieru reload status wait expired")
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
		code, out, stderr := accountsRun(t, home, "status")
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
			serverE2EFailureDiagnostics(t, home, "Mieru dropped status wait expired")
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

func e2eMieruDiagnostics(t *testing.T, homeA, homeB string, secrets []string) {
	t.Helper()
	for _, home := range []struct{ label, path string }{{"A", homeA}, {"B", homeB}} {
		for _, args := range [][]string{{"status", "--full"}, {"logs", "--since", "2m", "--tail", "50"}, {"node", "list"}} {
			code, out, stderr := accountsRun(t, home.path, args...)
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
