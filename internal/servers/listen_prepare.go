package servers

import (
	"context"
	"errors"
	"net"
	"sync"

	C "github.com/metacubex/mihomo/constant"
)

// preparedListenConfig transfers the pre-acquired TCP socket to a Mihomo
// listener constructor exactly once, preventing bind failures after loops start.
type preparedListenConfig struct {
	mu      sync.Mutex
	address string
	tcp     net.Listener
}

func (p *preparedListenConfig) Listen(_ context.Context, network, address string) (net.Listener, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if network != "tcp" || address != p.address || p.tcp == nil {
		return nil, net.ErrClosed
	}
	listener := p.tcp
	p.tcp = nil
	return listener, nil
}

func (p *preparedListenConfig) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return nil, errors.New("UDP listening is not supported")
}

func (p *preparedListenConfig) closeUnclaimed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tcp != nil {
		_ = p.tcp.Close()
		p.tcp = nil
	}
}

var _ C.InboundListenConfig = (*preparedListenConfig)(nil)
