package dial

import (
	"context"
	"errors"
	"net"
	"testing"

	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func TestNilDialerAndNilPoolAreSafe(t *testing.T) {
	var nilDialer *Dialer
	if conn, err := nilDialer.DialContext(context.Background(), "tcp", "target:443"); conn != nil || !errors.Is(err, ErrNoHops) {
		t.Fatalf("nil Dialer result = (%v, %v), want nil and ErrNoHops", conn, err)
	}
	if conn, err := New(nil, nil, Options{}).DialContext(context.Background(), "tcp", "target:443"); conn != nil || !errors.Is(err, ErrNoHops) {
		t.Fatalf("nil pool result = (%v, %v), want nil and ErrNoHops", conn, err)
	}

	p := pool.New([]string{"hop"}, pool.Options{})
	hop := &fakeHop{key: "hop", dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if ctx == nil || ctx.Err() != nil {
			t.Fatalf("received unusable context: %v", ctx)
		}
		return &fakeConn{}, nil
	}}
	if conn, err := New(p, []Hop{hop}, Options{}).DialContext(context.Background(), "tcp", "target:443"); err != nil || conn == nil {
		t.Fatalf("context result = (%v, %v), want successful connection", conn, err)
	}
}
