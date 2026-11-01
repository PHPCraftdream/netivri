//go:build integration

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/servers"
	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
)

const udpIntegrationProtocolName = "core-test-udp-echo"

func init() {
	servers.Register(udpEchoProtocol{})
	servers.Register(tcpEchoProtocol{})
}

type udpEchoProtocol struct{}

func (udpEchoProtocol) Name() string                                             { return udpIntegrationProtocolName }
func (udpEchoProtocol) UDPBound() bool                                           { return true }
func (udpEchoProtocol) SingleUser() bool                                         { return false }
func (udpEchoProtocol) Validate(map[string]string) error                         { return nil }
func (udpEchoProtocol) NewMaterial(map[string]string) (map[string]string, error) { return nil, nil }
func (udpEchoProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	return nil, nil
}
func (udpEchoProtocol) Link(servers.Spec, string, string) (string, error) { return "", nil }
func (udpEchoProtocol) Start(sp servers.Spec, env servers.Env) (io.Closer, error) {
	pc, err := env.ListenConfig.ListenPacket(context.Background(), "udp", sp.ListenAddress())
	if err != nil {
		return nil, err
	}
	ln, err := env.ListenConfig.Listen(context.Background(), "tcp", sp.ListenAddress())
	if err == nil {
		_ = ln.Close()
		_ = pc.Close()
		return nil, errors.New("UDP protocol unexpectedly opened TCP")
	}
	r := &udpEchoRunning{pc: pc, done: make(chan struct{}), joined: make(chan struct{})}
	go r.loop()
	return r, nil
}

type udpEchoRunning struct {
	pc           net.PacketConn
	done, joined chan struct{}
	once         sync.Once
}

func (r *udpEchoRunning) loop() {
	defer close(r.joined)
	buf := make([]byte, 2048)
	for {
		if err := r.pc.SetDeadline(time.Now().Add(testTimeout)); err != nil {
			return
		}
		n, a, err := r.pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if err = r.pc.SetWriteDeadline(time.Now().Add(testTimeout)); err != nil {
			return
		}
		if _, err = r.pc.WriteTo(buf[:n], a); err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
	}
}
func (r *udpEchoRunning) Close() error {
	var err error
	r.once.Do(func() { close(r.done); err = r.pc.Close(); <-r.joined })
	return err
}
func udpEchoSpec(port int, rev string) servers.Spec {
	params := map[string]string{}
	if rev != "" {
		params["revision"] = rev
	}
	return servers.Spec{Name: "udp-echo", Protocol: udpIntegrationProtocolName, Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: params}
}
func udpAddress(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func udpExchange(t *testing.T, port int, payload string, deadline time.Duration) bool {
	t.Helper()
	c, err := net.DialTimeout("udp", udpAddress(port), time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	if err = c.SetDeadline(time.Now().Add(deadline)); err != nil {
		t.Fatal(err)
	}
	if err = c.SetWriteDeadline(time.Now().Add(deadline)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Write([]byte(payload)); err != nil {
		return false
	}
	buf := make([]byte, len(payload))
	n, err := c.Read(buf)
	return err == nil && string(buf[:n]) == payload
}
func startUDPInstance(t *testing.T, home string) (*Instance, int) {
	t.Helper()
	var inst *Instance
	port := servertest.StartOnFreePort(t, func(port int) error {
		candidate, err := Start(context.Background(), Options{HomeDir: home, Servers: []servers.Spec{udpEchoSpec(port, "initial")}})
		if err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			return err
		}
		inst = candidate
		return nil
	})
	return inst, port
}
func TestUDPServerStartReconfigureRollbackAndClose(t *testing.T) {
	home := t.TempDir()
	inst, p1 := startUDPInstance(t, home)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = inst.Close()
		}
	})
	if !udpExchange(t, p1, "initial", testTimeout) {
		t.Fatal("initial UDP echo failed")
	}
	if err := inst.Reconfigure(Options{HomeDir: home, Servers: []servers.Spec{udpEchoSpec(p1, "changed")}}); err != nil {
		t.Fatal(err)
	}
	if !udpExchange(t, p1, "same-port-reconfigured", testTimeout) {
		t.Fatal("same-port parameter replacement failed")
	}
	p2 := servertest.StartOnFreePort(t, func(port int) error {
		if port == p1 {
			return syscall.EADDRINUSE
		}
		err := inst.Reconfigure(Options{HomeDir: home, Servers: []servers.Spec{udpEchoSpec(port, "different-port")}})
		if err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			return err
		}
		return nil
	})
	if !udpExchange(t, p2, "new-port", testTimeout) {
		t.Fatal("new UDP listener failed")
	}
	probe, err := net.ListenPacket("udp", udpAddress(p1))
	if err != nil {
		t.Fatalf("old port not freed after replacement: %v", err)
	}
	_ = probe.Close()
	p1 = p2
	blocker, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	blocked := blocker.LocalAddr().(*net.UDPAddr).Port
	if err = inst.Reconfigure(Options{HomeDir: home, Servers: []servers.Spec{udpEchoSpec(blocked, "blocked")}}); err == nil {
		_ = blocker.Close()
		t.Fatal("occupied UDP replacement succeeded")
	} else {
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" {
			t.Fatalf("expected PORT_IN_USE, got %v", err)
		}
	}
	if err = blocker.Close(); err != nil {
		t.Fatal(err)
	}
	if !udpExchange(t, p1, "restored", testTimeout) {
		t.Fatal("old listener stopped responding after failed replacement")
	}
	if err = inst.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	probe, err = net.ListenPacket("udp", udpAddress(p1))
	if err != nil {
		t.Fatalf("Close did not free UDP port: %v", err)
	}
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
}

const tcpIntegrationProtocolName = "core-test-tcp-echo"

type tcpEchoProtocol struct{}

func (tcpEchoProtocol) Name() string                                             { return tcpIntegrationProtocolName }
func (tcpEchoProtocol) SingleUser() bool                                         { return false }
func (tcpEchoProtocol) Validate(map[string]string) error                         { return nil }
func (tcpEchoProtocol) NewMaterial(map[string]string) (map[string]string, error) { return nil, nil }
func (tcpEchoProtocol) NewSecret(string, map[string]string) (map[string]string, error) {
	return nil, nil
}
func (tcpEchoProtocol) Link(servers.Spec, string, string) (string, error) { return "", nil }
func (tcpEchoProtocol) Start(sp servers.Spec, env servers.Env) (io.Closer, error) {
	ln, err := env.ListenConfig.Listen(context.Background(), "tcp", sp.ListenAddress())
	if err != nil {
		return nil, err
	}
	r := &tcpEchoRunning{ln: ln, done: make(chan struct{}), joined: make(chan struct{})}
	go r.loop()
	return r, nil
}

type tcpEchoRunning struct {
	ln           net.Listener
	done, joined chan struct{}
	once         sync.Once
	wg           sync.WaitGroup
}

func (r *tcpEchoRunning) loop() {
	defer close(r.joined)
	for {
		c, err := r.ln.Accept()
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer c.Close()
			if err = c.SetDeadline(time.Now().Add(testTimeout)); err != nil {
				return
			}
			_, _ = io.Copy(c, c)
		}()
	}
}
func (r *tcpEchoRunning) Close() error {
	var err error
	r.once.Do(func() { close(r.done); err = r.ln.Close(); <-r.joined; r.wg.Wait() })
	return err
}
func tcpEchoSpec(port int, rev string) servers.Spec {
	return servers.Spec{Name: "tcp-echo", Protocol: tcpIntegrationProtocolName, Bind: "127.0.0.1", Port: port, Accounts: []string{"alice"}, Params: map[string]string{"revision": rev}}
}
func tcpExchange(t *testing.T, port int, payload string) bool {
	t.Helper()
	c, err := net.DialTimeout("tcp", udpAddress(port), testTimeout)
	if err != nil {
		return false
	}
	defer c.Close()
	if err = c.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	if err = c.SetWriteDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(c, payload); err != nil {
		return false
	}
	got := make([]byte, len(payload))
	_, err = io.ReadFull(c, got)
	return err == nil && string(got) == payload
}
func TestMixedTCPUDPCommitRollbackIntegration(t *testing.T) {
	s := &serverSet{broker: newSocketBroker()}
	t.Cleanup(s.close)
	var tcpPort, udpPort int
	tcpPort = servertest.StartOnFreePort(t, func(port int) error {
		tx, err := s.prepare([]servers.Spec{tcpEchoSpec(port, "old")}, nil, nil, nil)
		if err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			return err
		}
		if err = tx.commit(); err != nil {
			return err
		}
		return nil
	})
	udpPort = servertest.StartOnFreePort(t, func(port int) error {
		tx, err := s.prepare([]servers.Spec{tcpEchoSpec(tcpPort, "old"), udpEchoSpec(port, "old")}, nil, nil, nil)
		if err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			return err
		}
		if err = tx.commit(); err != nil {
			var ce *cli.Error
			if errors.As(err, &ce) && ce.Code == "PORT_IN_USE" {
				return syscall.EADDRINUSE
			}
			return err
		}
		return nil
	})
	blocker, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	blocked := blocker.LocalAddr().(*net.UDPAddr).Port
	oldTCP := s.items["tcp-echo"]
	tx, err := s.prepare([]servers.Spec{tcpEchoSpec(tcpPort, "changed"), udpEchoSpec(blocked, "changed")}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stagedTCP, ok := tx.next["tcp-echo"].running.Closer.(*tcpEchoRunning)
	if !ok {
		t.Fatal("staged TCP listener has unexpected closer")
	}
	if err = tx.commit(); err == nil {
		t.Fatal("occupied UDP replacement committed")
	}
	if s.items["tcp-echo"] != oldTCP {
		t.Fatal("old TCP entry was not retained")
	}
	select {
	case <-stagedTCP.joined:
	case <-time.After(testTimeout):
		t.Fatal("staged TCP closer was not cleaned up")
	}
	if !tcpExchange(t, tcpPort, "old-tcp") {
		t.Fatal("old TCP did not echo after rollback")
	}
	if !udpExchange(t, udpPort, "old-udp", testTimeout) {
		t.Fatal("old UDP did not echo after rollback")
	}
	s.close()
	probe, err := net.Listen("tcp", udpAddress(tcpPort))
	if err != nil {
		t.Fatalf("Close did not free TCP port: %v", err)
	}
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
}
