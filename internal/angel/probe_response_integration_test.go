//go:build integration

package angel

import (
	"context"
	"encoding/json"
	"errors"
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
