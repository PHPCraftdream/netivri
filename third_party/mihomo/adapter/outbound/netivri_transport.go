// netivri addition: VLESS transport seam (see docs/design/latency.md).
// This file is added by the netivri authors and is not part of upstream mihomo.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

// ErrTransportUnsupported is returned by PrepareTransport for nodes whose
// transport cannot be pre-dialed (non-TCP networks or plain, non-TLS nodes).
var ErrTransportUnsupported = errors.New("netivri: transport preparation unsupported for this node")

// TransportSource provides pre-established transport connections. Take
// returns false when no connection is available, in which case the proxy
// falls back to its normal dial path.
type TransportSource interface {
	Take(ctx context.Context) (net.Conn, bool)
}

// Phase identifies a stage of the transport setup.
type Phase string

const (
	// PhaseTCP is the raw TCP dial stage.
	PhaseTCP Phase = "tcp"
	// PhaseTLS is the TLS/Reality handshake stage.
	PhaseTLS Phase = "tls"
)

// PhaseObserver receives the duration and outcome of each transport phase.
type PhaseObserver interface {
	ObservePhase(phase Phase, took time.Duration, err error)
}

// UnwrapVless returns the VLESS adapter behind a (possibly auto-close
// wrapped) proxy adapter, as adapter.ParseProxy builds it.
func UnwrapVless(a C.ProxyAdapter) (*Vless, bool) {
	for a != nil {
		switch t := a.(type) {
		case *Vless:
			return t, true
		case *autoCloseProxyAdapter:
			a = t.ProxyAdapter
		default:
			return nil, false
		}
	}
	return nil, false
}

// transportSupported reports whether the node uses a plain TCP + TLS
// transport that PrepareTransport can pre-dial.
func (v *Vless) transportSupported() bool {
	return (v.option.Network == "" || v.option.Network == "tcp") && v.option.TLS
}

// PrepareTransport pre-dials the TCP connection and completes the TLS
// handshake, returning the TLS conn ready for the VLESS stream. The returned
// conn is unwrapped so Vision can type-assert it later in the streamConnContext
// path.
func (v *Vless) PrepareTransport(ctx context.Context) (net.Conn, error) {
	if !v.transportSupported() {
		return nil, ErrTransportUnsupported
	}

	start := time.Now()
	c, err := v.dialer.DialContext(ctx, "tcp", v.addr)
	v.observePhase(PhaseTCP, time.Since(start), err)
	if err != nil {
		return nil, fmt.Errorf("%s connect error: %s", v.addr, err.Error())
	}

	// streamTLSConn reports the tls phase itself.
	tlsConn, err := v.streamTLSConn(ctx, c, false)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s connect error: %s", v.addr, err.Error())
	}
	return tlsConn, nil
}

// SetTransportSource attaches a source of pre-dialed transports, or clears
// it when s is nil.
func (v *Vless) SetTransportSource(s TransportSource) {
	v.transportSource.Store(&s)
}

// SetPhaseObserver attaches a phase observer, or clears it when o is nil.
func (v *Vless) SetPhaseObserver(o PhaseObserver) {
	v.phaseObserver.Store(&o)
}

// observePhase notifies the observer if one is set; no-op otherwise.
func (v *Vless) observePhase(phase Phase, took time.Duration, err error) {
	p := v.phaseObserver.Load()
	if p != nil && *p != nil {
		(*p).ObservePhase(phase, took, err)
	}
}
