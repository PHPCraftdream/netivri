//go:build bench

package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// BenchmarkWarmNodeProbe measures an actual HTTP probe through Mihomo and a
// loopback SOCKS5 upstream whose target server emulates network RTT.
func BenchmarkWarmNodeProbe(b *testing.B) {
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	target := httptest.NewServer(backend)
	defer target.Close()
	proxy := benchSOCKS(b)
	defer proxy.Close()
	parsed, _ := url.Parse(target.URL)
	node := map[string]any{"type": "socks5", "server": "127.0.0.1", "port": proxy.Addr().(*net.TCPAddr).Port}
	inst, err := Start(context.Background(), Options{HomeDir: b.TempDir(), Node: node})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = inst.Close(); drainFinalizers() }()
	w, err := startWarm(WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: target.URL}, node)
	if err != nil {
		b.Fatal(err)
	}
	defer w.Close()
	p := w.proxies
	if len(p) == 0 {
		b.Fatal("configured node proxy unavailable")
	}
	poolWarm, err := startWarm(WarmOptions{ProbeInterval: time.Hour, ProbeTimeout: time.Second, ProbeURL: target.URL, PoolSize: 1}, node)
	if err != nil {
		b.Fatal(err)
	}
	defer poolWarm.Close()
	poolProxy := poolWarm.proxies
	if len(poolProxy) == 0 {
		b.Fatal("pooled node proxy unavailable")
	}
	b.ReportAllocs()
	for _, pooled := range []bool{false, true} {
		name := "without-pool"
		if pooled {
			name = "with-pool"
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				proxyToUse := p[0]
				if pooled {
					proxyToUse = poolProxy[0]
				}
				_, err := proxyToUse.URLTest(ctx, parsed.String(), nil)
				cancel()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchSOCKS(b *testing.B) net.Listener {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var workers sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				// Simulate upstream TCP establishment RTT before protocol negotiation.
				timer := time.NewTimer(2 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-stop:
					return
				}
				g, ok := benchReadN(c, 2)
				if !ok || g[0] != 5 {
					return
				}
				if _, ok = benchReadN(c, int(g[1])); !ok {
					return
				}
				_, _ = c.Write([]byte{5, 0})
				r, ok := benchReadN(c, 4)
				if !ok || r[1] != 1 {
					return
				}
				var host string
				switch r[3] {
				case 1:
					v, ok := benchReadN(c, 4)
					if !ok {
						return
					}
					host = net.IP(v).String()
				case 3:
					n, ok := benchReadN(c, 1)
					if !ok {
						return
					}
					v, ok := benchReadN(c, int(n[0]))
					if !ok {
						return
					}
					host = string(v)
				case 4:
					v, ok := benchReadN(c, 16)
					if !ok {
						return
					}
					host = net.IP(v).String()
				default:
					return
				}
				p, ok := benchReadN(c, 2)
				if !ok {
					return
				}
				port := int(p[0])<<8 | int(p[1])
				up, e := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), time.Second)
				if e != nil {
					return
				}
				defer up.Close()
				_ = up.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
				go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
				<-done
				_ = c.Close()
				_ = up.Close()
				<-done
			}()
		}
	}()
	b.Cleanup(func() { close(stop); _ = l.Close(); workers.Wait() })
	return l
}

// benchReadN reads exactly n bytes; false on any short read.
func benchReadN(c net.Conn, n int) ([]byte, bool) {
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err == nil
}
