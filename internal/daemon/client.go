package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// ProbeTimeout bounds a single control request.
const ProbeTimeout = 2 * time.Second

// Client talks to the daemon control server. It never uses a configured or
// environment proxy and never follows redirects.
type Client struct {
	Addr  string
	Token string
}

func (c Client) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = c.Addr
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return req, nil
}

func (c Client) do(ctx context.Context, req *http.Request) ([]byte, int, error) {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: ProbeTimeout}).DialContext,
		DisableKeepAlives:     true,
		ExpectContinueTimeout: time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       ProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStateSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// Status asks the control server for the live status.
func (c Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/status")
	if err != nil {
		return Status{}, err
	}
	data, code, err := c.do(ctx, req)
	if err != nil {
		return Status{}, err
	}
	return parseControlStatus(data, code)
}

// Stop asks the daemon to shut down. The daemon answers 202 before stopping:
// a nil error means "stop accepted", not "stopped".
func (c Client) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/stop")
	if err != nil {
		return err
	}
	_, code, err := c.do(ctx, req)
	if err != nil {
		return err
	}
	if code != http.StatusAccepted {
		return fmt.Errorf("%w: HTTP %d", ErrControlRejected, code)
	}
	return nil
}

// Probe reports the state file of a running daemon, or ErrNotRunning.
func Probe(ctx context.Context, home string) (State, error) {
	st, _, err := ProbeLive(ctx, home)
	return st, err
}

// ProbeLive checks whether a daemon runs for this home: the state file AND
// a live control answer from the same pid are required, anything else is
// ErrNotRunning and a stale state file is removed best-effort. It returns the
// state file content and the live status.
func ProbeLive(ctx context.Context, home string) (State, Status, error) {
	st, err := ReadState(home)
	if err != nil {
		if !errors.Is(err, ErrNotRunning) {
			// A corrupt file is cleared. Accepted race: a successor daemon
			// could replace it between the read and the removal, which needs
			// the previous daemon to exit and a new one to start in between.
			_ = os.Remove(StatePath(home))
		}
		return State{}, Status{}, ErrNotRunning
	}
	status, err := (Client{Addr: st.Control, Token: st.Token}).Status(ctx)
	if err != nil || status.PID != st.PID {
		RemoveStateIf(home, st.PID, st.Token)
		return State{}, Status{}, ErrNotRunning
	}
	return st, status, nil
}
