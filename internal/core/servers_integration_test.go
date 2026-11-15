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

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/testutil"
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

func waitServerConnections(t *testing.T, inst *Instance) {
	t.Helper()
	deadline := time.NewTimer(testTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		connections := 0
		for _, info := range inst.Servers() {
			connections += info.Connections
		}
		if connections == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("server connections did not drain; still have %d", connections)
		}
	}
}

// ssRoutesFixture is the state shared by the phases of
// TestShadowsocksServersRoutesAndReload.
type ssRoutesFixture struct {
	protocol      servers.Protocol
	aliceNode     *chainRecorder
	bobNode       *chainRecorder
	aliceAddress  string
	bobAddress    string
	backend       string
	host          string
	backendPort   int
	alicePort     int
	bobPort       int
	alicePortText string
	bobPortText   string
	aliceKey      string
	bobKey        string
	home          string
	base          Options
	rotated       Options
	rotatedKey    string
	rotatedSpecs  []servers.Spec
	rotatedEntry  *serverEntry
	inst          *Instance
	closed        bool
}

// newSSRoutesFixture starts the route recorders and the echo backend and makes
// the two shadowsocks keys and the base options.
func newSSRoutesFixture(t *testing.T) *ssRoutesFixture {
	t.Helper()
	f := &ssRoutesFixture{}
	f.aliceNode, f.aliceAddress = startChainRecorder(t)
	f.bobNode, f.bobAddress = startChainRecorder(t)
	f.backend = echoServer(t)
	host, backendPortText, err := net.SplitHostPort(f.backend)
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	f.backendPort, err = strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("shadowsocks protocol is not registered")
	}
	f.protocol = protocol
	f.alicePort, f.alicePortText = serverPort(t)
	f.bobPort, f.bobPortText = serverPort(t)
	for f.bobPort == f.alicePort {
		f.bobPort, f.bobPortText = serverPort(t)
	}
	f.aliceKey, f.bobKey = f.newSecret(t), f.newSecret(t)
	f.home = testutil.TempHome(t)
	f.base = f.options([]servers.Spec{
		ssRouteSpec("alice-entry", f.alicePort, "alice", f.aliceKey),
		ssRouteSpec("bob-entry", f.bobPort, "bob", f.bobKey),
	}, nil, nil)
	return f
}

// ssRouteSpec is a shadowsocks entry on port that serves account with key.
func ssRouteSpec(name string, port int, account, key string) servers.Spec {
	return servers.Spec{
		Name: name, Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port,
		Accounts: []string{account}, Params: map[string]string{},
		Users: servers.UserSecrets{account: servers.SecretMap{"password": key}},
	}
}

// newSecret makes a fresh shadowsocks password.
func (f *ssRoutesFixture) newSecret(t *testing.T) string {
	t.Helper()
	values, err := f.protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	return values["password"]
}

// options is the instance options over the server list specs.
func (f *ssRoutesFixture) options(specs []servers.Spec, global []netip.Prefix, account map[string][]netip.Prefix) Options {
	return Options{
		HomeDir:       f.home,
		Authenticator: aliceAuthenticator(),
		RouteNodes: map[string][]map[string]any{
			"alice": {nodeMap(f.aliceAddress)}, "bob": {nodeMap(f.bobAddress)},
		},
		AccountRoute: map[string]string{"alice": "alice", "bob": "bob"},
		Servers:      specs, AllowedClients: global, AccountAllowedClients: account,
	}
}

// startInstance opens the instance on the fixture ports, taking new ports while
// they are in use, and closes it at cleanup unless the test closed it first.
func (f *ssRoutesFixture) startInstance(t *testing.T) {
	t.Helper()
	var startErr error
	for range 3 {
		f.inst, startErr = Start(context.Background(), f.base)
		if startErr == nil {
			break
		}
		var cliErr *cli.Error
		if !errors.As(startErr, &cliErr) || cliErr.Code != "PORT_IN_USE" {
			t.Fatalf("Start: %v", startErr)
		}
		f.alicePort, f.alicePortText = serverPort(t)
		f.bobPort, f.bobPortText = serverPort(t)
		for f.bobPort == f.alicePort {
			f.bobPort, f.bobPortText = serverPort(t)
		}
		f.base.Servers[0] = ssRouteSpec("alice-entry", f.alicePort, "alice", f.aliceKey)
		f.base.Servers[1] = ssRouteSpec("bob-entry", f.bobPort, "bob", f.bobKey)
	}
	if startErr != nil {
		t.Fatalf("Start after port retries: %v", startErr)
	}
	t.Cleanup(func() {
		if !f.closed {
			defer func() { _ = f.inst.Close() }()
			waitServerConnections(t, f.inst)
		}
	})
}

// dial sends one echo payload through the shadowsocks entry on port with key
// and returns the error of the round trip.
func (f *ssRoutesFixture) dial(t *testing.T, port int, key string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{
		Name: "loopback-client", Server: "127.0.0.1", Port: port,
		Password: key, Cipher: "2022-blake3-aes-128-gcm",
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(f.host), DstPort: toUint16(t, f.backendPort)}
	err = func() error {
		conn, err := client.DialContext(ctx, meta)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if err = conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
			return err
		}
		payload := []byte("shadowsocks-route-echo")
		if _, err = conn.Write(payload); err != nil {
			return err
		}
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(conn, got); err != nil {
			return err
		}
		if string(got) != string(payload) {
			return io.ErrUnexpectedEOF
		}
		return nil
	}()
	waitServerConnections(t, f.inst)
	return err
}

// routeChecks checks that each entry reaches only its own account's route, and
// that a wrong key gets no echo.
func (f *ssRoutesFixture) routeChecks(t *testing.T) {
	t.Helper()
	if err := f.dial(t, f.alicePort, f.aliceKey); err != nil {
		t.Fatalf("alice server route: %v", err)
	}
	if got := f.aliceNode.snapshot(); len(got) != 1 || got[0] != f.backend {
		t.Fatalf("alice route recorder = %v, want only %s", got, f.backend)
	}
	if got := f.bobNode.snapshot(); len(got) != 0 {
		t.Fatalf("alice traffic reached bob route: %v", got)
	}
	if err := f.dial(t, f.bobPort, f.bobKey); err != nil {
		t.Fatalf("bob server route: %v", err)
	}
	if got := f.aliceNode.snapshot(); len(got) != 1 || got[0] != f.backend {
		t.Fatalf("bob traffic unexpectedly changed alice route: %v", got)
	}
	if got := f.bobNode.snapshot(); len(got) != 1 || got[0] != f.backend {
		t.Fatalf("bob route recorder = %v, want only %s", got, f.backend)
	}
	wrongKey := f.newSecret(t)
	if err := f.dial(t, f.alicePort, wrongKey); err == nil {
		t.Fatal("wrong key completed echo")
	}
}

// equalReload reloads the base options: running listeners must stay.
func (f *ssRoutesFixture) equalReload(t *testing.T) {
	t.Helper()
	aliceEntry := f.inst.servers.items["alice-entry"]
	bobEntry := f.inst.servers.items["bob-entry"]
	if err := f.inst.Reconfigure(f.base); err != nil {
		t.Fatalf("equal-spec reload: %v", err)
	}
	if f.inst.servers.items["alice-entry"] != aliceEntry || f.inst.servers.items["bob-entry"] != bobEntry {
		t.Fatal("equal specs replaced running listeners")
	}
	if err := f.dial(t, f.alicePort, f.aliceKey); err != nil {
		t.Fatalf("alice after equal reload: %v", err)
	}
}

// rotate changes Alice's key: the new key serves, the old one does not, and
// Bob's listener is untouched.
func (f *ssRoutesFixture) rotate(t *testing.T) {
	t.Helper()
	f.rotatedKey = f.newSecret(t)
	f.rotatedSpecs = []servers.Spec{f.base.Servers[0].Clone(), f.base.Servers[1].Clone()}
	f.rotatedSpecs[0].Users["alice"]["password"] = f.rotatedKey
	f.rotated = f.options(f.rotatedSpecs, nil, nil)
	if err := f.inst.Reconfigure(f.rotated); err != nil {
		t.Fatalf("rotate alice key: %v", err)
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	if err := f.dial(t, f.alicePort, f.aliceKey); err == nil {
		t.Fatal("old key still works after rotation")
	}
	if err := f.dial(t, f.bobPort, f.bobKey); err != nil {
		t.Fatalf("bob listener changed during alice rotation: %v", err)
	}
}

// failedReload reloads an invalid key: the reload fails and the old listener
// keeps serving.
func (f *ssRoutesFixture) failedReload(t *testing.T) {
	t.Helper()
	f.rotatedEntry = f.inst.servers.items["alice-entry"]
	badSpecs := []servers.Spec{f.rotatedSpecs[0].Clone(), f.rotatedSpecs[1].Clone()}
	badSpecs[0].Users["alice"]["password"] = "invalid-key"
	if err := f.inst.Reconfigure(f.options(badSpecs, nil, nil)); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	if f.inst.servers.items["alice-entry"] != f.rotatedEntry {
		t.Fatal("old listener changed after failed reload")
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err != nil {
		t.Fatalf("old server stopped after failed reload: %v", err)
	}
}

// addressConflict reloads Alice onto blocker's occupied address: the reload
// fails and the old listener keeps serving.
func (f *ssRoutesFixture) addressConflict(t *testing.T, blocker net.Listener) {
	t.Helper()
	_, blockedPortText, err := net.SplitHostPort(blocker.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	blockedPort, err := strconv.Atoi(blockedPortText)
	if err != nil {
		t.Fatal(err)
	}
	conflictSpecs := []servers.Spec{f.rotatedSpecs[0].Clone(), f.rotatedSpecs[1].Clone()}
	conflictSpecs[0].Port = blockedPort
	if err := f.inst.Reconfigure(f.options(conflictSpecs, nil, nil)); err == nil {
		t.Fatal("occupied-address reload succeeded")
	}
	if f.inst.servers.items["alice-entry"] != f.rotatedEntry {
		t.Fatal("occupied-address failure replaced old listener")
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err != nil {
		t.Fatalf("old listener failed after address conflict: %v", err)
	}
}

// policies checks the global and the account allow lists, then restores the
// rotated options.
func (f *ssRoutesFixture) policies(t *testing.T) {
	t.Helper()
	globalDenied := f.options(f.rotatedSpecs, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil)
	if err := f.inst.Reconfigure(globalDenied); err != nil {
		t.Fatal(err)
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err == nil {
		t.Fatal("global policy allowed loopback")
	}
	if droppedFor(f.inst, "alice-entry") == 0 {
		t.Fatal("global denial was not counted")
	}
	accountDenied := f.options(f.rotatedSpecs, nil, map[string][]netip.Prefix{"alice": {netip.MustParsePrefix("192.0.2.0/24")}})
	if err := f.inst.Reconfigure(accountDenied); err != nil {
		t.Fatal(err)
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err == nil {
		t.Fatal("account policy allowed loopback")
	}
	if droppedFor(f.inst, "alice-entry") < 2 {
		t.Fatal("account denial was not counted")
	}
	if err := f.inst.Reconfigure(f.rotated); err != nil {
		t.Fatal(err)
	}
	if err := f.dial(t, f.alicePort, f.rotatedKey); err != nil {
		t.Fatalf("restored client policy: %v", err)
	}
}

// closeAndRebind closes the instance and checks that both ports can be bound
// again.
func (f *ssRoutesFixture) closeAndRebind(t *testing.T) {
	t.Helper()
	waitServerConnections(t, f.inst)
	if err := f.inst.Close(); err != nil {
		t.Fatal(err)
	}
	f.closed = true
	for _, portText := range []string{f.alicePortText, f.bobPortText} {
		addr := net.JoinHostPort("127.0.0.1", portText)
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", addr)
		if err != nil {
			t.Fatalf("TCP bind %s after Close: %v", addr, err)
		}
		_ = ln.Close()
	}
}

func TestShadowsocksServersRoutesAndReload(t *testing.T) {
	f := newSSRoutesFixture(t)
	f.startInstance(t)
	f.routeChecks(t)
	f.equalReload(t)
	f.rotate(t)
	f.failedReload(t)
	blocker, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	f.addressConflict(t, blocker)
	f.policies(t)
	f.closeAndRebind(t)
}

// ssCounterConn is the client connection of
// TestShadowsocksServerCountersSurviveReplacement.
type ssCounterConn struct {
	conn   net.Conn
	closed bool
}

// close closes the connection once. The Shadowsocks reader and writer share and
// re-close the raw connection, so a second close of it is not an error.
func (c *ssCounterConn) close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	closeErr := c.conn.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

// ssCounterFixture is the state shared by the phases of
// TestShadowsocksServerCountersSurviveReplacement.
type ssCounterFixture struct {
	protocol    servers.Protocol
	host        string
	backendPort int
	port        int
	home        string
	oldKey      string
	newKey      string
	payload     []byte
	oldEntry    *serverEntry
	oldDropped  uint64
	inst        *Instance
	closed      bool
}

// ssCounterSpec is the counter entry on port with the key of alice.
func ssCounterSpec(port int, key string) servers.Spec {
	return servers.Spec{Name: "counter-entry", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: port,
		Accounts: []string{"alice"}, Params: map[string]string{},
		Users: servers.UserSecrets{"alice": servers.SecretMap{"password": key}}}
}

// newCounterFixture starts the echo backend and makes the first key.
func newCounterFixture(t *testing.T) *ssCounterFixture {
	t.Helper()
	protocol, ok := servers.Lookup("shadowsocks")
	if !ok {
		t.Fatal("shadowsocks protocol is not registered")
	}
	f := &ssCounterFixture{protocol: protocol}
	backend := echoServer(t)
	host, backendPortText, err := net.SplitHostPort(backend)
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	f.backendPort, err = strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatal(err)
	}
	f.port, _ = serverPort(t)
	f.home = testutil.TempHome(t)
	f.oldKey = f.secret(t)
	f.payload = []byte("open-across-replacement")
	return f
}

// secret makes a fresh shadowsocks password.
func (f *ssCounterFixture) secret(t *testing.T) string {
	t.Helper()
	values, err := f.protocol.NewSecret("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	return values["password"]
}

// startInstance opens the instance on the fixture port, taking a new port while
// it is in use, and closes it at cleanup unless the test closed it first.
func (f *ssCounterFixture) startInstance(t *testing.T) {
	t.Helper()
	var err error
	for range 3 {
		f.inst, err = Start(context.Background(), Options{HomeDir: f.home, Servers: []servers.Spec{ssCounterSpec(f.port, f.oldKey)}})
		if err == nil {
			break
		}
		var cliErr *cli.Error
		if !isAddrInUse(err) && (!errors.As(err, &cliErr) || cliErr.Code != "PORT_IN_USE") {
			t.Fatalf("Start: %v", err)
		}
		f.port, _ = serverPort(t)
	}
	if err != nil {
		t.Fatalf("Start after port retries: %v", err)
	}
	t.Cleanup(func() {
		if !f.closed {
			defer func() { _ = f.inst.Close() }()
			waitServerConnections(t, f.inst)
		}
	})
}

// echoOnOpen checks that the open connection echoes before the replacement.
func (f *ssCounterFixture) echoOnOpen(t *testing.T, cc *ssCounterConn) {
	t.Helper()
	if err := cc.conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		_ = cc.close()
		t.Fatal(err)
	}
	if _, err := cc.conn.Write(f.payload); err != nil {
		_ = cc.close()
		t.Fatal(err)
	}
	got := make([]byte, len(f.payload))
	if _, err := io.ReadFull(cc.conn, got); err != nil {
		_ = cc.close()
		t.Fatal(err)
	}
	if string(got) != string(f.payload) {
		_ = cc.close()
		t.Fatalf("echo = %q, want %q", got, f.payload)
	}
}

// checkCountersBefore records the entry and checks its connection and drop
// counters before the replacement.
func (f *ssCounterFixture) checkCountersBefore(t *testing.T, cc *ssCounterConn) {
	t.Helper()
	if got := f.inst.Servers()[0].Connections; got != 1 {
		_ = cc.close()
		t.Fatalf("connections before replacement = %d, want 1", got)
	}
	f.oldEntry = f.inst.servers.items["counter-entry"]
	packet := &dropCountingPacket{}
	(&serverTunnel{entry: f.oldEntry}).HandleUDPPacket(packet, nil)
	if packet.drops != 1 {
		_ = cc.close()
		t.Fatalf("UDP drops = %d, want 1", packet.drops)
	}
	f.oldDropped = f.oldEntry.counters.dropped.Load()
	if f.oldDropped != 1 {
		_ = cc.close()
		t.Fatalf("dropped count before replacement = %d, want 1", f.oldDropped)
	}
}

// replaceEntry swaps the key of the entry: the new generation reuses the
// counters and the open connection is still counted.
func (f *ssCounterFixture) replaceEntry(t *testing.T, cc *ssCounterConn) {
	t.Helper()
	f.newKey = f.secret(t)
	if err := f.inst.Reconfigure(Options{HomeDir: f.home, Servers: []servers.Spec{ssCounterSpec(f.port, f.newKey)}}); err != nil {
		_ = cc.close()
		t.Fatalf("replace server generation: %v", err)
	}
	newEntry := f.inst.servers.items["counter-entry"]
	if newEntry == f.oldEntry {
		_ = cc.close()
		t.Fatal("replacement reused the old server entry")
	}
	if newEntry.counters != f.oldEntry.counters {
		_ = cc.close()
		t.Fatal("replacement did not reuse server counters")
	}
	if got := f.inst.Servers()[0].Connections; got != 1 {
		_ = cc.close()
		t.Fatalf("connections after replacement = %d, want 1", got)
	}
	if got := droppedFor(f.inst, "counter-entry"); got != f.oldDropped {
		_ = cc.close()
		t.Fatalf("dropped count after replacement = %d, want %d", got, f.oldDropped)
	}
}

// echoAfterReplacement checks that the connection opened before the replacement
// still echoes, then closes it.
func (f *ssCounterFixture) echoAfterReplacement(t *testing.T, cc *ssCounterConn) {
	t.Helper()
	if err := cc.conn.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		_ = cc.close()
		t.Fatal(err)
	}
	if _, err := cc.conn.Write(f.payload); err != nil {
		_ = cc.close()
		t.Fatalf("old connection after replacement: %v", err)
	}
	got := make([]byte, len(f.payload))
	if _, err := io.ReadFull(cc.conn, got); err != nil {
		_ = cc.close()
		t.Fatalf("old connection echo after replacement: %v", err)
	}
	if string(got) != string(f.payload) {
		_ = cc.close()
		t.Fatalf("old connection echo = %q, want %q", got, f.payload)
	}
	if err := cc.close(); err != nil {
		t.Fatal(err)
	}
	waitServerConnections(t, f.inst)
}

// waitConcurrentResult waits for the result of a concurrent call and fails the
// test at the deadline.
func waitConcurrentResult(t *testing.T, ch <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish before deadline", name)
		return nil
	}
}

// closeConcurrently closes the instance while a reconfigure runs: both must
// finish, and the port must be bindable afterwards.
func (f *ssCounterFixture) closeConcurrently(t *testing.T) {
	t.Helper()
	start := make(chan struct{})
	closeResult := make(chan error, 1)
	reconfigureResult := make(chan error, 1)
	go func() {
		<-start
		closeResult <- f.inst.Close()
	}()
	go func() {
		<-start
		reconfigureResult <- f.inst.Reconfigure(Options{HomeDir: f.home, Servers: []servers.Spec{ssCounterSpec(f.port, f.newKey)}})
	}()
	close(start)
	if err := waitConcurrentResult(t, closeResult, "Close"); err != nil {
		t.Fatalf("concurrent Close: %v", err)
	}
	f.closed = true
	if err := waitConcurrentResult(t, reconfigureResult, "Reconfigure"); err != nil && !errors.Is(err, errInstanceClosed) {
		t.Fatalf("concurrent Reconfigure: %v", err)
	}
	if err := f.inst.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port))
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("TCP bind %s after concurrent Close: %v", addr, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestShadowsocksServerCountersSurviveReplacement(t *testing.T) {
	f := newCounterFixture(t)
	f.startInstance(t)
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "counter-client", Server: "127.0.0.1", Port: f.port,
		Password: f.oldKey, Cipher: "2022-blake3-aes-128-gcm"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	meta := &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr(f.host), DstPort: toUint16(t, f.backendPort)}
	conn, err := client.DialContext(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	cc := &ssCounterConn{conn: conn}
	defer func() {
		_ = cc.close()
		if !f.closed {
			defer func() { _ = f.inst.Close() }()
			waitServerConnections(t, f.inst)
		}
	}()
	f.echoOnOpen(t, cc)
	f.checkCountersBefore(t, cc)
	f.replaceEntry(t, cc)
	f.echoAfterReplacement(t, cc)
	f.closeConcurrently(t)
}

func droppedFor(i *Instance, name string) uint64 {
	for _, info := range i.Servers() {
		if info.Name == name {
			return info.DroppedClients
		}
	}
	return 0
}
