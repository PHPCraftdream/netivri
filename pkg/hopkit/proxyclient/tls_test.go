package proxyclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func testTLSConfig(t *testing.T) (*tls.Config, *tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("could not generate test TLS key")
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "proxy test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal("could not generate test TLS CA")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("could not generate test TLS leaf key")
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "proxy.example"}, DNSNames: []string{"proxy.example"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal("could not generate test TLS leaf")
	}
	leaf := tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
	roots := x509.NewCertPool()
	roots.AddCert(mustParseCert(t, caDER))
	return &tls.Config{RootCAs: roots, ServerName: "caller-name.invalid"}, &leaf
}

func mustParseCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal("could not parse test certificate")
	}
	return cert
}

// bufferedTLSPipePair is a hermetic, in-memory duplex stream. Its per-direction
// byte buffers let the peer read an alert even if a prior TLS flight is still
// being written, unlike net.Pipe's synchronous writes.
func bufferedTLSPipePair() (net.Conn, net.Conn) {
	clientToServer := &bufferedPipe{changed: make(chan struct{})}
	serverToClient := &bufferedPipe{changed: make(chan struct{})}
	client := &bufferedPipeConn{in: serverToClient, out: clientToServer, closed: make(chan struct{})}
	server := &bufferedPipeConn{in: clientToServer, out: serverToClient, closed: make(chan struct{})}
	return client, server
}

type bufferedPipe struct {
	mu           sync.Mutex
	data         bytes.Buffer
	writerClosed bool
	readerClosed bool
	changed      chan struct{}
}

func (p *bufferedPipe) signalLocked() {
	if p.changed != nil {
		close(p.changed)
	}
	p.changed = make(chan struct{})
}

type bufferedPipeConn struct {
	in, out       *bufferedPipe
	closed        chan struct{}
	closeOnce     sync.Once
	mu            sync.Mutex
	isClosed      bool
	readDeadline  time.Time
	writeDeadline time.Time
}

func (c *bufferedPipeConn) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for {
		c.mu.Lock()
		closed := c.isClosed
		c.mu.Unlock()
		if closed {
			return 0, net.ErrClosed
		}

		c.in.mu.Lock()
		if c.in.data.Len() > 0 {
			n, _ := c.in.data.Read(dst)
			c.in.mu.Unlock()
			return n, nil
		}
		if c.in.writerClosed {
			c.in.mu.Unlock()
			return 0, io.EOF
		}
		changed := c.in.changed
		c.in.mu.Unlock()

		c.mu.Lock()
		deadline := c.readDeadline
		c.mu.Unlock()
		if deadline.IsZero() {
			select {
			case <-c.closed:
				return 0, net.ErrClosed
			case <-changed:
			}
			continue
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-c.closed:
			timer.Stop()
			return 0, net.ErrClosed
		case <-changed:
			timer.Stop()
		case <-timer.C:
			c.mu.Lock()
			stillExpired := !c.readDeadline.IsZero() && !time.Now().Before(c.readDeadline)
			c.mu.Unlock()
			if stillExpired {
				return 0, os.ErrDeadlineExceeded
			}
		}
	}
}

func (c *bufferedPipeConn) Write(src []byte) (int, error) {
	if len(src) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isClosed {
		return 0, net.ErrClosed
	}
	if !c.writeDeadline.IsZero() && !time.Now().Before(c.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}
	c.out.mu.Lock()
	defer c.out.mu.Unlock()
	if c.out.readerClosed {
		return 0, io.ErrClosedPipe
	}
	_, _ = c.out.data.Write(src)
	c.out.signalLocked()
	return len(src), nil
}

func (c *bufferedPipeConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.isClosed = true
		close(c.closed)
		c.mu.Unlock()

		c.in.mu.Lock()
		c.in.readerClosed = true
		c.in.signalLocked()
		c.in.mu.Unlock()
		c.out.mu.Lock()
		c.out.writerClosed = true
		c.out.signalLocked()
		c.out.mu.Unlock()
	})
	return nil
}

func (c *bufferedPipeConn) LocalAddr() net.Addr  { return bufferedPipeAddr{} }
func (c *bufferedPipeConn) RemoteAddr() net.Addr { return bufferedPipeAddr{} }

func (c *bufferedPipeConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.writeDeadline = deadline
	c.mu.Unlock()
	c.in.mu.Lock()
	c.in.signalLocked()
	c.in.mu.Unlock()
	return nil
}

func (c *bufferedPipeConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	c.in.mu.Lock()
	c.in.signalLocked()
	c.in.mu.Unlock()
	return nil
}

func (c *bufferedPipeConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

type bufferedPipeAddr struct{}

func (bufferedPipeAddr) Network() string { return "buffered-pipe" }
func (bufferedPipeAddr) String() string  { return "buffered-pipe" }

func TestHTTPSConnectTLSValidationAndPayload(t *testing.T) {
	config, leaf := testTLSConfig(t)
	originalServerName := config.ServerName
	client, rawServer := bufferedTLSPipePair()
	server := tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	peer := peerResult()
	peerFinished := make(chan struct{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = rawServer.Close()
		<-peerFinished
	})
	go func() {
		defer close(peerFinished)
		err := server.Handshake()
		if err == nil {
			state := server.ConnectionState()
			if state.ServerName != "proxy.example" {
				err = errors.New("TLS server name mismatch")
			}
		}
		if err == nil {
			request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
			got, e := readExact(server, len(request))
			err = e
			if err == nil {
				checkWire(t, got, []byte(request))
				_, err = server.Write([]byte("HTTP/1.1 200 OK\r\n\r\npayload"))
			}
		}
		if err == nil {
			var marker [3]byte
			_, err = io.ReadFull(server, marker[:])
			if err == nil {
				_, err = server.Write(marker[:])
			}
		}
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	tunnel, err := Handshake(ctx, client, Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, testTarget, WithTLSConfig(config))
	if err != nil {
		_ = checkProxyError(t, err, StageConnect, 0)
		t.Fatal("unexpected TLS handshake failure")
	}
	payload, err := readExact(tunnel, 7)
	if err != nil {
		t.Fatal("could not read HTTPS tunnel payload")
	}
	checkWire(t, payload, []byte("payload"))
	if _, err := tunnel.Write([]byte("abc")); err != nil {
		t.Fatal("could not write through HTTPS tunnel")
	}
	echo, err := readExact(tunnel, 3)
	if err != nil {
		t.Fatal("could not read HTTPS tunnel echo")
	}
	checkWire(t, echo, []byte("abc"))
	if config.ServerName != originalServerName {
		t.Fatal("TLS config was mutated")
	}
	waitErr(t, peer)
}

func TestHTTPSConnectRejectsWrongCAAndIgnoresInsecureOverride(t *testing.T) {
	_, leaf := testTLSConfig(t)
	wrongRoots := x509.NewCertPool()
	wrongRoots.AddCert(mustParseCert(t, mustSelfSigned(t)))
	config := &tls.Config{RootCAs: wrongRoots, InsecureSkipVerify: true, ServerName: "caller-name.invalid"}
	originalServerName := config.ServerName
	client, rawServer := bufferedTLSPipePair()
	server := tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	peer := peerResult()
	peerFinished := make(chan struct{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = rawServer.Close()
		<-peerFinished
	})
	go func() {
		defer close(peerFinished)
		err := server.Handshake()
		peerDone(peer, err)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Handshake(ctx, client, Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, testTarget, WithTLSConfig(config))
	pe := checkProxyError(t, err, StageConnect, 0)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(pe, &unknownAuthority) {
		t.Fatal("TLS verification error was not an unknown-authority error")
	}
	if pe.Timeout() {
		t.Fatal("TLS verification failure was classified as a timeout")
	}
	if config.ServerName != originalServerName || !config.InsecureSkipVerify {
		t.Fatal("TLS config was mutated")
	}
	select {
	case peerErr := <-peer:
		var remoteError *net.OpError
		if !errors.As(peerErr, &remoteError) || remoteError.Op != "remote error" || remoteError.Err == nil {
			t.Fatal("TLS peer error did not identify a remote alert")
		}
		if alert, ok := remoteError.Err.(interface{ Error() string }); !ok || alert.Error() != "tls: bad certificate" {
			t.Fatal("TLS peer received an unexpected TLS alert")
		}
		if errors.Is(peerErr, io.EOF) || errors.Is(peerErr, io.ErrUnexpectedEOF) {
			t.Fatal("TLS peer saw EOF instead of the verification alert")
		}
		if errors.Is(peerErr, context.DeadlineExceeded) {
			t.Fatal("TLS peer timed out instead of receiving the verification alert")
		}
		var networkErr interface{ Timeout() bool }
		if errors.As(peerErr, &networkErr) && networkErr.Timeout() {
			t.Fatal("TLS peer timed out instead of receiving the verification alert")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TLS peer did not finish its alert handshake")
	}
}

func mustSelfSigned(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("could not generate second CA key")
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(99), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal("could not generate second test CA")
	}
	return der
}

func TestTLSHandshakeCancellationIsConnectStage(t *testing.T) {
	client, server := bufferedTLSPipePair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const watchdog = 2 * time.Second
	started := make(chan struct{})
	serverDone := make(chan error, 1)
	serverFinished := make(chan struct{})
	clientFinished := make(chan struct{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		<-serverFinished
		<-clientFinished
	})
	go func() {
		defer close(serverFinished)
		var clientHelloPrefix [6]byte
		_, err := io.ReadFull(server, clientHelloPrefix[:])
		if err == nil && (clientHelloPrefix[0] != 0x16 || clientHelloPrefix[5] != 0x01) {
			err = errors.New("TLS peer did not receive a client hello")
		}
		close(started)
		serverDone <- err
	}()
	done := make(chan error, 1)
	go func() {
		defer close(clientFinished)
		_, err := Handshake(ctx, client, Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, testTarget)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(watchdog):
		_ = client.Close()
		_ = server.Close()
		t.Fatal("TLS client hello did not start")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		elapsed := time.Since(start)
		pe := checkProxyError(t, err, StageConnect, 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatal("TLS handshake error did not preserve context cancellation")
		}
		if pe.Timeout() {
			t.Fatal("TLS cancellation was classified as a timeout")
		}
		if elapsed >= watchdog {
			t.Fatal("TLS handshake did not abort promptly on cancellation")
		}
	case <-time.After(watchdog):
		_ = client.Close()
		_ = server.Close()
		t.Fatal("TLS handshake did not abort on cancellation")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal("TLS peer failed to read the client hello")
		}
	case <-time.After(watchdog):
		_ = client.Close()
		_ = server.Close()
		t.Fatal("TLS peer did not finish")
	}
}
