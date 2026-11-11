package proxyclient

import (
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

func TestHTTPSConnectTLSValidationAndPayload(t *testing.T) {
	config, leaf := testTLSConfig(t)
	originalServerName := config.ServerName
	client, rawServer := pipePair(t)
	t.Cleanup(func() { _ = client.Close(); _ = rawServer.Close() })
	server := tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	peer := peerResult()
	go func() {
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
	client, rawServer := pipePair(t)
	t.Cleanup(func() { _ = client.Close(); _ = rawServer.Close() })
	server := tls.Server(rawServer, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	peer := peerResult()
	go func() {
		err := server.Handshake()
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
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
		if peerErr == nil {
			t.Fatal("TLS peer did not report the verification alert")
		}
		if errors.Is(peerErr, context.DeadlineExceeded) {
			t.Fatal("TLS peer timed out instead of receiving the verification alert")
		}
		var networkErr interface{ Timeout() bool }
		if errors.As(peerErr, &networkErr) && networkErr.Timeout() {
			t.Fatal("TLS peer timed out instead of receiving the verification alert")
		}
	case <-time.After(15 * time.Second):
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
	client, server := pipePair(t)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const watchdog = 2 * time.Second
	started := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
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
