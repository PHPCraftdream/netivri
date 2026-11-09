package fetch

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync/atomic"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// Phases of one fetch, named in a failure so a timeout says where it stood.
// A fixed list: never a host, address or the text of an error.
const (
	phaseResolve = "resolving the name"
	phaseConnect = "connecting"
	phaseTLS     = "TLS handshake"
	phaseSend    = "sending the request"
	phaseWait    = "waiting for the answer"
	phaseRead    = "reading the answer"
	phaseBody    = "reading the body"
)

// progress remembers the phase a fetch has reached. The transport calls the
// hooks from its own goroutines, so the phase is atomic.
type progress struct {
	proxied bool
	phase   atomic.Value // string
}

func newProgress(proxied bool) *progress {
	p := &progress{proxied: proxied}
	p.phase.Store(phaseConnect)
	return p
}

func (p *progress) set(phase string) { p.phase.Store(phase) }

// trace is the hooks that move the phase. With a proxy the connecting and TLS
// phases are those of the proxy; the annotation says "via proxy".
func (p *progress) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:              func(string) { p.set(phaseConnect) },
		DNSStart:             func(httptrace.DNSStartInfo) { p.set(phaseResolve) },
		DNSDone:              func(httptrace.DNSDoneInfo) { p.set(phaseConnect) },
		TLSHandshakeStart:    func() { p.set(phaseTLS) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { p.set(phaseSend) },
		GotConn:              func(httptrace.GotConnInfo) { p.set(phaseSend) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { p.set(phaseWait) },
		GotFirstResponseByte: func() { p.set(phaseRead) },
	}
}

// annotate appends the path and the phase to a FETCH_FAILED message:
// "request failed: timeout (direct, waiting for the answer)". The head of the
// message stays as it was.
func (p *progress) annotate(e *cli.Error) *cli.Error {
	if e == nil || e.Code != "FETCH_FAILED" {
		return e
	}
	path := "direct"
	if p.proxied {
		path = "via proxy"
	}
	phase, _ := p.phase.Load().(string)
	out := *e
	out.Message = e.Message + " (" + path + ", " + phase + ")"
	return &out
}
