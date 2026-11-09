package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestDoctorServerTCPChecksUseTCPProbe(t *testing.T) {
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	cfg.Servers = []config.Server{{Name: "edge", Protocol: "shadowsocks", Bind: "0.0.0.0", Port: port, Accounts: "alice"}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveServerSpecs(home, cfg); err != nil {
		t.Fatal(err)
	}
	old := doctorServerListenTCP
	oldUDP := doctorServerListenUDP
	var tcpCalls, udpCalls int
	doctorServerListenTCP = func(got string) error {
		tcpCalls++
		if got != net.JoinHostPort("0.0.0.0", strconv.Itoa(port)) {
			t.Errorf("TCP probe address = %q, want %s", got, net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		}
		return errors.New("occupied")
	}
	doctorServerListenUDP = func(string) error {
		udpCalls++
		return nil
	}
	t.Cleanup(func() { doctorServerListenTCP, doctorServerListenUDP = old, oldUDP })
	d := &doctorContext{home: home, cfg: cfg}
	checks := d.checkServers()
	if tcpCalls != 1 || udpCalls != 0 {
		t.Fatalf("probe calls: TCP=%d UDP=%d, want TCP=1 UDP=0", tcpCalls, udpCalls)
	}
	if len(checks) < 2 || !strings.Contains(checks[len(checks)-1].Detail, "binds publicly") || !strings.Contains(checks[len(checks)-1].Detail, "port is taken") {
		t.Fatalf("checks=%+v", checks)
	}
}

func TestDoctorServerUDPChecksUseUDPProbe(t *testing.T) {
	for _, protocol := range []string{"hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) {
			portListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := portListener.Addr().(*net.TCPAddr).Port
			if err := portListener.Close(); err != nil {
				t.Fatal(err)
			}
			home := serverTestHome(t)
			cfg, err := loadConfig(home)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Servers = []config.Server{{Name: "edge", Protocol: protocol, Bind: "127.0.0.1", Port: port, Accounts: "alice"}}
			if err := config.Save(config.ConfigPath(home), cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveServerSpecs(home, cfg); err != nil {
				t.Fatal(err)
			}

			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			tcpErr := errors.New("TCP occupied")
			var tcpCalls, udpCalls int
			oldTCP, oldUDP, oldListen := doctorServerListenTCP, doctorServerListenUDP, doctorListen
			doctorListen = func(string) error { return tcpErr }
			doctorServerListenTCP = func(got string) error {
				tcpCalls++
				if got != addr {
					t.Errorf("TCP probe address = %q, want %q", got, addr)
				}
				return tcpErr
			}
			doctorServerListenUDP = func(got string) error {
				udpCalls++
				if got != addr {
					t.Errorf("UDP probe address = %q, want %q", got, addr)
				}
				return nil
			}
			t.Cleanup(func() {
				doctorServerListenTCP, doctorServerListenUDP, doctorListen = oldTCP, oldUDP, oldListen
			})

			d := &doctorContext{home: home, cfg: cfg}
			checks := d.checkServers()
			if tcpCalls != 0 || udpCalls != 1 {
				t.Fatalf("probe calls: TCP=%d UDP=%d, want TCP=0 UDP=1", tcpCalls, udpCalls)
			}
			if strings.Contains(checks[len(checks)-1].Detail, "port is taken") {
				t.Fatalf("TCP occupancy incorrectly marked UDP endpoint taken: %+v", checks)
			}

			tcpErr = nil
			udpErr := errors.New("UDP occupied")
			doctorServerListenUDP = func(got string) error {
				udpCalls++
				if got != addr {
					t.Errorf("UDP probe address = %q, want %q", got, addr)
				}
				return udpErr
			}
			checks = d.checkServers()
			if tcpCalls != 0 || udpCalls != 2 {
				t.Fatalf("probe calls after occupied UDP check: TCP=%d UDP=%d, want TCP=0 UDP=2", tcpCalls, udpCalls)
			}
			if !strings.Contains(checks[len(checks)-1].Detail, "port is taken") {
				t.Fatalf("UDP occupancy not reported: %+v", checks)
			}
		})
	}
}

func TestDoctorServerProbeClosesListeners(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr := tcp.Addr().String()
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := doctorServerListenTCP(tcpAddr); err != nil {
		t.Fatalf("TCP probe failed to bind and close %s: %v", tcpAddr, err)
	}
	tcpAgain, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		t.Fatalf("TCP probe left %s bound: %v", tcpAddr, err)
	}
	if err := tcpAgain.Close(); err != nil {
		t.Fatal(err)
	}

	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpAddr := udp.LocalAddr().String()
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := doctorServerListenUDP(udpAddr); err != nil {
		t.Fatalf("UDP probe failed to bind and close %s: %v", udpAddr, err)
	}
	udpAgain, err := net.ListenPacket("udp", udpAddr)
	if err != nil {
		t.Fatalf("UDP probe left %s bound: %v", udpAddr, err)
	}
	if err := udpAgain.Close(); err != nil {
		t.Fatal(err)
	}
}
