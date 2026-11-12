package proxyclient

import (
	"context"
	"errors"
	"net"
)

// Stage identifies the phase that produced an Error.
type Stage uint8

const (
	// StageConnect identifies proxy connection or TLS setup failures.
	StageConnect Stage = iota + 1
	// StageHandshake identifies malformed or incomplete proxy protocol exchanges.
	StageHandshake
	// StageRefused identifies proxy authentication or policy refusal.
	StageRefused
	// StageTarget identifies failures reaching the requested target.
	StageTarget
	// StageOther identifies invalid input, unclassified SOCKS5 reply codes or HTTP status codes, and other local failures.
	StageOther
)

// Error reports a proxy operation failure without exposing its underlying text.
type Error struct {
	// Stage identifies the failure phase.
	Stage Stage
	// Code is the SOCKS5 REP or HTTP status, or zero when unavailable.
	Code int
	// Err is the underlying cause and may contain sensitive details.
	Err error
	// TargetSent reports whether the target request may have been sent to the proxy.
	// It becomes true from the first attempted CONNECT write onward, and remains
	// false for earlier protocol negotiation and TLS setup failures.
	TargetSent bool
}

// Error returns a fixed, non-sensitive description of the failure stage.
func (e *Error) Error() string {
	if e == nil {
		return "proxy error"
	}
	switch e.Stage {
	case StageConnect:
		return "proxy connection failed"
	case StageHandshake:
		return "proxy handshake failed"
	case StageRefused:
		return "proxy refused the request"
	case StageTarget:
		return "proxy could not reach target"
	default:
		return "proxy error"
	}
}

// Unwrap returns the underlying error, if present.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Timeout reports whether err wraps a deadline or a timed-out network error.
func (e *Error) Timeout() bool {
	if e == nil {
		return false
	}
	if errors.Is(e.Err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(e.Err, &ne) && ne.Timeout()
}

// NextUnreachable reports whether the proxy explicitly could not reach the target.
func (e *Error) NextUnreachable() bool {
	return e != nil && e.Stage == StageTarget
}

func proxyError(stage Stage, code int, err error) error {
	return &Error{Stage: stage, Code: code, Err: err}
}

func targetSentError(stage Stage, code int, err error) error {
	return proxyErrorWithTarget(stage, code, err, true)
}

func proxyErrorWithTarget(stage Stage, code int, err error, targetSent bool) error {
	return &Error{Stage: stage, Code: code, Err: err, TargetSent: targetSent}
}

func errorTargetSent(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.TargetSent
}
