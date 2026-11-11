package proxyclient

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestSOCKS5SuccessWireAndTunnel(t *testing.T) {
	cases := []struct {
		name     string
		proxy    Proxy
		target   string
		greeting []byte
		method   byte
		auth     []byte
		request  []byte
	}{
		{"domain no auth", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget, []byte{5, 1, 0}, 0, nil, []byte{5, 1, 0, 3, 25, 'u', 'n', 'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', '.', 'i', 'n', 'v', 'a', 'l', 'i', 'd', 1, 187}},
		{"IPv4 no auth", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, "192.0.2.7:8081", []byte{5, 1, 0}, 0, nil, []byte{5, 1, 0, 1, 192, 0, 2, 7, 31, 145}},
		{"IPv6 no auth", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, "[2001:db8::7]:8443", []byte{5, 1, 0}, 0, nil, []byte{5, 1, 0, 4, 32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7, 32, 251}},
		{"domain userpass", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, testTarget, []byte{5, 2, 0, 2}, 2, []byte{1, 12, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'u', 's', 'e', 'r', 16, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'}, []byte{5, 1, 0, 3, 25, 'u', 'n', 'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', '.', 'i', 'n', 'v', 'a', 'l', 'i', 'd', 1, 187}},
		{"IPv4 userpass", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, "192.0.2.7:8081", []byte{5, 2, 0, 2}, 2, []byte{1, 12, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'u', 's', 'e', 'r', 16, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'}, []byte{5, 1, 0, 1, 192, 0, 2, 7, 31, 145}},
		{"IPv6 userpass", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, "[2001:db8::7]:8443", []byte{5, 2, 0, 2}, 2, []byte{1, 12, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'u', 's', 'e', 'r', 16, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'}, []byte{5, 1, 0, 4, 32, 1, 13, 184, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7, 32, 251}},
		{"credentials choose no-auth", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, testTarget, []byte{5, 2, 0, 2}, 0, nil, []byte{5, 1, 0, 3, 25, 'u', 'n', 'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', '.', 'i', 'n', 'v', 'a', 'l', 'i', 'd', 1, 187}},
		{"username only", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser}, testTarget, []byte{5, 2, 0, 2}, 2, []byte{1, 12, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'u', 's', 'e', 'r', 0}, []byte{5, 1, 0, 3, 25, 'u', 'n', 'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', '.', 'i', 'n', 'v', 'a', 'l', 'i', 'd', 1, 187}},
		{"password only", Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Password: testPassword}, testTarget, []byte{5, 2, 0, 2}, 2, []byte{1, 0, 16, 'p', 'r', 'i', 'v', 'a', 't', 'e', '-', 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'}, []byte{5, 1, 0, 3, 25, 'u', 'n', 'r', 'e', 's', 'o', 'l', 'v', 'e', 'd', '-', 't', 'a', 'r', 'g', 'e', 't', '.', 'i', 'n', 'v', 'a', 'l', 'i', 'd', 1, 187}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			peer := peerResult()
			go func() {
				got, err := readExact(server, len(tc.greeting))
				if err == nil {
					checkWire(t, got, tc.greeting)
					_, err = server.Write([]byte{5, tc.method})
				}
				if err == nil && len(tc.auth) != 0 {
					got, err = readExact(server, len(tc.auth))
					if err == nil {
						checkWire(t, got, tc.auth)
						_, err = server.Write([]byte{1, 0})
					}
				}
				if err == nil {
					got, err = readExact(server, len(tc.request))
					if err == nil {
						checkWire(t, got, tc.request)
						_, err = server.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
					}
				}
				if err == nil {
					_, err = server.Write([]byte("welcome"))
				}
				if err == nil {
					var echo [4]byte
					_, err = io.ReadFull(server, echo[:])
					if err == nil {
						_, err = server.Write(echo[:])
					}
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			tunnel, err := Handshake(ctx, client, tc.proxy, tc.target)
			if err != nil {
				_ = checkProxyError(t, err, StageHandshake, 0)
				t.Fatal("unexpected handshake failure")
			}
			marker, err := readExact(tunnel, 7)
			if err != nil {
				t.Fatal("could not read tunnel marker")
			}
			checkWire(t, marker, []byte("welcome"))
			if _, err = tunnel.Write([]byte("echo")); err != nil {
				t.Fatal("could not write tunnel payload")
			}
			echo, err := readExact(tunnel, 4)
			if err != nil {
				t.Fatal("could not read tunnel echo")
			}
			checkWire(t, echo, []byte("echo"))
			waitErr(t, peer)
		})
	}
}

func TestSOCKS5CancellationAndDeadlineAbort(t *testing.T) {
	cases := []struct {
		name        string
		newContext  func() (context.Context, context.CancelFunc, error)
		wantTimeout bool
	}{
		{"cancel", func() (context.Context, context.CancelFunc, error) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, context.Canceled
		}, false},
		{"deadline", func() (context.Context, context.CancelFunc, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			return ctx, cancel, context.DeadlineExceeded
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			ctx, cancel, wantCause := tc.newContext()
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{}, 1)
			t.Cleanup(func() {
				select {
				case release <- struct{}{}:
				default:
				}
			})
			peer := peerResult()
			peerJoined := false
			go func() {
				_ = server.SetDeadline(time.Now().Add(3 * time.Second))
				_, peerErr := readExact(server, 3)
				if peerErr == nil {
					close(started)
					<-release
				}
				peerDone(peer, peerErr)
			}()
			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, handshakeErr := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget)
				result <- handshakeErr
			}()
			releasePeer := func() {
				select {
				case release <- struct{}{}:
				default:
				}
			}
			t.Cleanup(func() {
				cancel()
				releasePeer()
				_ = client.Close()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Errorf("SOCKS cancellation handshake did not stop after cleanup")
				}
				if !peerJoined {
					select {
					case <-peer:
					case <-time.After(2 * time.Second):
						t.Errorf("SOCKS cancellation peer did not stop after cleanup")
					}
				}
			})
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("SOCKS greeting was not read")
			}
			if tc.name == "cancel" {
				cancel()
			} else {
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("SOCKS context deadline did not fire")
				}
			}
			cancelledAt := time.Now()
			var handshakeErr error
			select {
			case handshakeErr = <-result:
			case <-time.After(2 * time.Second):
				_ = client.Close()
				t.Fatal("SOCKS handshake did not abort")
			}
			pe := checkProxyError(t, handshakeErr, StageHandshake, 0)
			if tc.name == "cancel" && (pe.Timeout() || errors.Is(handshakeErr, context.DeadlineExceeded)) {
				t.Fatal("SOCKS cancellation was reported as a timeout")
			}
			if !errors.Is(pe, wantCause) {
				t.Fatal("SOCKS cancellation cause was not preserved")
			}
			if time.Since(cancelledAt) >= 2*time.Second {
				t.Fatal("SOCKS cancellation did not abort promptly")
			}
			if pe.Timeout() != tc.wantTimeout || (tc.name == "deadline" && !errors.Is(handshakeErr, context.DeadlineExceeded)) {
				t.Fatal("SOCKS cancellation timeout classification mismatch")
			}
			select {
			case release <- struct{}{}:
			default:
			}
			waitErr(t, peer)
			peerJoined = true
		})
	}
}

func TestSOCKS5SuccessfulBoundAddressTypesPreserveMarker(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
	}{
		{"IPv4", []byte{5, 0, 0, 1, 127, 0, 0, 1, 0x12, 0x34}},
		{"domain", []byte{5, 0, 0, 3, 3, 'b', 'n', 'd', 0x12, 0x34}},
		{"IPv6", []byte{5, 0, 0, 4, 0x20, 1, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x12, 0x34}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			peer := peerResult()
			go func() {
				_, err := readExact(server, 3)
				if err == nil {
					_, err = server.Write([]byte{5, 0})
				}
				if err == nil {
					_, err = readExact(server, 32)
				}
				if err == nil {
					_, err = server.Write(tc.reply)
				}
				if err == nil {
					_, err = server.Write([]byte("marker"))
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			tunnel, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget)
			if err != nil {
				_ = checkProxyError(t, err, StageHandshake, 0)
				t.Fatal("unexpected handshake failure")
			}
			marker, err := readExact(tunnel, 6)
			if err != nil {
				t.Fatal("could not read SOCKS tunnel marker")
			}
			checkWire(t, marker, []byte("marker"))
			waitErr(t, peer)
		})
	}
}

func TestSOCKS5ReplyCodesAndConsumption(t *testing.T) {
	cases := []struct {
		rep   byte
		stage Stage
	}{
		{1, StageOther}, {2, StageRefused}, {3, StageTarget}, {4, StageTarget}, {5, StageTarget}, {6, StageTarget}, {7, StageOther}, {8, StageOther}, {9, StageOther},
	}
	for _, tc := range cases {
		t.Run("reply", func(t *testing.T) {
			clientRaw, server := pipePair(t)
			client := &readCounterConn{Conn: clientRaw}
			peer := peerResult()
			go func() {
				_, err := readExact(server, 3)
				if err == nil {
					_, err = server.Write([]byte{5, 0})
				}
				if err == nil {
					_, err = readExact(server, 32)
				}
				if err == nil {
					_, err = server.Write([]byte{5, tc.rep, 0, 3, 4, 'n', 'o', 'w', 'a', 0x12, 0x34})
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget)
			_ = checkProxyError(t, err, tc.stage, int(tc.rep))
			waitErr(t, peer)
			if client.readBytes != 13 {
				t.Fatal("SOCKS reply address or port was not fully consumed")
			}
		})
	}
}

func TestSOCKS5RejectedMethodsAndAuthentication(t *testing.T) {
	cases := []struct {
		name      string
		response  []byte
		authReply []byte
		proxy     Proxy
		stage     Stage
	}{
		{"no acceptable method", []byte{5, 255}, nil, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, StageRefused},
		{"wrong greeting version", []byte{4, 0}, nil, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, StageHandshake},
		{"unoffered method", []byte{5, 2}, nil, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, StageHandshake},
		{"unsupported method", []byte{5, 1}, nil, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, StageHandshake},
		{"auth refused", []byte{5, 2}, []byte{1, 1}, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, StageRefused},
		{"wrong auth version", []byte{5, 2}, []byte{2, 0}, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, StageHandshake},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			peer := peerResult()
			peerJoined := false
			started := time.Now()
			go func() {
				_ = server.SetDeadline(time.Now().Add(3 * time.Second))
				greetingLen := 4
				if tc.name == "no acceptable method" || tc.name == "wrong greeting version" || tc.name == "unoffered method" || tc.name == "unsupported method" {
					greetingLen = 3
				}
				_, err := readExact(server, greetingLen)
				if err == nil {
					_, err = server.Write(tc.response)
				}
				if err == nil && tc.authReply != nil {
					_, err = readExact(server, 31)
					if err == nil {
						_, err = server.Write(tc.authReply)
					}
				}
				if err == nil {
					_ = server.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
					var unexpected [1]byte
					n, readErr := server.Read(unexpected[:])
					if n != 0 {
						err = errors.New("client sent data after rejected SOCKS reply")
					} else if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
						err = nil
					} else if errors.Is(readErr, io.EOF) || errors.Is(readErr, net.ErrClosed) {
						err = nil
					} else if readErr != nil {
						err = readErr
					} else {
						err = errors.New("unexpected zero-byte peer read")
					}
				}
				peerDone(peer, err)
			}()
			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := Handshake(ctx, client, tc.proxy, testTarget)
				result <- err
			}()
			returned := false
			t.Cleanup(func() {
				cancel()
				_ = client.Close()
				if !returned {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Errorf("SOCKS handshake goroutine did not stop after cleanup")
					}
				}
				if !peerJoined {
					select {
					case <-peer:
					case <-time.After(2 * time.Second):
						t.Errorf("SOCKS peer routine did not stop after cleanup")
					}
				}
			})
			var handshakeErr error
			select {
			case handshakeErr = <-result:
				returned = true
			case <-time.After(2 * time.Second):
				_ = client.Close()
				t.Fatal("rejected SOCKS method did not fail promptly")
			}
			if elapsed := time.Since(started); elapsed >= 2*time.Second {
				t.Fatal("rejected SOCKS method took too long")
			}
			pe := checkProxyError(t, handshakeErr, tc.stage, 0)
			if pe.Timeout() || errors.Is(handshakeErr, context.DeadlineExceeded) {
				t.Fatal("rejected SOCKS method was reported as a timeout")
			}
			_ = client.Close()
			waitErr(t, peer)
			peerJoined = true
		})
	}
}

type readAheadConn struct {
	net.Conn
	pending []byte
}

func (c *readAheadConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		var buffer [4096]byte
		n, err := c.Conn.Read(buffer[:])
		if n > 0 {
			c.pending = append(c.pending[:0], buffer[:n]...)
		}
		if n == 0 {
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func TestSOCKS5MalformedAndTruncatedReplies(t *testing.T) {
	cases := []struct {
		name            string
		greeting, reply []byte
		wantTruncation  error
	}{
		{"truncated greeting", nil, nil, io.EOF},
		{"short greeting reply", []byte{5}, nil, io.ErrUnexpectedEOF},
		{"truncated response header", []byte{5, 0}, []byte{5, 0}, io.ErrUnexpectedEOF},
		{"bad response version", []byte{5, 0}, []byte{4, 0, 0, 1, 192, 0, 2, 1, 0, 80}, nil},
		{"bad reserved", []byte{5, 0}, []byte{5, 0, 1, 1, 192, 0, 2, 1, 0, 80}, nil},
		{"bad address type", []byte{5, 0}, []byte{5, 0, 0, 9, 0, 80}, nil},
		{"truncated domain length", []byte{5, 0}, []byte{5, 0, 0, 3}, io.EOF},
		{"empty domain", []byte{5, 0}, []byte{5, 0, 0, 3, 0, 0, 80}, nil},
		{"truncated address", []byte{5, 0}, []byte{5, 0, 0, 1, 192, 0}, io.EOF},
		{"truncated port", []byte{5, 0}, []byte{5, 0, 0, 1, 192, 0, 2, 1, 0}, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientRaw, server := pipePair(t)
			tracked := &closeTrackingConn{Conn: clientRaw}
			client := &readAheadConn{Conn: tracked}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			peer := peerResult()
			peerJoined := false
			t.Cleanup(func() {
				cancel()
				_ = tracked.Close()
				if !peerJoined {
					select {
					case <-peer:
					case <-time.After(2 * time.Second):
						t.Errorf("SOCKS malformed-reply peer did not stop after cleanup")
					}
				}
			})
			go func() {
				_ = server.SetDeadline(time.Now().Add(3 * time.Second))
				_, peerErr := readExact(server, 3)
				if peerErr == nil && tc.greeting != nil {
					_ = server.SetWriteDeadline(time.Now().Add(1 * time.Second))
					peerErr = writeAll(server, tc.greeting)
				}
				if peerErr == nil && len(tc.greeting) == 2 {
					_, peerErr = readExact(server, 32)
					if peerErr == nil {
						_ = server.SetWriteDeadline(time.Now().Add(1 * time.Second))
						peerErr = writeAll(server, tc.reply)
					}
				}
				_ = server.Close()
				if errors.Is(peerErr, net.ErrClosed) || errors.Is(peerErr, io.ErrClosedPipe) {
					peerErr = nil
				}
				peerDone(peer, peerErr)
			}()
			started := time.Now()
			_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080}, testTarget)
			if elapsed := time.Since(started); elapsed >= 2*time.Second {
				t.Fatal("malformed SOCKS reply did not fail promptly")
			}
			pe := checkProxyError(t, err, StageHandshake, 0)
			if pe.Timeout() || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("malformed SOCKS reply was reported as a timeout")
			}
			if tc.wantTruncation == nil {
				if !errors.Is(err, errInvalidProxy) {
					t.Fatal("malformed SOCKS reply did not report protocol validation")
				}
			} else if !errors.Is(err, tc.wantTruncation) {
				t.Fatal("truncated SOCKS reply did not preserve its EOF classification")
			}
			if tracked.closed {
				t.Fatal("Handshake closed its input on protocol failure")
			}
			if tc.wantTruncation != nil {
				_ = tracked.Close()
			}
			waitErr(t, peer)
			peerJoined = true
		})
	}
}

func TestSOCKS5TruncatedAuthResponse(t *testing.T) {
	client, server := pipePair(t)
	peer := peerResult()
	peerJoined := false
	t.Cleanup(func() {
		_ = client.Close()
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("truncated-auth peer did not stop after cleanup")
			}
		}
	})
	go func() {
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		_, err := readExact(server, 4)
		if err == nil {
			_, err = server.Write([]byte{5, 2})
		}
		if err == nil {
			_, err = readExact(server, 31)
		}
		if err == nil {
			_, err = server.Write([]byte{1})
		}
		_ = server.Close()
		peerDone(peer, err)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := Handshake(ctx, client, Proxy{Scheme: "socks5", Host: "proxy", Port: 1080, Username: testUser, Password: testPassword}, testTarget)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatal("truncated SOCKS auth response did not fail promptly")
	}
	pe := checkProxyError(t, err, StageHandshake, 0)
	if !errors.Is(err, io.ErrUnexpectedEOF) || pe.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("truncated SOCKS auth response error classification mismatch")
	}
	waitErr(t, peer)
	peerJoined = true
}
