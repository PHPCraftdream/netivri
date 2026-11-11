package proxyclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

type timeoutCause struct{}

func (timeoutCause) Error() string   { return testPassword }
func (timeoutCause) Timeout() bool   { return true }
func (timeoutCause) Temporary() bool { return false }

func TestErrorFormattingUnwrapAndClassification(t *testing.T) {
	secretCause := errors.New("failure for " + testUser + " with " + testPassword + " at " + testTarget)
	wrappedCause := fmt.Errorf("sensitive wrapping text: %w", secretCause)
	cases := []struct {
		name  string
		stage Stage
		text  string
	}{
		{"connect", StageConnect, "proxy connection failed"},
		{"handshake", StageHandshake, "proxy handshake failed"},
		{"refused", StageRefused, "proxy refused the request"},
		{"target", StageTarget, "proxy could not reach target"},
		{"other", StageOther, "proxy error"},
		{"unknown", Stage(255), "proxy error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := &Error{Stage: tc.stage, Code: 503, Err: wrappedCause}
			if pe.Error() != tc.text {
				t.Fatal("proxy error formatting mismatch")
			}
			for _, secret := range []string{testUser, testPassword, "unresolved-target", "sensitive wrapping text"} {
				if contains(pe.Error(), secret) {
					t.Fatal("formatted proxy error exposed underlying details")
				}
			}
			if !errors.Is(pe, secretCause) {
				t.Fatal("proxy error did not unwrap to its cause")
			}
			var got *Error
			if !errors.As(pe, &got) || got != pe {
				t.Fatal("proxy error did not support errors.As")
			}
			if pe.NextUnreachable() != (tc.stage == StageTarget) {
				t.Fatal("NextUnreachable classification mismatch")
			}
			if pe.Timeout() {
				t.Fatal("ordinary cause classified as timeout")
			}
		})
	}
}

func TestErrorTimeoutClassification(t *testing.T) {
	deadline := &Error{Stage: StageHandshake, Err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)}
	if !deadline.Timeout() || !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("wrapped deadline was not recognized")
	}
	var netErr net.Error = timeoutCause{}
	if !(&Error{Err: fmt.Errorf("wrapped: %w", netErr)}).Timeout() {
		t.Fatal("wrapped network timeout was not recognized")
	}
	if (&Error{Err: errors.New("ordinary failure")}).Timeout() {
		t.Fatal("ordinary error classified as timeout")
	}
	var nilError *Error
	if nilError.Timeout() || nilError.NextUnreachable() || nilError.Unwrap() != nil || nilError.Error() != "proxy error" {
		t.Fatal("nil error receiver behavior mismatch")
	}
	if (&Error{Stage: StageTarget}).NextUnreachable() != true {
		t.Fatal("target stage not classified unreachable")
	}
	if (&Error{Stage: StageConnect}).NextUnreachable() {
		t.Fatal("non-target stage classified unreachable")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
