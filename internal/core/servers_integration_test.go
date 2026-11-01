//go:build integration

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/testutil"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

func serverPort(t *testing.T) (int, string) {
	t.Helper()
	_, port, err := net.SplitHostPort(testutil.FreeLoopbackAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n, port
}

func TestShadowsocksServersRoutesAndReload(t *testing.T) {
	aliceNode, aliceAddress := startChainRecorder(t)
	bobNode, bobAddress := startChainRecorder(t)
	backend := echoServer(t)
	host, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("shadowsocks protocol is not registered")
	}
	newSecret := func() string {
		values, err := protocol.NewSecret("alice", nil)
		if err != nil {
			t.Fatal(err)
		}
		return values["password"]
	}
	makeSpec := func(name string, port int, account, key string) servers.Spec {
		return servers.Spec{
			Name: name, Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port,
			Accounts: []string{account}, Params: map[string]string{},
			Users: servers.UserSecrets{account: servers.SecretMap{"password": key}},
		}
	}
	alicePort, alicePortText := serverPort(t)
	bobPort, bobPortText := serverPort(t)
	for bobPort == alicePort {
		bobPort, bobPortText = serverPort(t)
	}
	aliceKey, bobKey := newSecret(), newSecret()
	home := testutil.TempHome(t)
	options := func(specs []servers.Spec, global []netip.Prefix, account map[string][]netip.Prefix) Options {
		return Options{
			HomeDir:       home,
			Authenticator: aliceAuthenticator(),
			RouteNodes: map[string][]map[string]any{
				"alice": {nodeMap(aliceAddress)}, "bob": {nodeMap(bobAddress)},
			},
			AccountRoute: map[string]string{"alice": "alice", "bob": "bob"},
			Servers:      specs, AllowedClients: global, AccountAllowedClients: account,
		}
	}
	base := options([]servers.Spec{
		makeSpec("alice-entry", alicePort, "alice", aliceKey),
		makeSpec("bob-entry", bobPort, "bob", bobKey),
	}, nil, nil)
	var inst *Instance
	var startErr error
	for attempt := 0; attempt < 3; attempt++ {
		inst, startErr = Start(context.Background(), base)
		if startErr == nil {
			break
		}
		var cliErr *cli.Error
		if !errors.As(startErr, &cliErr) || cliErr.Code != "PORT_IN_USE" {
			t.Fatalf("Start: %v", startErr)
		}
		alicePort, alicePortText = serverPort(t)
		bobPort, bobPortText = serverPort(t)
		for bobPort == alicePort {
			bobPort, bobPortText = serverPort(t)
		}
		base.Servers[0] = makeSpec("alice-entry", alicePort, "alice", aliceKey)
		base.Servers[1] = makeSpec("bob-entry", bobPort, "bob", bobKey)
	}
	if startErr != nil {
		t.Fatalf("Start after port retries: %v", startErr)
	}
	t.Cleanup(func() { _ = inst.Close() })

	dial := func(port int, key string) error {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{
			Name: "loopback-client", Server: "127.0.0.1", Port: port,
			Password: key, Cipher: "2022-blake3-aes-128-gcm",
		})
		if err != nil {
			return err
		}
		defer client.Close()
		meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)}
		conn, err := client.DialContext(ctx, meta)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
			return err
		}
		payload := []byte("shadowsocks-route-echo")
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
	if err := dial(alicePort, aliceKey); err != nil {
		t.Fatalf("alice server route: %v", err)
	}
	if got := aliceNode.snapshot(); len(got) != 1 || got[0] != backend {
		t.Fatalf("alice route recorder = %v, want only %s", got, backend)
	}
	if got := bobNode.snapshot(); len(got) != 0 {
		t.Fatalf("alice traffic reached bob route: %v", got)
	}
	if err := dial(bobPort, bobKey); err != nil {
		t.Fatalf("bob server route: %v", err)
	}
	if got := aliceNode.snapshot(); len(got) != 1 || got[0] != backend {
		t.Fatalf("bob traffic unexpectedly changed alice route: %v", got)
	}
	if got := bobNode.snapshot(); len(got) != 1 || got[0] != backend {
		t.Fatalf("bob route recorder = %v, want only %s", got, backend)
	}
	wrongKey := newSecret()
	if err := dial(alicePort, wrongKey); err == nil {
		t.Fatal("wrong key completed echo")
	}

	aliceEntry := inst.servers.items["alice-entry"]
	bobEntry := inst.servers.items["bob-entry"]
	if err := inst.Reconfigure(base); err != nil {
		t.Fatalf("equal-spec reload: %v", err)
	}
	if inst.servers.items["alice-entry"] != aliceEntry || inst.servers.items["bob-entry"] != bobEntry {
		t.Fatal("equal specs replaced running listeners")
	}
	if err := dial(alicePort, aliceKey); err != nil {
		t.Fatalf("alice after equal reload: %v", err)
	}

	rotatedKey := newSecret()
	rotatedSpecs := []servers.Spec{base.Servers[0].Clone(), base.Servers[1].Clone()}
	rotatedSpecs[0].Users["alice"]["password"] = rotatedKey
	rotated := options(rotatedSpecs, nil, nil)
	if err := inst.Reconfigure(rotated); err != nil {
		t.Fatalf("rotate alice key: %v", err)
	}
	if err := dial(alicePort, rotatedKey); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if err := dial(alicePort, aliceKey); err == nil {
		t.Fatal("old key still works after rotation")
	}
	if err := dial(bobPort, bobKey); err != nil {
		t.Fatalf("bob listener changed during alice rotation: %v", err)
	}

	rotatedEntry := inst.servers.items["alice-entry"]
	badSpecs := []servers.Spec{rotatedSpecs[0].Clone(), rotatedSpecs[1].Clone()}
	badSpecs[0].Users["alice"]["password"] = "invalid-key"
	if err := inst.Reconfigure(options(badSpecs, nil, nil)); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	if inst.servers.items["alice-entry"] != rotatedEntry {
		t.Fatal("old listener changed after failed reload")
	}
	if err := dial(alicePort, rotatedKey); err != nil {
		t.Fatalf("old server stopped after failed reload: %v", err)
	}

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	_, blockedPortText, err := net.SplitHostPort(blocker.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	blockedPort, err := strconv.Atoi(blockedPortText)
	if err != nil {
		t.Fatal(err)
	}
	conflictSpecs := []servers.Spec{rotatedSpecs[0].Clone(), rotatedSpecs[1].Clone()}
	conflictSpecs[0].Port = blockedPort
	conflict := options(conflictSpecs, nil, nil)
	if err := inst.Reconfigure(conflict); err == nil {
		t.Fatal("occupied-address reload succeeded")
	}
	if inst.servers.items["alice-entry"] != rotatedEntry {
		t.Fatal("occupied-address failure replaced old listener")
	}
	if err := dial(alicePort, rotatedKey); err != nil {
		t.Fatalf("old listener failed after address conflict: %v", err)
	}

	globalDenied := options(rotatedSpecs, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	if err := inst.Reconfigure(globalDenied); err != nil {
		t.Fatal(err)
	}
	if err := dial(alicePort, rotatedKey); err == nil {
		t.Fatal("global policy allowed loopback")
	}
	if droppedFor(inst, "alice-entry") == 0 {
		t.Fatal("global denial was not counted")
	}
	accountDenied := options(rotatedSpecs, nil, map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := inst.Reconfigure(accountDenied); err != nil {
		t.Fatal(err)
	}
	if err := dial(alicePort, rotatedKey); err == nil {
		t.Fatal("account policy allowed loopback")
	}
	if droppedFor(inst, "alice-entry") < 2 {
		t.Fatal("account denial was not counted")
	}
	if err := inst.Reconfigure(rotated); err != nil {
		t.Fatal(err)
	}
	if err := dial(alicePort, rotatedKey); err != nil {
		t.Fatalf("restored client policy: %v", err)
	}

	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
	for _, portText := range []string{alicePortText, bobPortText} {
		addr := net.JoinHostPort("127.0.0.1", portText)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("TCP bind %s after Close: %v", addr, err)
		}
		_ = ln.Close()
	}
}

func TestShadowsocksServerCountersSurviveReplacement(t *testing.T) {
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("shadowsocks protocol is not registered")
	}
	secret := func() string {
		values, err := protocol.NewSecret("alice", nil)
		if err != nil {
			t.Fatal(err)
		}
		return values["password"]
	}
	backend := echoServer(t)
	host, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := serverPort(t)
	makeSpec := func(port int, key string) servers.Spec {
		return servers.Spec{Name: "counter-entry", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port,
			Accounts: []string{"alice"}, Params: map[string]string{},
			Users: servers.UserSecrets{"alice": servers.SecretMap{"password": key}}}
	}
	home := testutil.TempHome(t)
	oldKey := secret()
	var inst *Instance
	for attempt := 0; attempt < 3; attempt++ {
		inst, err = Start(context.Background(), Options{HomeDir: home, Servers: []servers.Spec{makeSpec(port, oldKey)}})
		if err == nil {
			break
		}
		var cliErr *cli.Error
		if !isAddrInUse(err) && (!errors.As(err, &cliErr) || cliErr.Code != "PORT_IN_USE") {
			t.Fatalf("Start: %v", err)
		}
		port, _ = serverPort(t)
	}
	if err != nil {
		t.Fatalf("Start after port retries: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "counter-client", Server: "127.0.0.1", Port: port,
		Password: oldKey, Cipher: "2022-blake3-aes-128-gcm"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(host), DstPort: uint16(backendPort)}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("open-across-replacement")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	if got := inst.Servers()[0].Connections; got != 1 {
		t.Fatalf("connections before replacement = %d, want 1", got)
	}
	oldEntry := inst.servers.items["counter-entry"]
	packet := &dropCountingPacket{}
	(&serverTunnel{entry: oldEntry}).HandleUDPPacket(packet, nil)
	if packet.drops != 1 {
		t.Fatalf("UDP drops = %d, want 1", packet.drops)
	}
	oldDropped := oldEntry.counters.dropped.Load()
	if oldDropped != 1 {
		t.Fatalf("dropped count before replacement = %d, want 1", oldDropped)
	}
	newKey := secret()
	if err := inst.Reconfigure(Options{HomeDir: home, Servers: []servers.Spec{makeSpec(port, newKey)}}); err != nil {
		t.Fatalf("replace server generation: %v", err)
	}
	newEntry := inst.servers.items["counter-entry"]
	if newEntry == oldEntry {
		t.Fatal("replacement reused the old server entry")
	}
	if newEntry.counters != oldEntry.counters {
		t.Fatal("replacement did not reuse server counters")
	}
	if got := inst.Servers()[0].Connections; got != 1 {
		t.Fatalf("connections after replacement = %d, want 1", got)
	}
	if got := droppedFor(inst, "counter-entry"); got != oldDropped {
		t.Fatalf("dropped count after replacement = %d, want %d", got, oldDropped)
	}
	if err := conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("old connection after replacement: %v", err)
	}
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("old connection echo after replacement: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("old connection echo = %q, want %q", got, payload)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(testTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if inst.Servers()[0].Connections == 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("connection counter did not drain after close")
		}
	}

	start := make(chan struct{})
	closeResult := make(chan error, 1)
	reconfigureResult := make(chan error, 1)
	go func() {
		<-start
		closeResult <- inst.Close()
	}()
	go func() {
		<-start
		reconfigureResult <- inst.Reconfigure(Options{HomeDir: home, Servers: []servers.Spec{makeSpec(port, newKey)}})
	}()
	close(start)
	waitResult := func(ch <-chan error, name string) error {
		t.Helper()
		select {
		case err := <-ch:
			return err
		case <-time.After(testTimeout):
			t.Fatalf("%s did not finish before deadline", name)
			return nil
		}
	}
	if err := waitResult(closeResult, "Close"); err != nil {
		t.Fatalf("concurrent Close: %v", err)
	}
	if err := waitResult(reconfigureResult, "Reconfigure"); err != nil && !errors.Is(err, errInstanceClosed) {
		t.Fatalf("concurrent Reconfigure: %v", err)
	}
	if err := inst.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("TCP bind %s after concurrent Close: %v", addr, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}
func droppedFor(i *Instance, name string) uint64 {
	for _, info := range i.Servers() {
		if info.Name == name {
			return info.DroppedClients
		}
	}
	return 0
}
