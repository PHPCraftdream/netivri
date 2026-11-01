//go:build integration

package servertest_test

import (
	"net"
	"strconv"
	"syscall"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/servers/servertest"
)

func TestFreePortIsFreeForTCPAndUDP(t *testing.T) {
	port := servertest.FreePort(t)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("port not free for TCP")
	}
	_ = listener.Close()
	packet, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatal("port not free for UDP")
	}
	_ = packet.Close()
}

func TestStartOnFreePortRetriesAddrInUse(t *testing.T) {
	attempts := 0
	port := servertest.StartOnFreePort(t, func(int) error {
		attempts++
		if attempts < 3 {
			return syscall.EADDRINUSE
		}
		return nil
	})
	if attempts != 3 || port <= 0 {
		t.Fatalf("attempts = %d, port = %d", attempts, port)
	}
}
