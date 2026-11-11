package proxyclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type simultaneousReadConn struct {
	net.Conn
	mu       sync.Mutex
	enabled  bool
	entered  int
	released bool
	overlap  bool
	both     chan struct{}
}

func (c *simultaneousReadConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if !c.enabled {
		c.mu.Unlock()
		return c.Conn.Read(p)
	}
	c.entered++
	if c.entered == 2 && !c.released {
		c.overlap = true
		c.released = true
		close(c.both)
	}
	released := c.released
	both := c.both
	c.mu.Unlock()
	if !released {
		select {
		case <-both:
		case <-time.After(2 * time.Second):
			c.mu.Lock()
			if !c.released {
				c.released = true
				close(c.both)
			}
			c.mu.Unlock()
		}
	}
	return c.Conn.Read(p)
}

func (c *simultaneousReadConn) enable() {
	c.mu.Lock()
	c.enabled = true
	c.mu.Unlock()
}

func (c *simultaneousReadConn) overlapped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overlap
}

func TestHTTPConnectExactRequestAndTunnel(t *testing.T) {
	for _, auth := range []bool{false, true} {
		t.Run(map[bool]string{false: "no auth", true: "basic auth"}[auth], func(t *testing.T) {
			client, server := pipePair(t)
			proxy := Proxy{Scheme: "http", Host: "proxy", Port: 8080}
			want := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
			if auth {
				proxy.Username, proxy.Password = testUser, testPassword
				want = "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\nProxy-Authorization: Basic cHJpdmF0ZS11c2VyOnByaXZhdGUtcGFzc3dvcmQ=\r\n\r\n"
			}
			peer := peerResult()
			go func() {
				got, err := readExact(server, len(want))
				if err == nil {
					checkWire(t, got, []byte(want))
					_, err = server.Write([]byte("HTTP/1.1 200 Connection Established\r\nProxy-Agent: fake\r\n\r\nhello"))
				}
				if err == nil {
					payload := make([]byte, 20000)
					for i := range payload {
						payload[i] = byte(i*31 + 7)
					}
					_, err = server.Write(payload)
					if err == nil {
						echo := make([]byte, len(payload))
						_, err = io.ReadFull(server, echo)
						if err == nil {
							for i := range payload {
								if echo[i] != payload[i] {
									err = io.ErrUnexpectedEOF
									break
								}
							}
						}
						if err == nil {
							_, err = server.Write([]byte("done"))
						}
					}
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			tunnel, err := Handshake(ctx, client, proxy, testTarget)
			if err != nil {
				_ = checkProxyError(t, err, StageHandshake, 0)
				t.Fatal("unexpected handshake failure")
			}
			prefix, err := readExact(tunnel, 5)
			if err != nil {
				t.Fatal("could not read buffered tunnel prefix")
			}
			checkWire(t, prefix, []byte("hello"))
			large := make([]byte, 20000)
			if _, err := io.ReadFull(tunnel, large); err != nil {
				t.Fatal("could not read full later tunnel payload")
			}
			for i := range large {
				if large[i] != byte(i*31+7) {
					t.Fatal("later tunnel payload was corrupted")
				}
			}
			if _, err := tunnel.Write(large); err != nil {
				t.Fatal("could not write large tunnel payload")
			}
			done, err := readExact(tunnel, 4)
			if err != nil {
				t.Fatal("could not read server tunnel response")
			}
			checkWire(t, done, []byte("done"))
			waitErr(t, peer)
		})
	}
}

func TestHTTPConnectBasicAuthOneSidedCredentials(t *testing.T) {
	cases := []struct {
		name  string
		proxy Proxy
		want  string
	}{
		{"username only", Proxy{Scheme: "http", Host: "proxy", Port: 8080, Username: testUser}, "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\nProxy-Authorization: Basic cHJpdmF0ZS11c2VyOg==\r\n\r\n"},
		{"password only", Proxy{Scheme: "http", Host: "proxy", Port: 8080, Password: testPassword}, "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\nProxy-Authorization: Basic OnByaXZhdGUtcGFzc3dvcmQ=\r\n\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := pipePair(t)
			peer := peerResult()
			go func() {
				got, err := readExact(server, len(tc.want))
				if err == nil {
					checkWire(t, got, []byte(tc.want))
					_, err = server.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			_, err := Handshake(ctx, client, tc.proxy, testTarget)
			if err != nil {
				_ = checkProxyError(t, err, StageHandshake, 0)
				t.Fatal("unexpected handshake failure")
			}
			waitErr(t, peer)
		})
	}
}
func TestHTTPConnectSuccessfulStatuses(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		t.Run(statusText(status), func(t *testing.T) {
			client, server := pipePair(t)
			peer := peerResult()
			go func() {
				request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
				_, err := readExact(server, len(request))
				if err == nil {
					response := "HTTP/1.1 " + statusText(status) + "\r\nX-Test: value\r\n\r\nmarker"
					_, err = server.Write([]byte(response))
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			tunnel, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget)
			if err != nil {
				_ = checkProxyError(t, err, StageHandshake, 0)
				t.Fatal("unexpected successful status failure")
			}
			marker, err := readExact(tunnel, len("marker"))
			if err != nil {
				t.Fatal("could not read successful CONNECT payload")
			}
			checkWire(t, marker, []byte("marker"))
			waitErr(t, peer)
		})
	}
}

func TestHTTPConnectResponseClassifications(t *testing.T) {
	cases := []struct {
		status int
		stage  Stage
	}{
		{403, StageRefused}, {407, StageRefused}, {502, StageTarget}, {503, StageTarget}, {504, StageTarget},
		{100, StageOther}, {300, StageOther}, {301, StageOther}, {500, StageOther},
	}
	for _, tc := range cases {
		t.Run("status", func(t *testing.T) {
			client, server := pipePair(t)
			peer := peerResult()
			go func() {
				request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
				_, err := readExact(server, len(request))
				if err == nil {
					_, err = server.Write([]byte("HTTP/1.1 " + statusText(tc.status) + "\r\nX-Test: value\r\n\r\n"))
				}
				peerDone(peer, err)
			}()
			ctx, cancel := testContext(t)
			defer cancel()
			_, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget)
			_ = checkProxyError(t, err, tc.stage, tc.status)
			waitErr(t, peer)
		})
	}
}

func statusText(status int) string {
	if status < 100 || status > 599 {
		return "000 Invalid"
	}
	return string([]byte{'0' + byte(status/100), '0' + byte(status/10%10), '0' + byte(status%10)}) + " Result"
}

func TestHTTPConnectBufferedPrefixConcurrentReaders(t *testing.T) {
	clientRaw, server := pipePair(t)
	client := &simultaneousReadConn{Conn: clientRaw, both: make(chan struct{})}
	const readerCount = 8
	const bytesPerReader = 32
	const prefixLen = 128
	payload := make([]byte, readerCount*bytesPerReader)
	for i := range payload {
		payload[i] = byte(i)
	}
	peer := peerResult()
	go func() {
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		if err == nil {
			responseAndPrefix := append([]byte("HTTP/1.1 200 OK\r\n\r\n"), payload[:prefixLen]...)
			_, err = server.Write(responseAndPrefix)
		}
		if err == nil {
			_, err = server.Write(payload[prefixLen:])
		}
		peerDone(peer, err)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tunnel, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget)
	if err != nil {
		_ = checkProxyError(t, err, StageHandshake, 0)
		t.Fatal("unexpected handshake failure")
	}
	client.enable()
	if err := clientRaw.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal("could not bound concurrent reader duration")
	}
	if n, err := tunnel.Read(nil); n != 0 || err != nil {
		t.Fatal("zero-length read did not return immediately")
	}

	start := make(chan struct{})
	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, readerCount)
	for i := 0; i < readerCount; i++ {
		go func() {
			data := make([]byte, bytesPerReader)
			<-start
			_, err := io.ReadFull(tunnel, data)
			results <- readResult{data: data, err: err}
		}()
	}
	close(start)
	counts := [256]int{}
	var readerErr error
	for i := 0; i < readerCount; i++ {
		select {
		case result := <-results:
			if result.err != nil && readerErr == nil {
				readerErr = result.err
			}
			for _, value := range result.data {
				counts[value]++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent tunnel readers did not finish")
		}
	}
	if readerErr != nil {
		t.Fatal("concurrent tunnel reader failed")
	}
	for value, count := range counts {
		if count != 1 {
			t.Fatalf("byte value %d delivered %d times, want 1", value, count)
		}
	}
	if !client.overlapped() {
		t.Fatal("concurrent readers did not reach the underlying connection together")
	}
	waitErr(t, peer)
}

func TestParseHTTPStatusCases(t *testing.T) {
	cases := []struct {
		line string
		code int
		ok   bool
	}{
		{"HTTP/1.0 200 OK\r\n", 200, true},
		{"HTTP/1.1 299 Result\r\n", 299, true},
		{"HTTP/1.1 100 Continue\r\n", 100, true},
		{"HTTP/1.1 2X0 Nope\r\n", 0, false},
		{"HTTP/1.1 1:0 Nope\r\n", 0, false},
		{"HTTP/2.0 200 OK\r\n", 0, false},
		{"HTTP/1.1 99 Nope\r\n", 0, false},
		{"HTTP/1.1 600 Nope\r\n", 0, false},
		{"HTTP/1.1 200\r\n", 0, false},
		{"HTTP/1.1 200\n", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseHTTPStatus(tc.line)
		if got != tc.code || ok != tc.ok {
			t.Errorf("parseHTTPStatus(%q) = (%d, %t), want (%d, %t)", tc.line, got, ok, tc.code, tc.ok)
		}
	}
}

func TestValidHTTPHeaderCases(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"X-Test: value\r\n", true},
		{"Content-Type:\ttext/plain\r\n", true},
		{"\r\n", false},
		{" no-fold: value\r\n", false},
		{"X Test: value\r\n", false},
		{"X-Test value\r\n", false},
		{"X-Test: bad\x01value\r\n", false},
		{"X-Test: value\n", false},
	}
	for _, tc := range cases {
		if got := validHTTPHeader(tc.line); got != tc.want {
			t.Errorf("validHTTPHeader(%q) = %t, want %t", tc.line, got, tc.want)
		}
	}
}

func TestReadHTTPLineBoundaries(t *testing.T) {
	maxLine := "X:" + strings.Repeat("a", maxHTTPHeaders-4) + "\r\n"
	overLimit := "X:" + strings.Repeat("a", maxHTTPHeaders-3) + "\r\n"
	cases := []struct {
		name    string
		line    string
		used    int
		want    string
		wantErr error
	}{
		{"line", "X: value\r\n", 0, "X: value\r\n", nil},
		{"lone LF", "X: value\n", 0, "", errInvalidProxy},
		{"exact limit", maxLine, 0, maxLine, nil},
		{"over limit", overLimit, 0, "", errInvalidProxy},
		{"cumulative over limit", "X: value\r\n", maxHTTPHeaders - 1, "", errInvalidProxy},
		{"missing newline", "X: partial", 0, "", io.EOF},
	}
	for _, tc := range cases {
		r := bufio.NewReader(strings.NewReader(tc.line))
		used := tc.used
		got, err := readHTTPLine(r, &used)
		if got != tc.want || !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
			t.Errorf("readHTTPLine(%s) = (%d bytes, %v), want (%d bytes, %v)", tc.name, len(got), err, len(tc.want), tc.wantErr)
		}
	}
}

func TestHTTPConnectMalformedAndOversizedResponses(t *testing.T) {
	cases := []struct {
		name     string
		response string
		wantEOF  bool
	}{
		{"malformed status", "not HTTP\r\nX-Test: value\r\n\r\nmarker", false},
		{"invalid status digits", "HTTP/1.1 1:0 Nope\r\nX-Test: value\r\n\r\nmarker", false},
		{"truncated status", "HTTP/1.1 200", true},
		{"malformed header", "HTTP/1.1 200 OK\r\nno-colon\r\n\r\nmarker", false},
		{"truncated header", "HTTP/1.1 200 OK\r\nX-Test: partial", true},
		{"oversized single line", "HTTP/1.1 200 OK\r\nX-Test: " + strings.Repeat("a", 17000) + "\r\n\r\nmarker", false},
		{"oversized cumulative", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-Test: line\r\n", 1400) + "\r\nmarker", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientRaw, server := pipePair(t)
			client := &closeTrackingConn{Conn: clientRaw}
			peer := peerResult()
			go func() {
				request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
				_, peerErr := readExact(server, len(request))
				if peerErr == nil {
					_, _ = server.Write([]byte(tc.response))
				}
				_ = server.Close()
				peerDone(peer, peerErr)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			started := time.Now()
			_, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget)
			pe := checkProxyError(t, err, StageHandshake, 0)
			if tc.wantEOF {
				if !errors.Is(pe, io.EOF) && !errors.Is(pe, io.ErrUnexpectedEOF) {
					t.Fatal("truncated response did not preserve an EOF cause")
				}
			} else {
				if errors.Is(pe, io.EOF) || errors.Is(pe, io.ErrUnexpectedEOF) {
					t.Fatal("complete malformed response failed due to EOF")
				}
				if time.Since(started) >= 2*time.Second {
					t.Fatal("complete malformed response was not rejected promptly")
				}
			}
			if pe.Timeout() {
				t.Fatal("malformed response was reported as a timeout")
			}
			if client.closed {
				t.Fatal("Handshake closed its input on protocol failure")
			}
			_ = client.Close()
			waitErr(t, peer)
		})
	}
}

func TestHTTPConnectTruncatedAtStart(t *testing.T) {
	client, server := pipePair(t)
	peer := peerResult()
	go func() {
		request := "CONNECT " + testTarget + " HTTP/1.1\r\nHost: " + testTarget + "\r\n\r\n"
		_, err := readExact(server, len(request))
		_ = server.Close()
		peerDone(peer, err)
	}()
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := Handshake(ctx, client, Proxy{Scheme: "http", Host: "proxy", Port: 8080}, testTarget)
	pe := checkProxyError(t, err, StageHandshake, 0)
	if !errors.Is(pe, io.EOF) && !errors.Is(pe, io.ErrUnexpectedEOF) {
		t.Fatal("empty response did not preserve an EOF cause")
	}
	if pe.Timeout() {
		t.Fatal("empty response was reported as a timeout")
	}
	waitErr(t, peer)
}
