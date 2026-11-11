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
	tlsConfig *tls.Config
}

// WithTLSConfig supplies TLS settings for HTTPS proxies. Certificate verification remains enabled.
// The configuration is cloned before use.
func WithTLSConfig(config *tls.Config) Option {
	return func(o *handshakeOptions) { o.tlsConfig = config }
}

// Handshake establishes a tunnel over conn. The caller remains responsible for closing conn on success and failure.
func Handshake(ctx context.Context, conn net.Conn, p Proxy, target string, opts ...Option) (net.Conn, error) {
	if ctx == nil || conn == nil {
		return nil, proxyError(StageOther, 0, errInvalidTarget)
	}
	if err := validateProxy(p); err != nil {
		return nil, proxyError(StageOther, 0, err)
	}
	host, port, err := validateTarget(target)
	if err != nil {
		return nil, proxyError(StageOther, 0, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		return nil, proxyError(StageConnect, 0, ctxErr)
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
		return nil, proxyError(StageOther, 0, errInvalidProxy)
	}
	if err := validateProxy(p); err != nil {
		return nil, proxyError(StageOther, 0, err)
	}
	if _, _, err := validateTarget(target); err != nil {
		return nil, proxyError(StageOther, 0, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		return nil, proxyError(StageConnect, 0, ctxErr)
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		if ctxErr := contextFailure(ctx); ctxErr != nil {
			return nil, proxyError(StageConnect, 0, ctxErr)
		}
		return nil, proxyError(StageConnect, 0, err)
	}
	if ctxErr := contextFailure(ctx); ctxErr != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, proxyError(StageConnect, 0, ctxErr)
	}
	if conn == nil {
		return nil, proxyError(StageConnect, 0, errInvalidProxy)
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
		clearErr := conn.SetDeadline(time.Time{})
		if ctxErr := contextFailure(ctx); ctxErr != nil {
			if clearErr != nil {
				ctxErr = errors.Join(ctxErr, clearErr)
			}
			return nil, proxyError(StageConnect, 0, ctxErr)
		}
		if clearErr != nil {
			return nil, proxyError(StageConnect, 0, errors.Join(err, clearErr))
		}
		return nil, proxyError(StageConnect, 0, err)
	}
	callbackDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		close(callbackDone)
	})
	finish := func() (contextErr, clearErr error) {
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

	active := conn
	stage := StageHandshake
	var result net.Conn
	var err error
	var sourceErr error
	var operationContextErr error
	if strings.EqualFold(p.Scheme, "https") {
		stage = StageConnect
		config := &tls.Config{}
		if opts.tlsConfig != nil {
			config = opts.tlsConfig.Clone()
		}
		config.ServerName = p.Host
		config.InsecureSkipVerify = false
		tlsConn := tls.Client(conn, config)
		err = tlsConn.HandshakeContext(ctx)
		if err != nil {
			sourceErr = err
			operationContextErr = contextFailure(ctx)
		}
		if err == nil {
			active = tlsConn
			stage = StageHandshake
		}
	}
	if err == nil {
		switch strings.ToLower(p.Scheme) {
		case "socks5":
			err = socks5Handshake(active, p, host, port)
			if err != nil {
				sourceErr = err
				operationContextErr = contextFailure(ctx)
			}
			result = active
		case "http", "https":
			result, err = httpConnectHandshake(active, p, target)
			if err != nil {
				sourceErr = err
				operationContextErr = contextFailure(ctx)
			}
		default:
			err = errInvalidProxy
			stage = StageOther
		}
	}
	if err != nil {
		if sourceErr == nil {
			sourceErr = err
		}
		_, clearErr := finish()
		if operationContextErr != nil {
			if clearErr != nil {
				operationContextErr = errors.Join(operationContextErr, clearErr)
			}
			return nil, proxyError(stage, 0, operationContextErr)
		}
		if clearErr != nil {
			var pe *Error
			if errors.As(sourceErr, &pe) {
				return nil, proxyError(pe.Stage, pe.Code, errors.Join(pe.Err, clearErr))
			}
			return nil, proxyError(stage, 0, errors.Join(sourceErr, clearErr))
		}
		var pe *Error
		if errors.As(sourceErr, &pe) {
			return nil, pe
		}
		return nil, proxyError(stage, 0, sourceErr)
	}
	contextErr, clearErr := finish()
	if contextErr != nil {
		if clearErr != nil {
			contextErr = errors.Join(contextErr, clearErr)
		}
		return nil, proxyError(stage, 0, contextErr)
	}
	if clearErr != nil {
		return nil, proxyError(StageConnect, 0, clearErr)
	}
	return result, nil
}
