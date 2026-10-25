package angel

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestClientNewRequest(t *testing.T) {
	c := Client{Addr: "127.0.0.1:1234", Token: "tok"}
	req, err := c.newRequest(context.Background(), http.MethodGet, "/v1/status")
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	if req.URL.Host != "127.0.0.1:1234" || req.Host != "127.0.0.1:1234" {
		t.Errorf("url/host = %s/%s", req.URL.Host, req.Host)
	}
	if req.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("authorization = %q", req.Header.Get("Authorization"))
	}
}

func TestProbeWithoutState(t *testing.T) {
	home := t.TempDir()
	if _, err := Probe(context.Background(), home); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Probe(empty home) = %v, want ErrNotRunning", err)
	}
	if _, err := os.Stat(StatePath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file created by Probe: %v", err)
	}
}

func TestProbeCorruptState(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDir(home), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(StatePath(home), []byte("junk"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Probe(context.Background(), home); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Probe(corrupt state) = %v, want ErrNotRunning", err)
	}
	// The stale file is removed best-effort.
	if _, err := os.Stat(StatePath(home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale state file not removed: %v", err)
	}
}
