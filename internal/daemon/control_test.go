package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func ctlRequest(t *testing.T, s *Server, method, path, host, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	s.handle(rec, req)
	return rec
}

func newTestServer(t *testing.T) (*Server, *int32) {
	t.Helper()
	var calls int32
	s := &Server{addr: "127.0.0.1:12345", token: "secret", status: func() Status { return Status{Daemon: "running", PID: 7, Listen: "127.0.0.1:7890"} }, stop: func() { atomic.AddInt32(&calls, 1) }}
	t.Cleanup(func() { _ = s.Close() })
	return s, &calls
}

func TestControlUnauthorized(t *testing.T) {
	s, _ := newTestServer(t)
	for _, auth := range []string{"", "Basic secret", "Bearer nope", "Bearer "} {
		rec := ctlRequest(t, s, http.MethodGet, "/v1/status", s.addr, auth)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status=%d want 401", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["ok"] != false {
			t.Errorf("body=%v", body)
		}
	}
}

func TestControlHostCheck(t *testing.T) {
	s, _ := newTestServer(t)
	if rec := ctlRequest(t, s, http.MethodGet, "/v1/status", "evil.example:12345", "Bearer secret"); rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestControlRouting(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodGet, "/v1/status", 200}, {http.MethodPost, "/v1/status", 405}, {http.MethodGet, "/v1/stop", 405}, {http.MethodGet, "/other", 404}} {
		if rec := ctlRequest(t, s, tc.method, tc.path, s.addr, "Bearer secret"); rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestControlStatusBody(t *testing.T) {
	s, _ := newTestServer(t)
	s.status = func() Status {
		return Status{Daemon: "running", PID: 7, Listen: "127.0.0.1:7890", Accounts: []AccountInfo{{Login: "alice", ExitCountry: "DE"}}}
	}
	rec := ctlRequest(t, s, http.MethodGet, "/v1/status", s.addr, "Bearer secret")
	if !strings.Contains(rec.Body.String(), `"accounts":[{"login":"alice","exit_country":"DE"}]`) || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("unexpected status json: %s", rec.Body.String())
	}
	st, err := parseControlStatus(rec.Body.Bytes(), rec.Code)
	if err != nil {
		t.Fatal(err)
	}
	if st.Daemon != "running" || st.PID != 7 || st.Listen != "127.0.0.1:7890" {
		t.Errorf("status=%+v", st)
	}
}

func TestControlStatusOmissionAndEmptyCountry(t *testing.T) {
	s, _ := newTestServer(t)
	s.status = func() Status { return Status{Accounts: []AccountInfo{{Login: "alice", ExitCountry: ""}}} }
	rec := ctlRequest(t, s, http.MethodGet, "/v1/status", s.addr, "Bearer secret")
	if !strings.Contains(rec.Body.String(), `"accounts":[{"login":"alice","exit_country":""}]`) {
		t.Fatalf("empty country key omitted: %s", rec.Body.String())
	}
	s.status = func() Status { return Status{} }
	rec = ctlRequest(t, s, http.MethodGet, "/v1/status", s.addr, "Bearer secret")
	if strings.Contains(rec.Body.String(), `"accounts"`) {
		t.Fatalf("empty accounts should be omitted: %s", rec.Body.String())
	}
}

func TestControlStopCalledOnce(t *testing.T) {
	var calls int32
	rec := httptest.NewRecorder()
	s := &Server{addr: "127.0.0.1:12345", token: "secret", status: func() Status { return Status{} }, stop: func() {
		atomic.AddInt32(&calls, 1)
		if !rec.Flushed {
			t.Error("response was not flushed before stop callback")
		}
		if rec.Code != http.StatusAccepted {
			t.Errorf("status=%d want 202 before stop callback", rec.Code)
		}
		if rec.Body.String() != "{\"stopping\":true}\n" {
			t.Errorf("body=%q want stopping response before callback", rec.Body.String())
		}
	}}
	req := httptest.NewRequest(http.MethodPost, "http://"+s.addr+"/v1/stop", nil)
	req.Host = s.addr
	req.Header.Set("Authorization", "Bearer secret")
	s.handle(rec, req)
	if rec.Code != http.StatusAccepted || !rec.Flushed || rec.Body.String() != "{\"stopping\":true}\n" {
		t.Errorf("first response: code=%d flushed=%v body=%q", rec.Code, rec.Flushed, rec.Body.String())
	}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://"+s.addr+"/v1/stop", nil)
		req.Host = s.addr
		req.Header.Set("Authorization", "Bearer secret")
		s.handle(rec, req)
		if rec.Code != http.StatusAccepted || !rec.Flushed || rec.Body.String() != "{\"stopping\":true}\n" {
			t.Errorf("response %d: code=%d flushed=%v body=%q", i+2, rec.Code, rec.Flushed, rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls=%d want 1", got)
	}
}

func TestParseControlStatusErrors(t *testing.T) {
	if _, err := parseControlStatus([]byte(`{}`), 500); err == nil {
		t.Error("non-200 accepted")
	}
	if _, err := parseControlStatus([]byte(`not json`), 200); err == nil {
		t.Error("invalid JSON accepted")
	}
}
