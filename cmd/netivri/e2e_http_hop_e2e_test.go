//go:build e2e

package main

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// httpHopUpstream is an HTTP CONNECT proxy that counts the tunnels it opened
// and remembers the credentials of the first.
type httpHopUpstream struct {
	listener net.Listener
	count    atomic.Int64
	wg       sync.WaitGroup
	mu       sync.Mutex
	auth     string
}

func httpHopServer(t *testing.T) *httpHopUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &httpHopUpstream{listener: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); s.wg.Wait() })
	return s
}

func (s *httpHopUpstream) credentials() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth
}

func (s *httpHopUpstream) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	out, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer out.Close()
	_ = out.SetDeadline(time.Now().Add(20 * time.Second))
	s.mu.Lock()
	if s.auth == "" {
		s.auth = req.Header.Get("Proxy-Authorization")
	}
	s.mu.Unlock()
	s.count.Add(1)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, out); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = out.Close()
	<-done
}

// A running angel applies an HTTP proxy as the post hop of an account, set with
// the account CLI: the traffic goes through the node and then through the hop,
// which sees the credentials, and `account show` does not print them.
func TestE2ELivePostHTTPHop(t *testing.T) {
	f := startLive(t)
	hop := httpHopServer(t)
	hopURL := "http://alice:s3cret@" + hop.listener.Addr().String()

	code, out, stderr := accountsCommand(t, f.home, "set-socks5", "alice", "--post", hopURL)
	if code != 0 {
		t.Fatalf("account set-socks5 --post http://...: %s%s", out, stderr)
	}
	if strings.Contains(out+stderr, "s3cret") {
		t.Errorf("set-socks5 printed the credentials: %s%s", out, stderr)
	}
	waitLive(t, f.home, "account post HTTP hop", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if hop.count.Load() != 1 {
		t.Fatalf("hop tunnels = %d, want 1", hop.count.Load())
	}
	if f.p1.count.Load() != 1 {
		t.Fatalf("node connections = %d, want 1", f.p1.count.Load())
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")); hop.credentials() != want {
		t.Errorf("the hop saw %q, want %q", hop.credentials(), want)
	}

	code, out, stderr = accountsCommand(t, f.home, "show", "alice")
	if code != 0 {
		t.Fatalf("account show: %s%s", out, stderr)
	}
	if !strings.Contains(out, "http://"+hop.listener.Addr().String()) || strings.Contains(out+stderr, "s3cret") {
		t.Errorf("account show must name the hop and hide its credentials: %s", out)
	}
	time.Sleep(liveSettle)
}
