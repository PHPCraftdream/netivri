//go:build e2e

package main

import (
	"testing"
	"time"
)

// A running angel applies account-level post-hop changes made through the
// account CLI; "none" disables the inherited global hop while preserving the node route.
func TestE2ELivePostSOCKS5AndNone(t *testing.T) {
	f := startLive(t)
	post := accountsServer(t)
	postURL := "socks5://" + post.listener.Addr().String()

	code, out, stderr := accountsCommand(t, f.home, "set-socks5", "alice", "--post", postURL)
	if code != 0 {
		t.Fatalf("account set-socks5 --post URL: %s%s", out, stderr)
	}
	waitLive(t, f.home, "account post SOCKS5 URL", func(s liveStatus) bool { return s.Data.Reload.Count >= 1 })
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if post.count.Load() != 1 {
		t.Fatalf("post hop connections=%d, want 1", post.count.Load())
	}
	if f.p1.count.Load() != 1 {
		t.Fatalf("node connections=%d, want 1", f.p1.count.Load())
	}

	code, out, stderr = accountsCommand(t, f.home, "set-socks5", "alice", "--post", "none")
	if code != 0 {
		t.Fatalf("account set-socks5 --post none: %s%s", out, stderr)
	}
	waitLive(t, f.home, "account post SOCKS5 none", func(s liveStatus) bool { return s.Data.Reload.Count >= 2 })
	accountsClient(t, f.listen, "alice", f.pass, f.backend)
	if post.count.Load() != 1 {
		t.Errorf("post hop connections after none=%d, want unchanged 1", post.count.Load())
	}
	if f.p1.count.Load() != 2 {
		t.Errorf("node connections after none=%d, want 2", f.p1.count.Load())
	}
	time.Sleep(liveSettle)
}
