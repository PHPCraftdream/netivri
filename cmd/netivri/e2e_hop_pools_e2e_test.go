//go:build e2e

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

const e2ePoolTargetHost = "pool-route.test"

type e2ePostPoolFake struct {
	t           *testing.T
	listener    net.Listener
	backend     string
	target      string
	live        atomic.Bool
	ready       chan struct{}
	readyOne    sync.Once
	acceptReady chan struct{}

	mu         sync.Mutex
	closing    bool
	conns      map[net.Conn]struct{}
	targets    []string
	forwarded  int
	acceptDone chan struct{}
	wg         sync.WaitGroup
	closeOne   sync.Once
}

func newE2EPostPoolFake(t *testing.T, backend, target string, live bool) *e2ePostPoolFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("post-hop fake listen: %v", err)
	}
	s := &e2ePostPoolFake{
		t: t, listener: ln, backend: backend, target: target,
		ready: make(chan struct{}), acceptReady: make(chan struct{}),
		conns: make(map[net.Conn]struct{}), acceptDone: make(chan struct{}),
	}
	s.live.Store(live)
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-s.acceptReady:
	case <-ctx.Done():
		t.Fatal("post-hop fake accept loop did not become ready")
	}
	return s
}

func (s *e2ePostPoolFake) address() string { return s.listener.Addr().String() }

func (s *e2ePostPoolFake) accept() {
	defer s.wg.Done()
	defer close(s.acceptDone)
	close(s.acceptReady)
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = c.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *e2ePostPoolFake) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close()
	}()
	_ = c.SetDeadline(time.Now().Add(e2eIOTimeout))
	// A closed-mode fake accepts no SOCKS negotiation and closes immediately,
	// producing a real hop-connect failure while keeping the reserved address.
	if !s.live.Load() {
		return
	}
	if err := e2eSOCKS5Negotiate(c); err != nil {
		return
	}
	s.readyOne.Do(func() { close(s.ready) })
	target, err := readE2ESOCKS5Target(c)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()
	if !s.live.Load() {
		_, _ = c.Write(e2eSOCKS5Reply(5))
		return
	}
	if !strings.EqualFold(target, s.target) {
		_, _ = c.Write(e2eSOCKS5Reply(2))
		return
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil || !strings.HasSuffix(strings.ToLower(host), ".test") {
		_, _ = c.Write(e2eSOCKS5Reply(2))
		return
	}
	// The .test target is fixed by this test and mapped directly to the loopback
	// echo endpoint; the fake performs no DNS or external network access.
	out, err := net.DialTimeout("tcp", s.backend, 3*time.Second)
	if err != nil {
		_, _ = c.Write(e2eSOCKS5Reply(5))
		return
	}
	defer out.Close()
	_ = out.SetDeadline(time.Now().Add(e2eIOTimeout))
	if _, err := c.Write(e2eSOCKS5Reply(0)); err != nil {
		return
	}
	s.mu.Lock()
	s.forwarded++
	s.mu.Unlock()
	e2eCopyConn(c, out)
}

func e2eSOCKS5ClientGreeting(c net.Conn) error {
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 0} {
		return fmt.Errorf("SOCKS5 no-auth greeting returned %v", reply)
	}
	return nil
}

func e2eSOCKS5Negotiate(c net.Conn) error {
	var greeting [2]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting[0] != 5 {
		return fmt.Errorf("invalid SOCKS5 greeting: %v", err)
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0 {
			_, err := c.Write([]byte{5, 0})
			return err
		}
	}
	_, err := c.Write([]byte{5, 0xff})
	return err
}

func readE2ESOCKS5Target(r io.Reader) (string, error) {
	var request [4]byte
	if _, err := io.ReadFull(r, request[:]); err != nil {
		return "", err
	}
	if request[0] != 5 || request[1] != 1 {
		return "", fmt.Errorf("expected SOCKS5 CONNECT")
	}
	var host string
	switch request[3] {
	case 1:
		var addr [4]byte
		if _, err := io.ReadFull(r, addr[:]); err != nil {
			return "", err
		}
		host = net.IP(addr[:]).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return "", err
		}
		name := make([]byte, int(size[0]))
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		host = string(name)
	case 4:
		var addr [16]byte
		if _, err := io.ReadFull(r, addr[:]); err != nil {
			return "", err
		}
		host = net.IP(addr[:]).String()
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type %d", request[3])
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

func e2eSOCKS5Reply(code byte) []byte { return []byte{5, code, 0, 1, 127, 0, 0, 1, 0, 1} }

func e2eCopyConn(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func (s *e2ePostPoolFake) activate(ctx context.Context) error {
	s.live.Store(true)
	return e2ePostPoolReady(ctx, s)
}

func (s *e2ePostPoolFake) stopListening(ctx context.Context) error {
	s.live.Store(false)
	s.closeOne.Do(func() { _ = s.listener.Close() })
	select {
	case <-s.acceptDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *e2ePostPoolFake) close() {
	s.mu.Lock()
	s.closing = true
	s.live.Store(false)
	s.mu.Unlock()
	s.closeOne.Do(func() { _ = s.listener.Close() })
	<-s.acceptDone
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(e2eKillGrace):
		s.t.Errorf("post-hop fake handlers did not stop within %v", e2eKillGrace)
	}
}

func (s *e2ePostPoolFake) forwardedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forwarded
}

func (s *e2ePostPoolFake) recordedTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *e2ePostPoolFake) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("live=%t forwarded=%d active=%d targets=%q", s.live.Load(), s.forwarded, len(s.conns), s.targets)
}

func e2eWriteSOCKS5Connect(c net.Conn, target string) error {
	host, rawPort, err := net.SplitHostPort(target)
	if err != nil || len(host) == 0 || len(host) > 255 {
		return fmt.Errorf("invalid test target")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid test target port")
	}
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, host...)
	request = append(request, byte(port>>8), byte(port))
	_, err = c.Write(request)
	return err
}

func e2eReadSOCKS5Reply(c net.Conn) error {
	var reply [4]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 {
		return fmt.Errorf("SOCKS5 CONNECT status %d", reply[1])
	}
	switch reply[3] {
	case 1:
		var rest [6]byte
		_, err := io.ReadFull(c, rest[:])
		return err
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return err
		}
		rest := make([]byte, int(size[0])+2)
		_, err := io.ReadFull(c, rest)
		return err
	case 4:
		var rest [18]byte
		_, err := io.ReadFull(c, rest[:])
		return err
	default:
		return fmt.Errorf("unsupported SOCKS5 reply address type %d", reply[3])
	}
}

func e2ePostPoolClient(ctx context.Context, proxy, login, password, target string) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy)
	if err != nil {
		return err
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	write := func(data []byte) error {
		for len(data) > 0 {
			n, err := c.Write(data)
			if err != nil {
				return err
			}
			data = data[n:]
		}
		return nil
	}
	read := func(size int) ([]byte, error) {
		buf := make([]byte, size)
		_, err := io.ReadFull(c, buf)
		return buf, err
	}
	if err := write([]byte{5, 1, 2}); err != nil {
		return err
	}
	method, err := read(2)
	if err != nil {
		return err
	}
	if method[0] != 5 || method[1] != 2 {
		return fmt.Errorf("account proxy selected SOCKS5 method %v", method)
	}
	if len(login) == 0 || len(login) > 255 || len(password) > 255 {
		return fmt.Errorf("invalid SOCKS5 account credentials")
	}
	auth := []byte{1, byte(len(login))}
	auth = append(auth, login...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, password...)
	if err := write(auth); err != nil {
		return err
	}
	authReply, err := read(2)
	if err != nil {
		return err
	}
	if authReply[0] != 1 || authReply[1] != 0 {
		return fmt.Errorf("account authentication failed")
	}
	if err := e2eWriteSOCKS5Connect(c, target); err != nil {
		return err
	}
	if err := e2eReadSOCKS5Reply(c); err != nil {
		return err
	}
	payload := []byte("post-hop-pool-echo")
	if err := write(payload); err != nil {
		return err
	}
	got, err := read(len(payload))
	if err != nil {
		return err
	}
	if string(got) != string(payload) {
		return fmt.Errorf("test echo mismatch")
	}
	return nil
}

func e2ePostPoolTryUntilSuccess(ctx context.Context, proxy, login, password, target string, dead, live *e2ePostPoolFake, maxAttempts int) (string, int, error) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", attempt - 1, err
		}
		deadBefore, liveBefore := dead.forwardedCount(), live.forwardedCount()
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := e2ePostPoolClient(attemptCtx, proxy, login, password, target)
		cancel()
		if err == nil {
			if live.forwardedCount() == liveBefore+1 && dead.forwardedCount() == deadBefore {
				return "initially-live", attempt, nil
			}
			if dead.forwardedCount() == deadBefore+1 && live.forwardedCount() == liveBefore {
				return "initially-dead", attempt, nil
			}
			return "unexpected-forwarder", attempt, nil
		}
	}
	return "", maxAttempts, fmt.Errorf("no successful echo in %d bounded attempts", maxAttempts)
}

func e2ePostPoolReady(ctx context.Context, fake *e2ePostPoolFake) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", fake.address())
	if err != nil {
		return fmt.Errorf("readiness dial failed: %w", err)
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	}
	if err := e2eSOCKS5ClientGreeting(c); err != nil {
		return fmt.Errorf("readiness handshake failed: %w", err)
	}
	select {
	case <-fake.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestE2EAccountPostHopPoolBindingAndRecovery(t *testing.T) {
	wholeTest, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	home := t.TempDir()
	password := "post-pool-account-password"
	hash, err := auth.Hash(password)
	if err != nil {
		t.Fatal("hash account password")
	}
	backend := accountsEcho(t)
	_, portText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal("split echo backend address")
	}
	target := net.JoinHostPort(e2ePoolTargetHost, portText)
	dead := newE2EPostPoolFake(t, backend, target, false)
	deadAddress := dead.address()
	live := newE2EPostPoolFake(t, backend, target, true)
	liveAddress := live.address()
	liveReadyCtx, liveReadyCancel := context.WithTimeout(wholeTest, 10*time.Second)
	liveReadyErr := e2ePostPoolReady(liveReadyCtx, live)
	liveReadyCancel()
	if liveReadyErr != nil {
		t.Fatalf("initially-live fake did not become ready: %v", liveReadyErr)
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Exit: "direct"}}
	})
	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	listen := p.Status().Data.Listen
	testutil.LogOnFail(t, "post-hop pool fake state", func() string {
		return "initially-dead fake: " + dead.summary() + "\ninitially-live fake: " + live.summary()
	})

	deadURL, liveURL := "socks5://"+deadAddress, "socks5://"+live.address()
	code, out, stderr := accountsCommand(t, home, "set-socks5", "alice", "--post", deadURL, "--post", liveURL)
	if code != 0 {
		t.Fatal("account set-socks5 with repeated --post did not succeed")
	}
	assertNoSecrets(t, out, stderr, password, hash)
	waitLive(t, home, "repeated post-hop list", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })

	if dead.address() != deadAddress || live.address() != liveAddress {
		t.Fatal("post-hop fake address changed")
	}
	first, attempts, err := e2ePostPoolTryUntilSuccess(wholeTest, listen, "alice", password, target, dead, live, 40)
	if err != nil {
		t.Fatalf("first routed echo failed after %d bounded attempts", attempts)
	}
	if first != "initially-live" {
		t.Fatalf("first successful echo used %s, want initially-live fake", first)
	}
	if got := live.recordedTargets(); len(got) < 1 || got[len(got)-1] != target {
		t.Fatalf("initially-live fake did not record original target %q: %q", target, got)
	}

	deadBefore, liveBefore := dead.forwardedCount(), live.forwardedCount()
	secondCtx, secondCancel := context.WithTimeout(wholeTest, 10*time.Second)
	secondErr := e2ePostPoolClient(secondCtx, listen, "alice", password, target)
	secondCancel()
	if secondErr != nil {
		t.Fatalf("second same-login/site connection failed: %v", secondErr)
	}
	if live.forwardedCount() != liveBefore+1 || dead.forwardedCount() != deadBefore {
		t.Fatalf("second same-login/site connection used a different post hop: dead=%d live=%d", dead.forwardedCount()-deadBefore, live.forwardedCount()-liveBefore)
	}
	if got := live.recordedTargets(); len(got) == 0 || got[len(got)-1] != target {
		t.Fatalf("same-login/site target changed: want %q, got %q", target, got)
	}

	stopCtx, stopCancel := context.WithTimeout(wholeTest, 5*time.Second)
	stopErr := live.stopListening(stopCtx)
	stopCancel()
	if stopErr != nil {
		t.Fatalf("could not stop site-bound live hop: %v", stopErr)
	}
	// This fake permits only the configured .test target and loopback hop
	// endpoints; it cannot initiate public-network traffic.
	for attempt := 0; attempt < 3; attempt++ {
		failCtx, failCancel := context.WithTimeout(wholeTest, 5*time.Second)
		connectErr := e2ePostPoolClient(failCtx, listen, "alice", password, target)
		failCancel()
		if connectErr == nil {
			t.Fatalf("connection %d succeeded without a usable post hop", attempt+1)
		}
	}
	readyCtx, readyCancel := context.WithTimeout(wholeTest, 10*time.Second)
	readyErr := dead.activate(readyCtx)
	readyCancel()
	if readyErr != nil {
		t.Fatalf("initially-dead fake did not become ready on its reserved address: %v", readyErr)
	}
	if dead.address() != deadAddress {
		t.Fatal("activated post-hop did not retain its initially reserved address")
	}
	targetsBeforeRecovery := dead.recordedTargets()
	recovered, attempts, err := e2ePostPoolTryUntilSuccess(wholeTest, listen, "alice", password, target, dead, live, 40)
	if err != nil {
		t.Fatalf("account did not recover through alternate post hop after %d bounded attempts", attempts)
	}
	if recovered != "initially-dead" {
		t.Fatalf("recovery used %s, want activated alternate fake", recovered)
	}
	if got := dead.recordedTargets(); len(got) <= len(targetsBeforeRecovery) || got[len(got)-1] != target {
		t.Fatalf("recovered post-hop did not record original target %q after activation: before=%q after=%q", target, targetsBeforeRecovery, got)
	}
}
