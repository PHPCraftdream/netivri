package proxyclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeDialer struct {
	conn    net.Conn
	err     error
	network string
	address string
	calls   int
}

func (d *fakeDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.network, d.address, d.calls = network, address, d.calls+1
	return d.conn, d.err
}

type deadlineConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
}

func (c *deadlineConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineConn) deadlineSnapshot() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.deadlines...)
}

type cancelOnDeadlineClearConn struct {
	net.Conn
	cancel context.CancelFunc
	mu     sync.Mutex
	clears int
}

func (c *cancelOnDeadlineClearConn) SetDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		c.mu.Lock()
		c.clears++
		cancel := c.clears == 2
		c.mu.Unlock()
		if cancel {
			c.cancel()
		}
	}
	return c.Conn.SetDeadline(deadline)
}

type tlsVerificationFailureConn struct {
	net.Conn
	readErr error
}

func (c *tlsVerificationFailureConn) Read([]byte) (int, error)    { return 0, c.readErr }
func (c *tlsVerificationFailureConn) Write(p []byte) (int, error) { return len(p), nil }

type callbackBlockingConn struct {
	net.Conn
	entered                 chan struct{}
	readStarted             chan struct{}
	readDone                chan struct{}
	callbackSetDeadlineDone chan struct{}
	earlyClear              chan struct{}
	release                 <-chan struct{}
	once                    sync.Once
	readOnce                sync.Once
	readDoneOnce            sync.Once
	callbackDoneOnce        sync.Once
	earlyClearOnce          sync.Once
	mu                      sync.Mutex
	calls                   []time.Time
}

func (c *callbackBlockingConn) Read(p []byte) (int, error) {
	if c.readStarted != nil {
		c.readOnce.Do(func() { close(c.readStarted) })
	}
	n, err := c.Conn.Read(p)
	if c.readDone != nil {
		c.readDoneOnce.Do(func() { close(c.readDone) })
	}
	return n, err
}

func (c *callbackBlockingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.calls = append(c.calls, deadline)
	c.mu.Unlock()
	if !deadline.IsZero() {
		c.once.Do(func() {
			close(c.entered)
			<-c.release
		})
	} else if c.earlyClear != nil {
		select {
		case <-c.entered:
			select {
			case <-c.callbackSetDeadlineDone:
			default:
				c.earlyClearOnce.Do(func() { close(c.earlyClear) })
				return errors.New("deadline cleared before cancellation callback completed")
			}
		default:
		}
	}
	err := c.Conn.SetDeadline(deadline)
	if !deadline.IsZero() && c.callbackSetDeadlineDone != nil {
		c.callbackDoneOnce.Do(func() { close(c.callbackSetDeadlineDone) })
	}
	return err
}

func (c *callbackBlockingConn) lastDeadline() (time.Time, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return time.Time{}, 0
	}
	return c.calls[len(c.calls)-1], len(c.calls)
}

func TestDialUsesProxyAddressAndReturnsTunnel(t *testing.T) {
	client, server := pipePair(t)
	dialer := &fakeDialer{conn: client}
	peer := peerResult()
	go func() {
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		got, err := readExact(server, len(request))
		if err == nil {
			checkWire(t, got, []byte(request))
			_, err = server.Write([]byte("HTTP/1.1 200 OK\r\n\r\nready"))
		}
		if err == nil {
			var b [3]byte
			_, err = server.Read(b[:])
			if err == nil {
				_, err = server.Write(b[:])
			}
		}
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	tunnel, err := Dial(ctx, dialer, Proxy{Scheme: "http", Host: "proxy.example", Port: 8080}, testTarget)
	if err != nil {
		_ = checkProxyError(t, err, StageHandshake, 0)
		t.Fatal("unexpected handshake failure")
	}
	if dialer.calls != 1 || dialer.network != "tcp" || dialer.address != "proxy.example:8080" {
		t.Fatal("Dial did not use the proxy address")
	}
	marker, err := readExact(tunnel, 5)
	if err != nil {
		t.Fatal("could not read dialed tunnel marker")
	}
	checkWire(t, marker, []byte("ready"))
	if _, err := tunnel.Write([]byte("abc")); err != nil {
		t.Fatal("could not use dialed tunnel")
	}
	echo, err := readExact(tunnel, 3)
	if err != nil {
		t.Fatal("could not read dialed tunnel echo")
	}
	checkWire(t, echo, []byte("abc"))
	waitErr(t, peer)
}

func TestDialValidationDoesNotCallDialer(t *testing.T) {
	d := &fakeDialer{}
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy", Port: 80}, "invalid")
	_ = checkProxyError(t, err, StageOther, 0)
	if d.calls != 0 {
		t.Fatal("Dial called dialer for invalid target")
	}
	if _, err := Dial(ctx, nil, Proxy{}, testTarget); err == nil {
		t.Fatal("Dial accepted a nil dialer")
	} else {
		_ = checkProxyError(t, err, StageOther, 0)
	}
	if d.calls != 0 {
		t.Fatal("Dial called nil dialer")
	}
}

func TestDialConnectionFailureRedactsCause(t *testing.T) {
	cause := errors.New("dialed " + testUser + ":" + testPassword + " for " + testTarget)
	d := &fakeDialer{err: cause}
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy.example", Port: 81, Username: testUser, Password: testPassword}, testTarget)
	pe := checkProxyError(t, err, StageConnect, 0)
	if !errors.Is(pe, cause) {
		t.Fatal("Dial error did not retain the underlying cause")
	}
	if d.address != "proxy.example:81" || d.network != "tcp" || d.calls != 1 {
		t.Fatal("Dial used an unexpected proxy address")
	}
}

func TestDialClosesNonNilConnectionReturnedWithError(t *testing.T) {
	clientRaw, server := rawPipePair(t)
	client := &closeTrackingConn{Conn: clientRaw}
	defer func() { _ = server.Close() }()
	d := &fakeDialer{conn: client, err: errors.New("dial error")}
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
	_ = checkProxyError(t, err, StageConnect, 0)
	if !client.closed {
		t.Fatal("Dial did not close the non-nil connection returned with error")
	}
}

func TestDialClosesConnectionAfterHandshakeFailure(t *testing.T) {
	client, server := rawPipePair(t)
	defer func() { _ = client.Close() }()
	d := &fakeDialer{conn: client}
	peer := peerResult()
	peerJoined := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("malformed-response peer did not stop after cleanup")
			}
		}
	})
	go func() {
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		if err == nil {
			_, err = server.Write([]byte("bad status\r\n"))
		}
		if err == nil {
			var b [1]byte
			_, err = server.Read(b[:])
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				peerDone(peer, nil)
				return
			}
			if err == nil {
				err = errors.New("connection remained open")
			}
			peerDone(peer, err)
			return
		}
		peerDone(peer, err)
	}()
	started := time.Now()
	_, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatal("malformed HTTP response did not fail promptly")
	}
	pe := checkProxyError(t, err, StageHandshake, 0)
	if pe.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("malformed HTTP response was reported as a timeout")
	}
	waitErr(t, peer)
	peerJoined = true
}

func TestHandshakeCancellationAndDeadlineAbortHTTP(t *testing.T) {
	testCases := []struct {
		name        string
		newContext  func() (context.Context, context.CancelFunc, error)
		wantTimeout bool
	}{
		{name: "cancel", newContext: func() (context.Context, context.CancelFunc, error) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, context.Canceled
		}, wantTimeout: false},
		{name: "deadline", newContext: func() (context.Context, context.CancelFunc, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			return ctx, cancel, context.DeadlineExceeded
		}, wantTimeout: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			tracked := &deadlineConn{Conn: client}
			ctx, cancel, wantCause := tc.newContext()
			defer cancel()
			started := make(chan struct{})
			release := make(chan struct{}, 1)
			releasePeer := func() {
				select {
				case release <- struct{}{}:
				default:
				}
			}
			t.Cleanup(releasePeer)
			peer := peerResult()
			peerJoined := false
			go func() {
				_ = server.SetDeadline(time.Now().Add(3 * time.Second))
				request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
				_, err := readExact(server, len(request))
				if err == nil {
					close(started)
					<-release
				}
				peerDone(peer, err)
			}()
			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, err := Handshake(ctx, tracked, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
				result <- err
			}()
			cleanupDone := false
			t.Cleanup(func() {
				cancel()
				releasePeer()
				_ = client.Close()
				if !cleanupDone {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Errorf("HTTP handshake goroutine did not stop after cleanup")
					}
				}
				if !peerJoined {
					select {
					case <-peer:
					case <-time.After(2 * time.Second):
						t.Errorf("HTTP cancellation peer did not stop after cleanup")
					}
				}
			})
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP handshake did not start")
			}
			var abortStarted time.Time
			if tc.name == "cancel" {
				abortStarted = time.Now()
				cancel()
			} else {
				select {
				case <-ctx.Done():
					abortStarted = time.Now()
				case <-time.After(3 * time.Second):
					t.Fatal("context deadline did not fire")
				}
			}
			var err error
			select {
			case err = <-result:
				cleanupDone = true
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP handshake did not abort")
			}
			pe := checkProxyError(t, err, StageHandshake, 0)
			if !errors.Is(pe, wantCause) {
				t.Fatal("HTTP cancellation cause was not preserved")
			}
			if pe.Timeout() != tc.wantTimeout || (tc.name == "deadline" && !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatal("HTTP cancellation timeout classification mismatch")
			}
			if tc.name == "cancel" && errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("HTTP cancellation was reported as a deadline")
			}
			if time.Since(abortStarted) >= 2*time.Second {
				t.Fatal("HTTP cancellation did not abort promptly")
			}
			releasePeer()
			waitErr(t, peer)
			peerJoined = true
			deadlines := tracked.deadlineSnapshot()
			if len(deadlines) < 2 || !deadlines[len(deadlines)-1].IsZero() {
				t.Fatal("Handshake did not clear its connection deadline")
			}
			deadline, ok := ctx.Deadline()
			if tc.wantTimeout {
				if !ok || !deadlines[0].Equal(deadline) {
					t.Fatal("Handshake did not apply the context deadline")
				}
			} else if !deadlines[0].IsZero() {
				t.Fatal("Handshake installed a deadline without a context deadline")
			}
		})
	}
}

func TestHandshakeWaitsForCancellationDeadlineCallback(t *testing.T) {
	client, server := rawPipePair(t)
	defer func() { _ = client.Close() }()
	release := make(chan struct{}, 1)
	releaseCallback := func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}
	t.Cleanup(releaseCallback)
	tracked := &callbackBlockingConn{
		Conn:                    client,
		entered:                 make(chan struct{}),
		readStarted:             make(chan struct{}),
		readDone:                make(chan struct{}),
		callbackSetDeadlineDone: make(chan struct{}),
		earlyClear:              make(chan struct{}),
		release:                 release,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestRead := make(chan struct{})
	responseReady := make(chan struct{})
	var responseReadyOnce sync.Once
	releaseResponse := func() { responseReadyOnce.Do(func() { close(responseReady) }) }
	peer := peerResult()
	peerJoined := false
	done := make(chan struct{})
	go func() {
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		if err == nil {
			close(requestRead)
			<-responseReady
			_ = server.SetWriteDeadline(time.Now().Add(1 * time.Second))
			_, err = server.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		}
		peerDone(peer, err)
	}()
	result := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := Handshake(ctx, tracked, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
		result <- err
	}()
	t.Cleanup(func() {
		releaseResponse()
		releaseCallback()
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("callback-interleaving handshake did not stop after cleanup")
		}
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("callback-interleaving peer did not stop after cleanup")
			}
		}
	})
	select {
	case <-requestRead:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request was not read")
	}
	select {
	case <-tracked.readStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP response read did not start")
	}
	cancel()
	select {
	case <-tracked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation deadline callback did not start")
	}
	releaseResponse()
	select {
	case <-tracked.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("complete HTTP response did not finish the handshake read")
	}
	waitErr(t, peer)
	peerJoined = true
	select {
	case <-tracked.earlyClear:
		t.Fatal("Handshake attempted deadline cleanup before cancellation callback completed")
	case err := <-result:
		t.Fatalf("Handshake returned before cancellation deadline callback completed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	releaseCallback()
	select {
	case <-tracked.callbackSetDeadlineDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation callback did not complete SetDeadline")
	}
	select {
	case <-tracked.earlyClear:
		t.Fatal("Handshake attempted deadline cleanup before cancellation callback completed")
	default:
	}
	select {
	case err := <-result:
		pe := checkProxyError(t, err, StageHandshake, 0)
		if !errors.Is(pe, context.Canceled) || pe.Timeout() {
			t.Fatal("cancellation cause or timeout classification mismatch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Handshake did not finish after releasing deadline callback")
	}
	last, count := tracked.lastDeadline()
	if count < 3 || !last.IsZero() {
		t.Fatal("Handshake did not clear deadline after callback completed")
	}
}

func TestDialSuccessfulConnectionSurvivesContextCancellation(t *testing.T) {
	client, server := pipePair(t)
	d := &fakeDialer{conn: client}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := peerResult()
	peerFinished := false
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = server.Close()
		if !peerFinished {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("successful dial peer did not stop after cleanup")
			}
		}
	})
	go func() {
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		if err == nil {
			_, err = server.Write([]byte("HTTP/1.1 200 OK\r\n\r\nhello"))
		}
		if err == nil {
			var b [3]byte
			_, err = server.Read(b[:])
			if err == nil {
				_, err = server.Write(b[:])
			}
		}
		peerDone(peer, err)
	}()
	tunnel, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
	if err != nil {
		_ = checkProxyError(t, err, StageHandshake, 0)
		t.Fatal("unexpected handshake failure")
	}
	cancel()
	marker, err := readExact(tunnel, 5)
	if err != nil {
		t.Fatal("context cancellation poisoned returned connection")
	}
	checkWire(t, marker, []byte("hello"))
	if _, err := tunnel.Write([]byte("abc")); err != nil {
		t.Fatal("context cancellation poisoned returned connection")
	}
	echo, err := readExact(tunnel, 3)
	if err != nil {
		t.Fatal("returned connection stopped working after cancellation")
	}
	checkWire(t, echo, []byte("abc"))
	waitErr(t, peer)
	peerFinished = true
}

func TestHandshakePreservesTLSVerificationFailureWhenCleanupCancels(t *testing.T) {
	clientRaw, serverRaw := rawPipePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	tracked := &cancelOnDeadlineClearConn{Conn: clientRaw, cancel: cancel}
	verificationErr := &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}
	conn := &tlsVerificationFailureConn{Conn: tracked, readErr: verificationErr}
	t.Cleanup(func() {
		cancel()
		_ = clientRaw.Close()
		_ = serverRaw.Close()
	})

	_, err := Handshake(ctx, conn, Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, testTarget)
	pe := checkProxyError(t, err, StageConnect, 0)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(pe, &unknownAuthority) {
		t.Fatal("TLS verification failure was replaced during cleanup")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("cleanup did not cancel the context")
	}
	if pe.Timeout() || errors.Is(err, context.Canceled) {
		t.Fatal("TLS verification failure was classified as a context failure")
	}
}

func TestHandshakePreservesCompletedProtocolFailureWhenCleanupCancels(t *testing.T) {
	clientRaw, server := rawPipePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	tracked := &cancelOnDeadlineClearConn{Conn: clientRaw, cancel: cancel}
	peer := peerResult()
	peerJoined := false
	go func() {
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		if err == nil {
			_, err = server.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
		}
		peerDone(peer, err)
	}()
	t.Cleanup(func() {
		cancel()
		_ = clientRaw.Close()
		_ = server.Close()
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Error("HTTP protocol peer did not stop after cleanup")
			}
		}
	})

	_, err := Handshake(ctx, tracked, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
	pe := checkProxyError(t, err, StageRefused, 407)
	if ctx.Err() != context.Canceled {
		t.Fatal("cleanup did not cancel the context")
	}
	if pe.Timeout() || errors.Is(err, context.Canceled) {
		t.Fatal("completed HTTP refusal was replaced by a context failure")
	}
	waitErr(t, peer)
	peerJoined = true
}

func TestDialTLSAndProtocolFailureCloseConnection(t *testing.T) {
	client, server := rawPipePair(t)
	defer func() { _ = client.Close() }()
	d := &fakeDialer{conn: client}
	peer := peerResult()
	peerJoined := false
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("TLS protocol peer did not stop after cleanup")
			}
		}
	})
	go func() {
		var recordHeader [5]byte
		err := func() error {
			_ = server.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(server, recordHeader[:]); err != nil {
				return err
			}
			if recordHeader[0] != 22 {
				return errors.New("TLS record not observed")
			}
			bodyLen := int(recordHeader[3])<<8 | int(recordHeader[4])
			if bodyLen == 0 {
				return errors.New("empty TLS handshake record")
			}
			body := make([]byte, bodyLen)
			if _, err := io.ReadFull(server, body); err != nil {
				return err
			}
			if _, err := server.Write([]byte{21, 3, 3, 0, 2, 2, 40}); err != nil {
				return err
			}
			_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
			var one [1]byte
			_, err := server.Read(one[:])
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err == nil {
				return errors.New("connection remained open")
			}
			return err
		}()
		peerDone(peer, err)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := Dial(ctx, d, Proxy{Scheme: "https", Host: "proxy.example", Port: 443}, testTarget)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatal("TLS protocol failure did not fail promptly")
	}
	if err == nil {
		t.Fatal("TLS protocol failure unexpectedly succeeded")
	}
	pe := checkProxyError(t, err, StageConnect, 0)
	if pe.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("TLS protocol failure was classified as a timeout")
	}
	waitErr(t, peer)
	peerJoined = true
}

func TestDialClosesConnectionOnContextCancellationAfterDial(t *testing.T) {
	client, server := rawPipePair(t)
	defer func() { _ = client.Close() }()
	started := make(chan struct{})
	d := &cancelDialer{conn: client, started: started}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal("could not bound cancellation peer read")
	}
	peer := peerResult()
	peerJoined := false
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("Dial cancellation handshake did not stop after cleanup")
		}
		if !peerJoined {
			select {
			case <-peer:
			case <-time.After(2 * time.Second):
				t.Errorf("dial-after-cancel peer did not stop after cleanup")
			}
		}
	})
	go func() {
		<-started
		var one [1]byte
		_, err := server.Read(one[:])
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			peerDone(peer, nil)
		} else if err != nil {
			peerDone(peer, err)
		} else {
			peerDone(peer, errors.New("connection remained open after Dial cancellation"))
		}
	}()
	result := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := Dial(ctx, d, Proxy{Scheme: "http", Host: "proxy", Port: 80}, testTarget)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dialer did not start")
	}
	startedAt := time.Now()
	cancel()
	select {
	case err := <-result:
		pe := checkProxyError(t, err, StageConnect, 0)
		if !errors.Is(pe, context.Canceled) || pe.Timeout() {
			t.Fatal("Dial cancellation cause or timeout classification mismatch")
		}
		if time.Since(startedAt) >= 2*time.Second {
			t.Fatal("Dial did not abort promptly after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dial did not abort after context cancellation")
	}
	waitErr(t, peer)
	peerJoined = true
}

type cancelDialer struct {
	conn    net.Conn
	started chan struct{}
}

func (d *cancelDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	close(d.started)
	<-ctx.Done()
	return d.conn, nil
}
