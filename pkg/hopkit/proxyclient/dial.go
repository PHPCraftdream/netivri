package proxyclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

// Option configures a proxy handshake.
type Option func(*handshakeOptions)

type handshakeOptions struct {
	tlsConfig   *tls.Config
	negotiation time.Duration
}

// WithNegotiationTimeout bounds the exchange before the target request is
// sent (TLS setup and SOCKS5 method and authentication negotiation). A proxy
// that does not answer in time fails with a network timeout at the stage it
// stalled in, not with a context error, so the caller can attribute it to the
// proxy. The context deadline still applies to the whole handshake; a
// nonpositive duration or one past the context deadline has no effect.
func WithNegotiationTimeout(d time.Duration) Option {
	return func(o *handshakeOptions) { o.negotiation = d }
}

// WithTLSConfig supplies TLS settings for HTTPS proxies. Certificate verification remains enabled.
// The configuration is cloned before use.
func WithTLSConfig(config *tls.Config) Option {
	return func(o *handshakeOptions) { o.tlsConfig = config }
}

// Handshake establishes a tunnel over conn. The caller remains responsible for closing conn on success and failure.
func Handshake(ctx context.Context, conn net.Conn, p Proxy, target string, opts ...Option) (net.Conn, error) {
	if ctx == nil || conn == nil {
		return nil, proxyError(StageOther, errInvalidTarget)
	}
	if err := validateProxy(p); err != nil {
		return nil, proxyError(StageOther, err)
	}
	host, port, err := validateTarget(target)
	if err != nil {
		return nil, proxyError(StageOther, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		return nil, proxyError(StageConnect, ctxErr)
	}
	o := handshakeOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	result, err := runHandshake(ctx, conn, p, host, port, target, o)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Dialer is the context-aware dialing interface required by Dial.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Dial establishes a proxy connection and negotiates a tunnel to target using d.DialContext.
// The proxy server name is dialed directly, without client-side target resolution.
func Dial(ctx context.Context, d Dialer, p Proxy, target string, opts ...Option) (net.Conn, error) {
	if ctx == nil || d == nil {
		return nil, proxyError(StageOther, errInvalidProxy)
	}
	if err := validateProxy(p); err != nil {
		return nil, proxyError(StageOther, err)
	}
	if _, _, err := validateTarget(target); err != nil {
		return nil, proxyError(StageOther, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		return nil, proxyError(StageConnect, ctxErr)
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		if ctxErr := contextFailure(ctx); ctxErr != nil {
			return nil, proxyError(StageConnect, ctxErr)
		}
		return nil, proxyError(StageConnect, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, proxyError(StageConnect, ctxErr)
	}
	if conn == nil {
		return nil, proxyError(StageConnect, errInvalidProxy)
	}
	tunnel, err := Handshake(ctx, conn, p, target, opts...)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tunnel, nil
}

func contextFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func runHandshake(ctx context.Context, conn net.Conn, p Proxy, host string, port int, target string, opts handshakeOptions) (net.Conn, error) {
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, deadlineSetupError(ctx, conn, err)
	}
	finish := watchCancel(ctx, conn)
	targetPhase := negotiationPhase(ctx, conn, deadline, opts.negotiation)
	n := negotiate(ctx, conn, p, host, port, target, opts, targetPhase)
	if n.err != nil {
		return nil, failHandshake(n, finish)
	}
	contextErr, clearErr := finish()
	if contextErr != nil {
		if clearErr != nil {
			contextErr = errors.Join(contextErr, clearErr)
		}
		return nil, proxyErrorWithTarget(n.stage, 0, contextErr, true)
	}
	if clearErr != nil {
		return nil, proxyErrorWithTarget(StageConnect, 0, clearErr, true)
	}
	return n.result, nil
}

// deadlineSetupError maps a failed initial SetDeadline to the handshake error, clearing the deadline first.
func deadlineSetupError(ctx context.Context, conn net.Conn, err error) error {
	clearErr := conn.SetDeadline(time.Time{})
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		if clearErr != nil {
			ctxErr = errors.Join(ctxErr, clearErr)
		}
		return proxyError(StageConnect, ctxErr)
	}
	if clearErr != nil {
		return proxyError(StageConnect, errors.Join(err, clearErr))
	}
	return proxyError(StageConnect, err)
}

// watchCancel expires conn's deadline as soon as ctx is done. The returned
// finish stops the watch, waits for it, and clears the deadline.
func watchCancel(ctx context.Context, conn net.Conn) func() (contextErr, clearErr error) {
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		close(callbackDone)
	})
	return func() (contextErr, clearErr error) {
		if !stop() {
			<-callbackDone
		}
		contextErr = contextFailure(ctx)
		clearErr = conn.SetDeadline(time.Time{})
		if contextErr == nil {
			contextErr = contextFailure(ctx)
		}
		return contextErr, clearErr
	}
}

// negotiationPhase bounds the negotiation by the option's limit and returns the
// function that lifts that limit before the target request is written.
func negotiationPhase(ctx context.Context, conn net.Conn, deadline time.Time, negotiation time.Duration) func() error {
	// targetPhase lifts the negotiation deadline before the target request is
	// written. Re-checking the context afterwards keeps a cancellation that
	// raced with the reset effective.
	targetPhase := func() error { return nil }
	if negotiation <= 0 {
		return targetPhase
	}
	limit := time.Now().Add(negotiation)
	if !deadline.IsZero() && !limit.Before(deadline) {
		return targetPhase
	}
	if err := conn.SetDeadline(limit); err != nil {
		return targetPhase
	}
	if ctx.Err() != nil {
		// The cancellation callback may have run before the limit
		// replaced its immediate deadline.
		_ = conn.SetDeadline(time.Now())
	}
	return func() error {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return conn.SetDeadline(time.Now())
		}
		return nil
	}
}

// handshakeProgress is the outcome of the protocol negotiation: the tunnel, the
// stage it reached, and the errors to report.
type handshakeProgress struct {
	result              net.Conn
	stage               Stage
	err                 error
	sourceErr           error
	operationContextErr error
}

// negotiate runs the TLS setup for https proxies and the scheme's handshake.
func negotiate(ctx context.Context, conn net.Conn, p Proxy, host string, port int, target string, opts handshakeOptions, targetPhase func() error) handshakeProgress {
	n := handshakeProgress{stage: StageHandshake}
	active := conn
	if strings.EqualFold(p.Scheme, "https") {
		n.stage = StageConnect
		tlsConn, err := startTLS(ctx, conn, p, opts)
		n.err = err
		if err != nil {
			n.sourceErr = err
			n.operationContextErr = contextFailure(ctx)
		} else {
			active = tlsConn
			n.stage = StageHandshake
		}
	}
	if n.err == nil {
		n.runScheme(ctx, active, p, host, port, target, targetPhase)
	}
	return n
}

// startTLS wraps conn in a verified TLS client session for the proxy host.
func startTLS(ctx context.Context, conn net.Conn, p Proxy, opts handshakeOptions) (*tls.Conn, error) {
	config := &tls.Config{}
	if opts.tlsConfig != nil {
		config = opts.tlsConfig.Clone()
	}
	config.ServerName = p.Host
	config.InsecureSkipVerify = false
	tlsConn := tls.Client(conn, config)
	return tlsConn, tlsConn.HandshakeContext(ctx)
}

// runScheme performs the handshake of the proxy's scheme over active.
func (n *handshakeProgress) runScheme(ctx context.Context, active net.Conn, p Proxy, host string, port int, target string, targetPhase func() error) {
	switch strings.ToLower(p.Scheme) {
	case "socks5":
		n.err = socks5Handshake(active, p, host, port, targetPhase)
		if n.err != nil {
			n.sourceErr = n.err
			n.operationContextErr = contextFailure(ctx)
		}
		n.result = active
	case "http", "https":
		if err := targetPhase(); err != nil {
			n.err = proxyError(StageHandshake, err)
		} else {
			n.result, n.err = httpConnectHandshake(active, p, target)
		}
		if n.err != nil {
			n.sourceErr = n.err
			n.operationContextErr = contextFailure(ctx)
		}
	default:
		n.err = errInvalidProxy
		n.stage = StageOther
	}
}

// failHandshake maps a failed negotiation to the handshake error, stopping the cancel watch first.
func failHandshake(n handshakeProgress, finish func() (contextErr, clearErr error)) error {
	if n.sourceErr == nil {
		n.sourceErr = n.err
	}
	_, clearErr := finish()
	if n.operationContextErr != nil {
		if clearErr != nil {
			n.operationContextErr = errors.Join(n.operationContextErr, clearErr)
		}
		return proxyErrorWithTarget(n.stage, 0, n.operationContextErr, errorTargetSent(n.sourceErr))
	}
	if clearErr != nil {
		var pe *Error
		if errors.As(n.sourceErr, &pe) {
			return proxyErrorWithTarget(pe.Stage, pe.Code, errors.Join(pe.Err, clearErr), pe.TargetSent)
		}
		return proxyError(n.stage, errors.Join(n.sourceErr, clearErr))
	}
	var pe *Error
	if errors.As(n.sourceErr, &pe) {
		return pe
	}
	return proxyError(n.stage, n.sourceErr)
}
