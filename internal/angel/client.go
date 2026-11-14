package angel

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

// Client talks to the angel control server. It never uses a configured or
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

var errControlResponseTooLarge = errors.New("control response exceeds size limit")

const maxControlResponseSize = 4 << 20

func readControlResponse(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxControlResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxControlResponseSize {
		return nil, fmt.Errorf("%w: more than %d bytes", errControlResponseTooLarge, maxControlResponseSize)
	}
	return data, nil
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
	data, err := readControlResponse(resp.Body)
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

// Stop asks the angel to shut down. The angel answers 202 before stopping:
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

// ProbeLiveKeep is ProbeLive without any cleanup: a failed control request
// leaves the state file alone and is reported as the request error, a pid
// mismatch is a wrapped ErrNotRunning (the angel the state file names is
// gone), and a missing state file is ErrNotRunning. The status watch polls
// through it, so a transient control timeout stays a retryable failure
// instead of ending the watch and deleting the state.
func ProbeLiveKeep(ctx context.Context, home string) (State, Status, error) {
	st, err := ReadState(home)
	if err != nil {
		return State{}, Status{}, err
	}
	status, err := (Client{Addr: st.Control, Token: st.Token}).Status(ctx)
	shapeErr := statusShapeErr(err)
	if shapeErr != nil {
		err = nil
	}
	if err != nil {
		if isConnRefused(err) {
			// Nothing listens on the recorded control port: the angel
			// the state file names is gone (a crash leaves the file).
			return st, Status{}, fmt.Errorf("%w: nothing listens on %s: %v",
				ErrNotRunning, st.Control, err)
		}
		return st, Status{}, err
	}
	if status.PID != st.PID {
		return st, Status{}, fmt.Errorf("%w: control answers for pid %d, the state file names pid %d",
			ErrNotRunning, status.PID, st.PID)
	}
	return st, status, shapeErr
}

// statusShapeErr returns err when it is an ErrStatusShape answer, else nil.
func statusShapeErr(err error) error {
	if errors.Is(err, ErrStatusShape) {
		return err
	}
	return nil
}

// Probe reports the state file of a running angel, or ErrNotRunning. An angel
// of another build (ErrStatusShape) is running: it needs only a live pid.
func Probe(ctx context.Context, home string) (State, error) {
	st, _, err := ProbeLive(ctx, home)
	if errors.Is(err, ErrStatusShape) {
		err = nil
	}
	return st, err
}

// ErrControlTimeout reports that the control endpoint the state file names
// accepted the request but did not answer in time. The angel is most likely
// alive and busy (a reload or core restart): its state file is kept and the
// caller should retry instead of treating the angel as gone.
var ErrControlTimeout = errors.New("angel control did not answer in time")

// isTimeout reports a deadline or a network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// ProbeLive checks whether an angel runs for this home: the state file AND
// a live control answer from the same pid are required. A refused connection
// or pid mismatch is ErrNotRunning and a stale state file is removed
// best-effort. Control errors without positive evidence that the angel is gone
// leave the state file in place. A control request that merely times out wraps
// ErrControlTimeout. It returns the state file content and the live status.
// An angel of another build returns the state file and its status header with
// a wrapped ErrStatusShape.
func ProbeLive(ctx context.Context, home string) (State, Status, error) {
	st, err := ReadState(home)
	if err != nil {
		if !errors.Is(err, ErrNotRunning) {
			// A corrupt file is cleared. Accepted race: a successor angel
			// could replace it between the read and the removal, which needs
			// the previous angel to exit and a new one to start in between.
			_ = os.Remove(StatePath(home))
		}
		return State{}, Status{}, ErrNotRunning
	}
	status, err := (Client{Addr: st.Control, Token: st.Token}).Status(ctx)
	shapeErr := statusShapeErr(err)
	if shapeErr != nil {
		err = nil
	}
	if err != nil {
		if isConnRefused(err) {
			RemoveStateIf(home, st.PID, st.Token)
			return State{}, Status{}, ErrNotRunning
		}
		if isTimeout(err) {
			return st, Status{}, fmt.Errorf("%w: %v", ErrControlTimeout, err)
		}
		return st, Status{}, err
	}
	if status.PID != st.PID {
		RemoveStateIf(home, st.PID, st.Token)
		return State{}, Status{}, ErrNotRunning
	}
	return st, status, shapeErr
}
