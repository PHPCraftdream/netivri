//go:build bench

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	auth "github.com/metacubex/mihomo/component/auth"
)

// Data-path benchmarks: local echo backend, fake SOCKS5 upstreams on
// loopback port 0, no external traffic, every operation deadline-bounded.
// Only one core.Start may be active per process, so each benchmark closes
// its instance via b.Cleanup before the next one starts.

const (
	benchConnectTimeout = 5 * time.Second
	benchBulkTimeout    = 60 * time.Second
)

// benchEchoBackend starts a TCP echo server on 127.0.0.1:0.
func benchEchoBackend(b *testing.B) string {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var conns sync.WaitGroup
	done := make(chan struct{})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer conns.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(benchBulkTimeout))
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	b.Cleanup(func() {
		close(done)
		_ = l.Close()
		conns.Wait()
	})
	return l.Addr().String()
}

// benchUpstream is a fake SOCKS5 upstream; hits counts the CONNECTs it
// served, so a routed benchmark can prove its traffic really took the route.
type benchUpstream struct {
	net.Listener
	hits atomic.Int64
}

// benchFakeUpstream starts a no-auth SOCKS5 upstream on 127.0.0.1:0 that
// answers CONNECT (IPv4 target) and pipes both ways.
func benchFakeUpstream(b *testing.B) *benchUpstream {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	l := &benchUpstream{Listener: listener}
	var conns sync.WaitGroup
	accepts := make(chan struct{})
	go func() {
		defer close(accepts)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer conns.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(benchBulkTimeout))
				head, ok := benchReadN(c, 2)
				if !ok || head[0] != 5 {
					return
				}
				if _, ok = benchReadN(c, int(head[1])); !ok {
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				req, ok := benchReadN(c, 4)
				if !ok || req[1] != 1 || req[3] != 1 {
					return
				}
				ip, ok := benchReadN(c, 4)
				if !ok {
					return
				}
				portBytes, ok := benchReadN(c, 2)
				if !ok {
					return
				}
				port := int(portBytes[0])<<8 | int(portBytes[1])
				up, err := net.DialTimeout("tcp",
					net.JoinHostPort(net.IP(ip).String(), strconv.Itoa(port)),
					benchConnectTimeout)
				if err != nil {
					return
				}
				defer up.Close()
				_ = up.SetDeadline(time.Now().Add(benchBulkTimeout))
				l.hits.Add(1)
				if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
				<-done
				// Closing both ends unblocks the other copier.
				_ = c.Close()
				_ = up.Close()
				<-done
			}(c)
		}
	}()
	b.Cleanup(func() {
		_ = l.Close()
		// The accept loop is the only caller of conns.Add: join it first.
		<-accepts
		conns.Wait()
	})
	return l
}

// benchRouteHits fails the benchmark unless the upstreams served at least
// want CONNECTs in total: without this a routing regression to DIRECT would
// still complete the echo exchange and the numbers would not be the routed
// path's.
func benchRouteHits(b *testing.B, want int64, upstreams ...*benchUpstream) {
	b.Helper()
	var total int64
	for _, u := range upstreams {
		total += u.hits.Load()
	}
	if total < want {
		b.Fatalf("upstreams served %d connections, want at least %d: traffic did not take the route", total, want)
	}
}

// benchReportPercentiles publishes the p50 and p99 of the per-iteration
// latencies next to the usual ns/op.
func benchReportPercentiles(b *testing.B, latencies []time.Duration) {
	if len(latencies) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(p int) float64 {
		idx := (p*len(sorted)+99)/100 - 1
		if idx < 0 {
			idx = 0
		}
		return float64(sorted[idx].Nanoseconds())
	}
	b.ReportMetric(at(50), "p50-ns")
	b.ReportMetric(at(99), "p99-ns")
}

// benchEcho sends one marked byte through the established tunnel and checks
// that the same byte comes back.
func benchEcho(c net.Conn) error {
	const mark = 0x5a
	if _, err := c.Write([]byte{mark}); err != nil {
		return err
	}
	got, ok := benchReadN(c, 1)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	if got[0] != mark {
		return errors.New("echo returned a different byte")
	}
	return nil
}

// benchNodeMap builds a socks5 node mapping for the upstream listener.
func benchNodeMap(l net.Listener) map[string]any {
	port := l.Addr().(*net.TCPAddr).Port
	return map[string]any{"type": "socks5", "server": "127.0.0.1", "port": port}
}

// benchSocksConnect dials the proxy and completes the SOCKS5 handshake
// (username/password when user is not empty) with an IPv4 CONNECT to target.
func benchSocksConnect(addr, target, user, pass string, deadline time.Time) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, benchConnectTimeout)
	if err != nil {
		return nil, err
	}
	if err := c.SetDeadline(deadline); err != nil {
		_ = c.Close()
		return nil, err
	}
	greeting := []byte{5, 1, 0}
	if user != "" {
		greeting = []byte{5, 1, 2}
	}
	if _, err := c.Write(greeting); err != nil {
		_ = c.Close()
		return nil, err
	}
	reply, ok := benchReadN(c, 2)
	if !ok {
		_ = c.Close()
		return nil, io.ErrUnexpectedEOF
	}
	switch {
	case user == "":
		if reply[1] != 0 {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
	case reply[1] == 2:
		sub := []byte{1, byte(len(user))}
		sub = append(sub, user...)
		sub = append(sub, byte(len(pass)))
		sub = append(sub, pass...)
		if _, err := c.Write(sub); err != nil {
			_ = c.Close()
			return nil, err
		}
		if authReply, ok := benchReadN(c, 2); !ok || authReply[1] != 0 {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
	default:
		_ = c.Close()
		return nil, io.ErrUnexpectedEOF
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		_ = c.Close()
		return nil, io.ErrUnexpectedEOF
	}
	req := []byte{5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(port >> 8), byte(port)}
	if _, err := c.Write(req); err != nil {
		_ = c.Close()
		return nil, err
	}
	switch reply2, ok := benchReadN(c, 4); {
	case !ok:
		_ = c.Close()
		return nil, io.ErrUnexpectedEOF
	case reply2[0] != 5 || reply2[1] != 0:
		_ = c.Close()
		return nil, errors.New("SOCKS5 CONNECT was refused")
	case reply2[3] == 1:
		if _, ok := benchReadN(c, 4+2); !ok {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
	case reply2[3] == 3:
		n, ok := benchReadN(c, 1)
		if !ok {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
		if _, ok := benchReadN(c, int(n[0])+2); !ok {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
	case reply2[3] == 4:
		if _, ok := benchReadN(c, 16+2); !ok {
			_ = c.Close()
			return nil, io.ErrUnexpectedEOF
		}
	default:
		_ = c.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return c, nil
}

// benchStart starts one core instance and registers Close as cleanup.
func benchStart(b *testing.B, o Options) *Instance {
	b.Helper()
	inst, err := Start(context.Background(), o)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = inst.Close()
		drainFinalizers()
	})
	return inst
}

// benchRoutedOptions builds the routed sticky-group setup: one account, one
// route with the given fake upstreams as members.
func benchRoutedOptions(home string, upstreams []*benchUpstream) Options {
	nodes := make([]map[string]any, len(upstreams))
	for i, u := range upstreams {
		nodes[i] = benchNodeMap(u.Listener)
	}
	return Options{
		HomeDir: home,
		Authenticator: auth.NewAuthenticator([]auth.AuthUser{
			{User: "bench", Pass: "bench"},
		}),
		RouteNodes:   map[string][]map[string]any{"bench": nodes},
		AccountRoute: map[string]string{"bench": "bench"},
	}
}

// benchConnectOnce is one iteration of the connect benchmarks: a new proxy
// connection, the SOCKS5 handshake, one echoed byte, close.
func benchConnectOnce(addr, backend, user, pass string) error {
	c, err := benchSocksConnect(addr, backend, user, pass, time.Now().Add(benchConnectTimeout))
	if err != nil {
		return err
	}
	defer c.Close()
	return benchEcho(c)
}

func BenchmarkConnectDirect(b *testing.B) {
	backend := benchEchoBackend(b)
	inst := benchStart(b, Options{HomeDir: b.TempDir()})
	latencies := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := benchConnectOnce(inst.Addr(), backend, "", ""); err != nil {
			b.Fatal(err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()
	benchReportPercentiles(b, latencies)
}

func BenchmarkConnectRouted(b *testing.B) {
	backend := benchEchoBackend(b)
	n0 := benchFakeUpstream(b)
	n1 := benchFakeUpstream(b)
	inst := benchStart(b, benchRoutedOptions(b.TempDir(), []*benchUpstream{n0, n1}))
	latencies := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if err := benchConnectOnce(inst.Addr(), backend, "bench", "bench"); err != nil {
			b.Fatal(err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()
	benchReportPercentiles(b, latencies)
	benchRouteHits(b, int64(b.N), n0, n1)
}

func BenchmarkConnectRoutedParallel(b *testing.B) {
	backend := benchEchoBackend(b)
	n0 := benchFakeUpstream(b)
	n1 := benchFakeUpstream(b)
	inst := benchStart(b, benchRoutedOptions(b.TempDir(), []*benchUpstream{n0, n1}))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Error, not Fatal: Fatal must not be called from these goroutines.
			if err := benchConnectOnce(inst.Addr(), backend, "bench", "bench"); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.StopTimer()
	benchRouteHits(b, int64(b.N), n0, n1)
}

func BenchmarkThroughputRouted(b *testing.B) {
	const chunk = 64 << 10   // 64 KiB writes
	const eachWay = 32 << 20 // 32 MiB each way
	backend := benchEchoBackend(b)
	n0 := benchFakeUpstream(b)
	n1 := benchFakeUpstream(b)
	inst := benchStart(b, benchRoutedOptions(b.TempDir(), []*benchUpstream{n0, n1}))
	payload := make([]byte, chunk)
	b.SetBytes(2 * eachWay)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c, err := benchSocksConnect(inst.Addr(), backend, "bench", "bench",
			time.Now().Add(benchBulkTimeout))
		if err != nil {
			b.Fatal(err)
		}
		read := make(chan error, 1)
		go func() {
			var total int64
			buf := make([]byte, chunk)
			for total < eachWay {
				n, err := io.ReadAtLeast(c, buf, chunk)
				total += int64(n)
				if err != nil {
					read <- err
					return
				}
			}
			read <- nil
		}()
		var writeErr error
		for written := 0; written < eachWay; written += chunk {
			if _, writeErr = c.Write(payload); writeErr != nil {
				break
			}
		}
		if writeErr != nil {
			// Closing unblocks the reader goroutine before the benchmark stops.
			_ = c.Close()
			<-read
			b.Fatal(writeErr)
		}
		readErr := <-read
		_ = c.Close()
		if readErr != nil {
			b.Fatal(readErr)
		}
	}
	b.StopTimer()
	benchRouteHits(b, int64(b.N), n0, n1)
}

func BenchmarkParallelConnects1000(b *testing.B) {
	const workers = 1000
	backend := benchEchoBackend(b)
	n0 := benchFakeUpstream(b)
	n1 := benchFakeUpstream(b)
	inst := benchStart(b, benchRoutedOptions(b.TempDir(), []*benchUpstream{n0, n1}))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := make(chan struct{})
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				<-start
				errs <- benchConnectOnce(inst.Addr(), backend, "bench", "bench")
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	benchRouteHits(b, int64(workers)*int64(b.N), n0, n1)
}
