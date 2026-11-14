package angel

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// oldBuildStatus is a status answer of an older build: pre_socks5 was a
// string there, so the body does not decode into this build's Status.
const oldBuildStatus = `{"angel":"running","foreground":false,"pid":4321,` +
	`"listen":"127.0.0.1:7890","protocols":["socks5","http"],"auth":"enabled",` +
	`"started_at":"2026-11-01T10:00:00Z","version":"0.0.0-old",` +
	`"accounts":[{"login":"alice","chain":{"pre_socks5":"none"}}]}`

func TestParseControlStatusOtherBuildKeepsHeader(t *testing.T) {
	st, err := parseControlStatus([]byte(oldBuildStatus), http.StatusOK)
	if !errors.Is(err, ErrStatusShape) {
		t.Fatalf("err = %v, want ErrStatusShape", err)
	}
	want := Status{
		Angel: "running", PID: 4321, Listen: "127.0.0.1:7890",
		Protocols: []string{"socks5", "http"}, Auth: "enabled",
		StartedAt: "2026-11-01T10:00:00Z", Version: "0.0.0-old",
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("status = %+v, want header only %+v", st, want)
	}
	for _, part := range []string{"pid 4321", "version 0.0.0-old", "netivri down, then netivri up", "pre_socks5"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q lacks %q", err, part)
		}
	}
}

func TestParseControlStatusOtherBuildWithoutVersion(t *testing.T) {
	body := `{"pid":7,"accounts":"not a list"}`
	st, err := parseControlStatus([]byte(body), http.StatusOK)
	if !errors.Is(err, ErrStatusShape) || st.PID != 7 || st.Version != "" {
		t.Fatalf("status = %+v, err = %v; want pid 7, empty version, ErrStatusShape", st, err)
	}
	if !strings.Contains(err.Error(), "version unknown") {
		t.Errorf("error %q lacks %q", err, "version unknown")
	}
}

// Without a usable pid the answer proves no live angel: it stays a plain
// decode error, never ErrStatusShape.
func TestParseControlStatusUnusableHeaderIsPlainError(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `{`,
		"no pid":       `{"accounts":"x"}`,
		"zero pid":     `{"pid":0,"accounts":"x"}`,
		"negative pid": `{"pid":-3,"accounts":"x"}`,
		"bad header":   `{"pid":5,"protocols":"socks5"}`,
		"string pid":   `{"pid":"5"}`,
	} {
		t.Run(name, func(t *testing.T) {
			st, err := parseControlStatus([]byte(body), http.StatusOK)
			if err == nil || errors.Is(err, ErrStatusShape) {
				t.Fatalf("err = %v, want a plain decode error", err)
			}
			if !strings.HasPrefix(err.Error(), "decode status answer: ") {
				t.Errorf("err = %q, want the decode status answer prefix", err)
			}
			if !reflect.DeepEqual(st, Status{}) {
				t.Errorf("status = %+v, want zero", st)
			}
		})
	}
}

func TestParseControlStatusRejectedCodeIgnoresBody(t *testing.T) {
	_, err := parseControlStatus([]byte(oldBuildStatus), http.StatusUnauthorized)
	if !errors.Is(err, ErrControlRejected) || errors.Is(err, ErrStatusShape) {
		t.Fatalf("err = %v, want ErrControlRejected only", err)
	}
}

func TestStatusShapeErr(t *testing.T) {
	shape := errors.Join(ErrStatusShape, errors.New("detail"))
	if statusShapeErr(shape) != shape {
		t.Error("statusShapeErr dropped a shape error")
	}
	for _, err := range []error{nil, errors.New("other"), ErrNotRunning, ErrControlTimeout} {
		if got := statusShapeErr(err); got != nil {
			t.Errorf("statusShapeErr(%v) = %v, want nil", err, got)
		}
	}
}
