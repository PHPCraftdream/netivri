//go:build integration

package angel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func probeLiveWithControlBody(t *testing.T, body []byte) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	st := testState()
	st.Control = strings.TrimPrefix(server.URL, "http://")
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	_, _, err := ProbeLive(context.Background(), home)
	return home, err
}

func TestProbeLiveAcceptsStatusLargerThanStateLimit(t *testing.T) {
	body, err := json.Marshal(Status{
		Angel:    "running",
		PID:      testState().PID,
		Activity: []AccountActivity{{Login: strings.Repeat("a", maxStateSize)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= maxStateSize {
		t.Fatalf("status body is %d bytes, want more than %d", len(body), maxStateSize)
	}
	home, err := probeLiveWithControlBody(t, body)
	if err != nil {
		t.Fatalf("ProbeLive with large valid status: %v", err)
	}
	if !stateExists(home) {
		t.Error("successful probe removed the state file")
	}
}

func TestProbeLiveOversizedResponseKeepsState(t *testing.T) {
	body := []byte(`{"pid":4321}` + strings.Repeat(" ", maxControlResponseSize))
	home, err := probeLiveWithControlBody(t, body)
	if !errors.Is(err, errControlResponseTooLarge) {
		t.Fatalf("ProbeLive error = %v, want oversized response error", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("oversized response became ErrNotRunning: %v", err)
	}
	if !stateExists(home) {
		t.Error("oversized response removed the state file")
	}
}

func TestProbeLiveMalformedStatusKeepsState(t *testing.T) {
	home, err := probeLiveWithControlBody(t, []byte("{"))
	if err == nil {
		t.Fatal("ProbeLive accepted malformed status JSON")
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("malformed status became ErrNotRunning: %v", err)
	}
	if !stateExists(home) {
		t.Error("malformed status removed the state file")
	}
}

// An angel of another build answers a status this build cannot decode in
// full: the probes still see it as running (and keep its state file), so
// `down` can stop it and `up` does not spawn a second one.
func TestProbesSeeAnotherBuildAsRunning(t *testing.T) {
	home, err := probeLiveWithControlBody(t, []byte(oldBuildStatus))
	if !errors.Is(err, ErrStatusShape) {
		t.Fatalf("ProbeLive error = %v, want ErrStatusShape", err)
	}
	if !stateExists(home) {
		t.Fatal("ProbeLive removed the state file of a live angel of another build")
	}
	st, status, err := ProbeLive(context.Background(), home)
	if !errors.Is(err, ErrStatusShape) || st.PID != testState().PID || status.PID != st.PID || status.Version != "0.0.0-old" {
		t.Fatalf("ProbeLive = %+v, %+v, %v; want the state and the header with ErrStatusShape", st, status, err)
	}
	st, status, err = ProbeLiveKeep(context.Background(), home)
	if !errors.Is(err, ErrStatusShape) || st.PID != testState().PID || status.Listen != "127.0.0.1:7890" {
		t.Fatalf("ProbeLiveKeep = %+v, %+v, %v; want the state and the header with ErrStatusShape", st, status, err)
	}
	st, err = Probe(context.Background(), home)
	if err != nil || st.PID != testState().PID {
		t.Fatalf("Probe = %+v, %v; want the running angel", st, err)
	}
}

// A pid mismatch still wins over the shape: the answer comes from another
// angel than the state file names, so the file is stale.
func TestProbeLiveAnotherBuildWithOtherPIDIsNotRunning(t *testing.T) {
	body := strings.Replace(oldBuildStatus, `"pid":4321`, `"pid":99`, 1)
	home, err := probeLiveWithControlBody(t, []byte(body))
	if !errors.Is(err, ErrNotRunning) || errors.Is(err, ErrStatusShape) {
		t.Fatalf("ProbeLive error = %v, want ErrNotRunning only", err)
	}
	if stateExists(home) {
		t.Error("ProbeLive kept the state file of another angel's pid")
	}
}

func TestProbeLiveKeepAnotherBuildWithOtherPIDIsNotRunning(t *testing.T) {
	body := strings.Replace(oldBuildStatus, `"pid":4321`, `"pid":99`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	st := testState()
	st.Control = strings.TrimPrefix(server.URL, "http://")
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ProbeLiveKeep(context.Background(), home); !errors.Is(err, ErrNotRunning) || errors.Is(err, ErrStatusShape) {
		t.Fatalf("ProbeLiveKeep error = %v, want ErrNotRunning only", err)
	}
	if !stateExists(home) {
		t.Error("ProbeLiveKeep removed the state file")
	}
}

// The down flow against an angel of another build: probe, stop, then the
// probe reports it gone once its control port closes.
func TestStopAnotherBuildAngel(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/stop" {
			w.WriteHeader(http.StatusAccepted)
			close(stopped)
			return
		}
		_, _ = io.WriteString(w, oldBuildStatus)
	}))
	home := t.TempDir()
	st := testState()
	st.Control = strings.TrimPrefix(server.URL, "http://")
	if err := WriteState(home, st); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(context.Background(), home)
	if err != nil {
		t.Fatalf("Probe before stop: %v", err)
	}
	if err := (Client{Addr: got.Control, Token: got.Token}).Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-stopped
	server.Close()
	if _, err := Probe(context.Background(), home); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Probe after stop = %v, want ErrNotRunning", err)
	}
	if stateExists(home) {
		t.Error("the state file of the stopped angel stayed")
	}
}
