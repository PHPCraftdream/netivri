//go:build integration

package core

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// httpHop is an HTTP CONNECT proxy recording the requested destinations and
// the credentials it was given. With wantAuth set it refuses a CONNECT that
// does not carry exactly those basic credentials.
type httpHop struct {
	wantAuth string // "user:password" or empty
	mu       sync.Mutex
	targets  []string
	auths    []string
}

func startHTTPHop(t *testing.T, wantAuth string) (*httpHop, string) {
	t.Helper()
	h := &httpHop{wantAuth: wantAuth}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("http hop listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return h, ln.Addr().String()
}

func (h *httpHop) snapshot() (targets, auths []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.targets...), append([]string(nil), h.auths...)
}

func (h *httpHop) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(testTimeout))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	auth := req.Header.Get("Proxy-Authorization")
	h.mu.Lock()
	h.targets = append(h.targets, req.Host)
	h.auths = append(h.auths, auth)
	h.mu.Unlock()
	if h.wantAuth != "" && auth != "Basic "+base64.StdEncoding.EncodeToString([]byte(h.wantAuth)) {
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"hop\"\r\n\r\n")
		return
	}
	up, err := net.DialTimeout("tcp", req.Host, testTimeout)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	if _, err = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
}

// The hops of a chain may be HTTP CONNECT proxies: the node is reached through
// the pre hop, the post hop gets the connection from the node and, with
// credentials in its URL, sees them.
func TestHTTPPrePostRouting(t *testing.T) {
	pre, preAddr := startHTTPHop(t, "")
	node, nodeAddr := startChainRecorder(t)
	post, postAddr := startHTTPHop(t, "alice:s3cret")
	backend := echoServer(t)
	inst, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t), Authenticator: aliceAuthenticator(),
		RouteNodes: map[string][]map[string]any{"de": {nodeMap(nodeAddr)}}, AccountRoute: map[string]string{"alice": "de"},
		RoutePre: map[string]string{"de": "http://" + preAddr}, RoutePost: map[string]string{"de": "http://alice:s3cret@" + postAddr},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "alice", alicePassword); err != nil {
		t.Fatal(err)
	}
	if got, _ := pre.snapshot(); len(got) != 1 || got[0] != nodeAddr {
		t.Errorf("pre CONNECTs = %v, want the selected node %s", got, nodeAddr)
	}
	if got := node.snapshot(); len(got) != 1 || got[0] != postAddr {
		t.Errorf("node CONNECTs = %v, want the post hop %s", got, postAddr)
	}
	got, auths := post.snapshot()
	if len(got) != 1 || got[0] != backend {
		t.Errorf("post CONNECTs = %v, want the destination %s", got, backend)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")); len(auths) != 1 || auths[0] != want {
		t.Errorf("post hop saw credentials %q, want %q", auths, want)
	}
}

// An HTTP post hop that refuses the credentials fails the connection instead
// of letting it go through unproxied.
func TestHTTPPostHopThatRefusesFailsTheConnection(t *testing.T) {
	_, nodeAddr := startChainRecorder(t)
	post, postAddr := startHTTPHop(t, "alice:s3cret")
	backend := echoServer(t)
	var mu sync.Mutex
	var kinds []string
	inst, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t), Authenticator: aliceAuthenticator(),
		RouteNodes: map[string][]map[string]any{"de": {nodeMap(nodeAddr)}}, AccountRoute: map[string]string{"alice": "de"},
		RoutePost: map[string]string{"de": "http://alice:wrong@" + postAddr},
		OnConnEvent: func(ev ConnEvent) {
			if ev.Kind == ConnDialFailed {
				mu.Lock()
				kinds = append(kinds, ev.Why)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "alice", alicePassword); err == nil {
		t.Fatal("the connection went through a post hop that answered 407")
	}
	if got, _ := post.snapshot(); len(got) == 0 {
		t.Error("the post hop was never asked")
	}
	// The log says what went wrong with the hop, not just "error".
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) == 0 || kinds[0] != "proxy auth failed" {
		t.Errorf("failed dial kinds = %q, want proxy auth failed", kinds)
	}
}

// startTLSHTTPHop is an HTTP CONNECT proxy behind TLS with a self-signed
// certificate for 127.0.0.1, which no client trusts.
func startTLSHTTPHop(t *testing.T) (*httpHop, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hop"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	t.Cleanup(func() { _ = ln.Close() })
	h := &httpHop{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return h, raw.Addr().String()
}

// An https hop is reached over TLS with the certificate checked: one that no
// client trusts fails the connection before anything is sent through it, and
// the log names it a failed handshake.
func TestHTTPSHopWithAnUntrustedCertificateFails(t *testing.T) {
	_, nodeAddr := startChainRecorder(t)
	post, postAddr := startTLSHTTPHop(t)
	backend := echoServer(t)
	var mu sync.Mutex
	var kinds []string
	inst, err := Start(context.Background(), Options{
		HomeDir: testutil.TempHome(t), Authenticator: aliceAuthenticator(),
		RouteNodes: map[string][]map[string]any{"de": {nodeMap(nodeAddr)}}, AccountRoute: map[string]string{"alice": "de"},
		RoutePost: map[string]string{"de": "https://" + postAddr},
		OnConnEvent: func(ev ConnEvent) {
			if ev.Kind == ConnDialFailed {
				mu.Lock()
				kinds = append(kinds, ev.Why)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	if err := tryDialAccountSOCKS5(t, inst.Addr(), "127.0.0.1", backend, "alice", alicePassword); err == nil {
		t.Fatal("the connection went through an https hop with an untrusted certificate")
	}
	if got, _ := post.snapshot(); len(got) != 0 {
		t.Errorf("the hop got CONNECTs %v although the certificate was not trusted", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) == 0 || kinds[0] != "handshake failed" {
		t.Errorf("failed dial kinds = %q, want handshake failed", kinds)
	}
}
