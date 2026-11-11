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

	mu              sync.Mutex
	closing         bool
	conns           map[net.Conn]struct{}
	targets         []string
	forwarded       int
	forwardedTarget map[string]int
	acceptDone      chan struct{}
	wg              sync.WaitGroup
	closeOne        sync.Once
}

func newE2EPostPoolFake(t *testing.T, backend, target string) *e2ePostPoolFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("post-hop fake listen: %v", err)
	}
	s := &e2ePostPoolFake{
		t: t, listener: ln, backend: backend, target: target,
		ready: make(chan struct{}), acceptReady: make(chan struct{}),
		conns: make(map[net.Conn]struct{}), forwardedTarget: make(map[string]int), acceptDone: make(chan struct{}),
	}
	s.live.Store(true)
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

func (s *e2ePostPoolFake) allowsTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	baseHost, basePort, baseErr := net.SplitHostPort(s.target)
	if err != nil || baseErr != nil || port != basePort {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	baseHost = strings.ToLower(strings.TrimSuffix(baseHost, "."))
	return host == baseHost || host == "alpha."+baseHost || host == "beta."+baseHost
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
	// If shutdown raced an accepted connection, close it without SOCKS negotiation.
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
	if !s.allowsTarget(target) {
		_, _ = c.Write(e2eSOCKS5Reply(2))
		return
	}
	// Approved .test targets are fixed by this test and map directly to the
	// loopback echo endpoint; the fake performs no DNS or external network access.
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
	s.forwardedTarget[target]++
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

func (s *e2ePostPoolFake) forwardedTargetCount(target string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forwardedTarget[target]
}

func (s *e2ePostPoolFake) recordedTargetCount(target string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, got := range s.targets {
		if strings.EqualFold(got, target) {
			count++
		}
	}
	return count
}

func (s *e2ePostPoolFake) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("live=%t forwarded=%d active=%d targets=%q forwarded-targets=%v", s.live.Load(), s.forwarded, len(s.conns), s.targets, s.forwardedTarget)
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

func e2ePostPoolTryUntilSuccess(ctx context.Context, proxy, login, password, target string, first, second *e2ePostPoolFake, maxAttempts int) (string, int, error) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", attempt - 1, err
		}
		firstBefore, secondBefore := first.forwardedTargetCount(target), second.forwardedTargetCount(target)
		firstObservedBefore, secondObservedBefore := first.recordedTargetCount(target), second.recordedTargetCount(target)
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := e2ePostPoolClient(attemptCtx, proxy, login, password, target)
		cancel()
		if err == nil {
			if first.forwardedTargetCount(target) == firstBefore+1 && second.forwardedTargetCount(target) == secondBefore && first.recordedTargetCount(target) == firstObservedBefore+1 && second.recordedTargetCount(target) == secondObservedBefore {
				return "first", attempt, nil
			}
			if second.forwardedTargetCount(target) == secondBefore+1 && first.forwardedTargetCount(target) == firstBefore && second.recordedTargetCount(target) == secondObservedBefore+1 && first.recordedTargetCount(target) == firstObservedBefore {
				return "second", attempt, nil
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
	fakeA := newE2EPostPoolFake(t, backend, target)
	fakeAAddress := fakeA.address()
	fakeB := newE2EPostPoolFake(t, backend, target)
	fakeBAddress := fakeB.address()
	for name, fake := range map[string]*e2ePostPoolFake{"fake A": fakeA, "fake B": fakeB} {
		readyCtx, readyCancel := context.WithTimeout(wholeTest, 10*time.Second)
		readyErr := e2ePostPoolReady(readyCtx, fake)
		readyCancel()
		if readyErr != nil {
			t.Fatalf("%s did not become ready before pool connections: %v", name, readyErr)
		}
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Exit: "direct"}}
	})
	p := startUp(t, home, "--port", "0")
	assertStatusSuccess(t, p.Status())
	listen := p.Status().Data.Listen
	testutil.LogOnFail(t, "post-hop pool fake state", func() string {
		return "fake A: " + fakeA.summary() + "\nfake B: " + fakeB.summary()
	})

	fakeAURL, fakeBURL := "socks5://"+fakeAAddress, "socks5://"+fakeBAddress
	code, out, stderr := accountsCommand(t, home, "set-socks5", "alice", "--post", fakeAURL, "--post", fakeBURL)
	if code != 0 {
		t.Fatal("account set-socks5 with repeated --post did not succeed")
	}
	assertNoSecrets(t, out, stderr, password, hash)
	waitLive(t, home, "repeated post-hop list", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })

	if fakeA.address() != fakeAAddress || fakeB.address() != fakeBAddress {
		t.Fatal("post-hop fake address changed")
	}
	first, attempts, err := e2ePostPoolTryUntilSuccess(wholeTest, listen, "alice", password, target, fakeA, fakeB, 40)
	if err != nil {
		t.Fatalf("first routed echo failed after %d bounded attempts", attempts)
	}
	var bound, alternate *e2ePostPoolFake
	switch first {
	case "first":
		bound, alternate = fakeA, fakeB
	case "second":
		bound, alternate = fakeB, fakeA
	default:
		t.Fatalf("first successful echo used unexpected post-hop %s", first)
	}

	targets := []string{
		net.JoinHostPort("alpha.pool-route.test", portText),
		net.JoinHostPort("beta.pool-route.test", portText),
		target,
	}
	boundBefore, alternateBefore := bound.forwardedCount(), alternate.forwardedCount()
	for connection := 0; connection < 12; connection++ {
		appTarget := targets[connection%len(targets)]
		boundTargetBefore := bound.forwardedTargetCount(appTarget)
		boundObservedBefore := bound.recordedTargetCount(appTarget)
		alternateTargetBefore := alternate.forwardedTargetCount(appTarget)
		alternateObservedBefore := alternate.recordedTargetCount(appTarget)
		connectCtx, connectCancel := context.WithTimeout(wholeTest, 10*time.Second)
		connectErr := e2ePostPoolClient(connectCtx, listen, "alice", password, appTarget)
		connectCancel()
		if connectErr != nil {
			t.Fatalf("same-login/site connection %d to %q failed: %v", connection+1, appTarget, connectErr)
		}
		if bound.forwardedCount() != boundBefore+connection+1 || alternate.forwardedCount() != alternateBefore {
			t.Fatalf("same-login/site connection %d switched post hops: bound=%d alternate=%d", connection+1, bound.forwardedCount()-boundBefore, alternate.forwardedCount()-alternateBefore)
		}
		if bound.forwardedTargetCount(appTarget) != boundTargetBefore+1 || alternate.forwardedTargetCount(appTarget) != alternateTargetBefore || bound.recordedTargetCount(appTarget) != boundObservedBefore+1 || alternate.recordedTargetCount(appTarget) != alternateObservedBefore {
			t.Fatalf("connection %d did not observe and forward original target %q through only the bound hop", connection+1, appTarget)
		}
	}
	if !fakeA.live.Load() || !fakeB.live.Load() {
		t.Fatal("both post-hop fakes must remain live during repeated same-site connections")
	}

	stopCtx, stopCancel := context.WithTimeout(wholeTest, 5*time.Second)
	stopErr := bound.stopListening(stopCtx)
	stopCancel()
	if stopErr != nil {
		t.Fatalf("could not stop site-bound post hop: %v", stopErr)
	}
	if bound.live.Load() || !alternate.live.Load() {
		t.Fatal("stopped bound fake and live alternate fake are not in the expected state")
	}
	// The alternate fake remains live. Retry with bounded per-attempt contexts
	// until the pool quarantines the stopped hop and succeeds through it.
	recovered, attempts, err := e2ePostPoolTryUntilSuccess(wholeTest, listen, "alice", password, target, bound, alternate, 40)
	if err != nil {
		t.Fatalf("account did not recover through alternate post hop after %d bounded attempts", attempts)
	}
	if recovered != "second" {
		t.Fatalf("recovery used %s fake, want the alternate post hop", recovered)
	}
	if bound.recordedTargetCount(target) != bound.forwardedTargetCount(target) || alternate.recordedTargetCount(target) != alternate.forwardedTargetCount(target) || alternate.forwardedTargetCount(target) < 1 {
		t.Fatalf("recovery target %q was not observed and forwarded through only the alternate fake", target)
	}
}
