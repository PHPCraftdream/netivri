//go:build integration

package servers_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
	"github.com/metacubex/mihomo/component/ca"
)

func TestTLSMaterialLoopbackVerification(t *testing.T) {
	material, err := servers.NewTLSMaterial(nil, "localhost")
	if err != nil {
		t.Fatal("could not generate TLS fixture")
	}
	certValue, keyValue := servers.TLSFiles(material)
	cert, err := tls.X509KeyPair([]byte(certValue), []byte(keyValue))
	if err != nil {
		t.Fatal("TLS fixture invalid")
	}

	t.Run("trusted pool", func(t *testing.T) {
		servertest.TrustForTest(t, material)
		config := &tls.Config{RootCAs: ca.GetCertPool().Clone(), ServerName: "localhost", MinVersion: tls.VersionTLS12}
		clientErr, serverErr := exchangeTLS(t, cert, config)
		if clientErr != nil || serverErr != nil {
			t.Fatal("trusted TLS handshake failed")
		}
	})
	t.Run("pinned leaf", func(t *testing.T) {
		verifier, err := ca.NewFingerprintVerifier(material["fingerprint"], time.Now)
		if err != nil {
			t.Fatal("pin verifier setup failed")
		}
		config := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, VerifyConnection: func(state tls.ConnectionState) error { return verifier(state.PeerCertificates, "localhost") }}
		clientErr, serverErr := exchangeTLS(t, cert, config)
		if clientErr != nil || serverErr != nil {
			t.Fatal("pinned TLS handshake failed")
		}
	})
	t.Run("wrong pin", func(t *testing.T) {
		verifier, err := ca.NewFingerprintVerifier("0000000000000000000000000000000000000000000000000000000000000000", time.Now)
		if err != nil {
			t.Fatal("pin verifier setup failed")
		}
		called := false
		var verifierErr error
		config := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, VerifyConnection: func(state tls.ConnectionState) error {
			called = true
			verifierErr = verifier(state.PeerCertificates, "localhost")
			return verifierErr
		}}
		clientErr, _ := exchangeTLS(t, cert, config)
		if !called || verifierErr == nil || !errors.Is(clientErr, verifierErr) {
			t.Fatal("wrong pin was not rejected by the pin verifier")
		}
	})

	attempts := 0
	port := servertest.StartOnFreePort(t, func(port int) error {
		attempts++
		if attempts == 1 {
			return &net.OpError{Op: "listen", Net: "tcp", Err: syscall.EADDRINUSE}
		}
		return nil
	})
	if port < 1 || attempts != 2 {
		t.Fatal("free-port startup retry failed")
	}
}

func exchangeTLS(t *testing.T, certificate tls.Certificate, clientConfig *tls.Config) (clientErr, serverErr error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.New("listener setup failed"), errors.New("listener setup failed")
	}
	defer listener.Close()
	deadline := time.Now().Add(8 * time.Second)
	if err := listener.(*net.TCPListener).SetDeadline(deadline); err != nil {
		return errors.New("listener deadline failed"), errors.New("listener deadline failed")
	}
	serverDone := make(chan error, 1)
	go func() {
		raw, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		if deadlineErr := raw.SetDeadline(deadline); deadlineErr != nil {
			_ = raw.Close()
			serverDone <- deadlineErr
			return
		}
		handshakeErr := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}).Handshake()
		_ = raw.Close()
		serverDone <- handshakeErr
	}()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	raw, dialErr := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", listener.Addr().String())
	if dialErr != nil {
		_ = listener.Close()
		return errors.New("client dial failed"), <-serverDone
	}
	if deadlineErr := raw.SetDeadline(deadline); deadlineErr != nil {
		_ = raw.Close()
		_ = listener.Close()
		return errors.New("client deadline failed"), <-serverDone
	}
	clientErr = tls.Client(raw, clientConfig).Handshake()
	_ = raw.Close()
	serverErr = <-serverDone
	return clientErr, serverErr
}
