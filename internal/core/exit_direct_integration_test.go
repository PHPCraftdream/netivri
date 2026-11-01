//go:build integration

package core

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func TestExitDirectAccountBypassesNodeWithoutAffectingRoutedAccount(t *testing.T) {
	recorder, recorderAddr := startChainRecorder(t)
	backend := echoServer(t)
	host, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	protocol, ok := servers.Lookup("vmess")
	if !ok {
		t.Fatal("vmess protocol is not registered")
	}
	newUUID := func() string {
		values, err := protocol.NewSecret("alice", nil)
		if err != nil {
			t.Fatal(err)
		}
		return values["uuid"]
	}
	port, _ := serverPort(t)
	aliceID, bobID := newUUID(), newUUID()
	spec := servers.Spec{
		Name: "exit-direct", Protocol: "vmess", Bind: "127.0.0.1", Port: port,
		Accounts: []string{"alice", "bob"}, Params: map[string]string{},
		Users: servers.UserSecrets{"alice": {"uuid": aliceID}, "bob": {"uuid": bobID}},
	}
	inst, err := Start(context.Background(), Options{
		HomeDir:      testutil.TempHome(t),
		Servers:      []servers.Spec{spec},
		RouteNodes:   map[string][]map[string]any{"route": {nodeMap(recorderAddr)}},
		AccountRoute: map[string]string{"alice": "route", "bob": "direct"},
		RoutePre:     map[string]string{"direct": "none"},
		RoutePost:    map[string]string{"direct": "none"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	connect := func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		client, err := outbound.NewVmess(outbound.VmessOption{
			Name: "loopback-client", Server: "127.0.0.1", Port: port,
			UUID: id, AlterID: 0, Cipher: "auto", Network: "tcp",
		})
		if err != nil {
			return err
		}
		defer client.Close()
		conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)})
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
			return err
		}
		payload := []byte("exit-direct-route-echo")
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			return err
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	if err := connect(aliceID); err != nil {
		t.Fatalf("alice echo: %v", err)
	}
	if err := connect(bobID); err != nil {
		t.Fatalf("bob echo: %v", err)
	}
	got := recorder.snapshot()
	want := net.JoinHostPort(host, backendPortText)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("route recorder = %v, want only alice's destination %s", got, want)
	}
}
