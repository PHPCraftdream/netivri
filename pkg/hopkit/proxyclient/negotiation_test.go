package proxyclient

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestNegotiationTimeoutFailsSilentSOCKS5AsHandshakeTimeout(t *testing.T) {
	client, server := rawPipePair(t)
	go func() { _, _ = io.Copy(io.Discard, server) }()
	ctx, cancel := testContext(t)
	defer cancel()
	started := time.Now()
	_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget, WithNegotiationTimeout(50*time.Millisecond))
	pe := checkProxyError(t, err, StageHandshake, 0)
	if pe.TargetSent {
		t.Fatal("silent greeting reported the target as sent")
	}
	if !pe.Timeout() || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("silent greeting error: timeout=%t deadline=%t, want a network timeout, not a context error", pe.Timeout(), errors.Is(err, context.DeadlineExceeded))
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("negotiation timeout took %s", elapsed)
	}
}

func TestNegotiationTimeoutIsLiftedBeforeTheTargetRequest(t *testing.T) {
	client, server := rawPipePair(t)
	peer := peerResult()
	go func() {
		_, err := readExact(server, 3)
		if err == nil {
			_, err = server.Write([]byte{5, 0})
		}
		if err == nil {
			_, err = readExact(server, 4+1+len("unresolved-target.invalid")+2)
		}
		if err == nil {
			// Longer than the negotiation timeout: only the context bounds this wait.
			time.Sleep(200 * time.Millisecond)
			_, err = server.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		}
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	conn, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget, WithNegotiationTimeout(50*time.Millisecond))
	if err != nil || conn == nil {
		t.Fatalf("slow CONNECT reply after negotiation = (%v, %v), want success", conn, err)
	}
	waitErr(t, peer)
}

func TestNegotiationTimeoutLiftedForHTTPConnect(t *testing.T) {
	client, server := rawPipePair(t)
	peer := peerResult()
	go func() {
		buf := make([]byte, 4096)
		total := ""
		for len(total) < 4 || total[len(total)-4:] != "\r\n\r\n" {
			n, err := server.Read(buf)
			if err != nil {
				peerDone(peer, err)
				return
			}
			total += string(buf[:n])
		}
		time.Sleep(200 * time.Millisecond)
		_, err := io.WriteString(server, "HTTP/1.1 200 OK\r\n\r\n")
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	conn, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget, WithNegotiationTimeout(50*time.Millisecond))
	if err != nil || conn == nil {
		t.Fatalf("slow HTTP CONNECT reply = (%v, %v), want success", conn, err)
	}
	waitErr(t, peer)
}

func TestNegotiationTimeoutFailsSilentTLSAsConnectTimeout(t *testing.T) {
	client, server := rawPipePair(t)
	go func() { _, _ = io.Copy(io.Discard, server) }()
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Handshake(ctx, client, Proxy{Scheme: "https", Host: "proxy.invalid", Port: 443}, testTarget, WithNegotiationTimeout(50*time.Millisecond))
	pe := checkProxyError(t, err, StageConnect, 0)
	if pe.TargetSent || !pe.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("silent TLS: targetSent=%t timeout=%t contextDeadline=%t", pe.TargetSent, pe.Timeout(), errors.Is(err, context.DeadlineExceeded))
	}
}

func TestNegotiationTimeoutBeyondContextDeadlineLeavesTheContextInCharge(t *testing.T) {
	client, server := rawPipePair(t)
	go func() { _, _ = io.Copy(io.Discard, server) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget, WithNegotiationTimeout(10*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the context deadline", err)
	}
}
