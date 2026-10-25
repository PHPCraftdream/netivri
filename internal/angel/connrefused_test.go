package angel

import (
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
)

// refusedTestErr returns the platform's connection-refused errno.
// Implemented per platform in connrefused_{unix,windows}_test.go.
func TestIsConnRefused(t *testing.T) {
	refused := refusedTestErr()
	if !isConnRefused(refused) {
		t.Errorf("isConnRefused(%v) = false, want true", refused)
	}
	// wrapped, the way a dial error arrives through net/http
	wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", refused)}
	chain := fmt.Errorf("client.do: %w", wrapped)
	if !isConnRefused(chain) {
		t.Errorf("isConnRefused(%v) = false, want true", chain)
	}
	if isConnRefused(errors.New("some other failure")) {
		t.Error("a plain error was classified as refused")
	}
	if isConnRefused(nil) {
		t.Error("nil was classified as refused")
	}
}
