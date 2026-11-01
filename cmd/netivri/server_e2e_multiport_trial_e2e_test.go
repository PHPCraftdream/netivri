//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestE2EMultiportTrialLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const alicePassword, bobPassword = "e2eMultiportTrialAlicePassword", "e2eMultiportTrialBobPassword"
	backend := accountsEcho(t)
	aliceRecorder, bobRecorder := accountsServer(t), accountsServer(t)
	aliceHash, err := auth.Hash(alicePassword)
	if err != nil {
		t.Fatal("hash Alice")
	}
	bobHash, err := auth.Hash(bobPassword)
	if err != nil {
		t.Fatal("hash Bob")
	}
	homeA := t.TempDir()
	writeConfig(t, homeA, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: aliceHash, Provider: "trial-alice-route"}, {Login: "bob", Password: bobHash, Provider: "trial-bob-route"}}
		cfg.Subscriptions = []config.Subscription{{Name: "trial-alice-route", URL: "http://alice.invalid/sub"}, {Name: "trial-bob-route", URL: "http://bob.invalid/sub"}}
	})
	accountsSaveSnapshot(t, homeA, "trial-alice-route", []subscription.Node{accountsSocksNode("trial-alice-node", "", aliceRecorder)})
	accountsSaveSnapshot(t, homeA, "trial-bob-route", []subscription.Node{accountsSocksNode("trial-bob-node", "", bobRecorder)})
	serverA := startUp(t, homeA, "--port", "0")
	defer serverA.Stop()
	port := freePort(t)
	add := func(args ...string) {
		t.Helper()
		code, out, stderr := serverRun(t, homeA, args...)
		if code != 0 {
			t.Fatalf("server add: %s", serverErrorSummary(out, stderr))
		}
	}
	add("add", "--protocol", "shadowsocks", "--name", "e2eMultiportTrialSS", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "cipher=2022-blake3-aes-128-gcm")
	add("add", "--protocol", "vmess", "--name", "e2eMultiportTrialVMess", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1")
	add("add", "--protocol", "trojan", "--name", "e2eMultiportTrialTLS", "--account", "alice,bob", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--param", "sni=trial-trojan.local")
	links := map[string]string{"ss-alice": e2eMultiportTrialLink(t, homeA, "e2eMultiportTrialSS", "alice"), "vmess-alice": e2eMultiportTrialLink(t, homeA, "e2eMultiportTrialVMess", "alice"), "vmess-bob": e2eMultiportTrialLink(t, homeA, "e2eMultiportTrialVMess", "bob"), "trojan-alice": e2eMultiportTrialLink(t, homeA, "e2eMultiportTrialTLS", "alice"), "trojan-bob": e2eMultiportTrialLink(t, homeA, "e2eMultiportTrialTLS", "bob")}
	feeds := map[string]*e2eMultiportTrialFeed{"alice": {links: []string{links["ss-alice"], links["vmess-alice"], links["trojan-alice"]}}, "bob": {links: []string{links["vmess-bob"], links["trojan-bob"]}}}
	feedURL := map[string]string{"alice": feeds["alice"].start(t), "bob": feeds["bob"].start(t)}
	homeB := t.TempDir()
	for _, login := range []string{"alice", "bob"} {
		code, out, stderr := accountsRun(t, homeB, "sub", "add", feedURL[login], "--name", "trial-"+login)
		if code != 0 {
			t.Fatalf("B subscription: %s", serverErrorSummary(out, stderr))
		}
		code, out, stderr = accountsRun(t, homeB, "account", "add", login, "--provider", "trial-"+login)
		if code != 0 {
			t.Fatalf("B account: %s", serverErrorSummary(out, stderr))
		}
	}
	clientB := startUp(t, homeB, "--port", "0")
	defer func() { clientB.Stop() }()
	drive := func(login, password string, recorder, other *accountsUpstream, retry bool) {
		t.Helper()
		before, otherBefore := recorder.count.Load(), other.count.Load()
		deadline := time.Now().Add(30 * time.Second)
		var last error
		for {
			err := e2eMultiportTrialAccountEcho(clientB.Status().Data.Listen, login, password, backend)
			if err == nil && recorder.count.Load() > before && other.count.Load() == otherBefore {
				return
			}
			if err != nil {
				last = err
			} else {
				last = fmt.Errorf("recorder routing mismatch")
			}
			if !retry || !time.Now().Before(deadline) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(150 * time.Millisecond):
			}
			if ctx.Err() != nil {
				break
			}
		}
		t.Fatalf("account %s failed: %v", login, last)
	}
	for _, item := range []struct {
		login, password string
		own, other      *accountsUpstream
	}{{"alice", alicePassword, aliceRecorder, bobRecorder}, {"bob", bobPassword, bobRecorder, aliceRecorder}} {
		drive(item.login, item.password, item.own, item.other, true)
	}
	for _, entry := range []struct {
		linkKey, login, password string
		own, other               *accountsUpstream
	}{{"ss-alice", "alice", alicePassword, aliceRecorder, bobRecorder}, {"vmess-alice", "alice", alicePassword, aliceRecorder, bobRecorder}, {"vmess-bob", "bob", bobPassword, bobRecorder, aliceRecorder}, {"trojan-alice", "alice", alicePassword, aliceRecorder, bobRecorder}, {"trojan-bob", "bob", bobPassword, bobRecorder, aliceRecorder}} {
		if err := e2eMultiportTrialForceBProtocol(t, ctx, homeB, entry.linkKey, links[entry.linkKey], entry.login, entry.password, backend, entry.own, entry.other); err != nil {
			t.Fatalf("B forced %s: %v", entry.linkKey, err)
		}
	}
	code, out, stderr := serverRun(t, homeA, "add", "--protocol", "vmess", "--name", "e2eMultiportTrialAmbiguous", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--public-host", "::ffff:127.0.0.1", "--default")
	if code != 6 || decodeErrEnvelope(t, stderr).Error.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("ambiguous member: %s", serverErrorSummary(out, stderr))
	}
	baselineReload := liveStatusOf(t, homeA).Data.Reload.Count
	if code, out, stderr = serverRun(t, homeA, "remove", "e2eMultiportTrialSS"); code != 0 {
		t.Fatalf("remove SS: %s", serverErrorSummary(out, stderr))
	}
	waitStatusReload(t, ctx, homeA, baselineReload+1)
	if code, out, stderr = accountsRun(t, homeB, "down"); code != 0 {
		t.Fatalf("B down: %s", serverErrorSummary(out, stderr))
	}
	clientB.Stop()
	clientB = startUp(t, homeB, "--port", "0")
	drive("bob", bobPassword, bobRecorder, aliceRecorder, true)
	beforeA, beforeB := aliceRecorder.count.Load(), bobRecorder.count.Load()
	err = e2eMultiportTrialDirectSSRefusal(ctx, links["ss-alice"], backend)
	if err == nil {
		t.Fatal("removed SS link succeeded")
	}
	if aliceRecorder.count.Load() != beforeA || bobRecorder.count.Load() != beforeB {
		t.Fatal("removed SS probe reached recorder")
	}
}

type e2eMultiportTrialFeed struct{ links []string }

func (f *e2eMultiportTrialFeed) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		for _, link := range f.links {
			_, _ = io.WriteString(w, link+"\n")
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("feed listen")
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(e2eKillGrace):
			t.Error("feed did not stop")
		}
	})
	return "http://" + ln.Addr().String() + "/"
}
func e2eMultiportTrialLink(t *testing.T, home, name, login string) string {
	t.Helper()
	code, out, stderr := serverRun(t, home, "link", name, "--account", login, "--format", "uri")
	if code != 0 {
		t.Fatalf("server link: %s", serverErrorSummary(out, stderr))
	}
	var env struct {
		Data struct {
			Link string `json:"link"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Data.Link == "" {
		t.Fatal("link response missing URI")
	}
	return env.Data.Link
}
func e2eMultiportTrialForceBProtocol(t *testing.T, parent context.Context, home, key, link, login, password, backend string, own, other *accountsUpstream) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	feed := &e2eMultiportTrialFeed{links: []string{link}}
	feedURL := feed.start(t)
	forcedHome := t.TempDir()
	code, out, stderr := accountsRun(t, forcedHome, "sub", "add", feedURL, "--name", "forced-"+key)
	if code != 0 {
		return fmt.Errorf("add forced subscription: %s", serverErrorSummary(out, stderr))
	}
	code, out, stderr = accountsRun(t, forcedHome, "account", "add", "forced", "--provider", "forced-"+key)
	if code != 0 {
		return fmt.Errorf("add forced account: %s", serverErrorSummary(out, stderr))
	}
	client := startUp(t, forcedHome, "--port", "0")
	defer client.Stop()
	before, otherBefore := own.count.Load(), other.count.Load()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		err := e2eMultiportTrialAccountEcho(client.Status().Data.Listen, "forced", password, backend)
		if err == nil && own.count.Load() > before && other.count.Load() == otherBefore {
			return nil
		}
		if err != nil {
			last = err
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return fmt.Errorf("forced %s route mismatch: %v", key, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}
func e2eMultiportTrialAccountEcho(listen, login, password, backend string) error {
	host, portText, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("backend is not IPv4")
	}
	var port uint16
	if _, err = fmt.Sscanf(portText, "%d", &port); err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", listen, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(7 * time.Second))
	if _, err = conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	var method [2]byte
	if _, err = io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 2} {
		return fmt.Errorf("SOCKS method refusal: %v", err)
	}
	authReq := append([]byte{1, byte(len(login))}, []byte(login)...)
	authReq = append(authReq, byte(len(password)))
	authReq = append(authReq, []byte(password)...)
	if _, err = conn.Write(authReq); err != nil {
		return err
	}
	var authReply [2]byte
	if _, err = io.ReadFull(conn, authReply[:]); err != nil || authReply[1] != 0 {
		return fmt.Errorf("SOCKS auth refusal: %v", err)
	}
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err = conn.Write(req); err != nil {
		return err
	}
	var reply [10]byte
	if _, err = io.ReadFull(conn, reply[:]); err != nil || reply[1] != 0 {
		return fmt.Errorf("SOCKS CONNECT refusal: %v", err)
	}
	payload := []byte("e2eMultiportTrialAccountEcho")
	if _, err = conn.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != string(payload) {
		return fmt.Errorf("echo mismatch")
	}
	return nil
}

func e2eMultiportTrialDirectSSRefusal(parent context.Context, link, backend string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	nodes, err := subscription.Parse([]byte(link))
	if err != nil || len(nodes) != 1 {
		return fmt.Errorf("parse SS link")
	}
	node := nodes[0]
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "e2eMultiportTrialRefusal", Server: node.Server, Port: node.Port, Password: node.Raw["password"].(string), Cipher: node.Raw["cipher"].(string)})
	if err != nil {
		return err
	}
	defer client.Close()
	host, text, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	p, _ := strconv.Atoi(text)
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(p)})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	_, err = conn.Write([]byte("refused-probe"))
	if err != nil {
		return err
	}
	b := make([]byte, 32)
	n, err := conn.Read(b)
	if n > 0 {
		return fmt.Errorf("removed endpoint unexpectedly returned payload")
	}
	return err
}
