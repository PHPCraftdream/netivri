package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// NewToken returns 32 random bytes as hex: the local control secret.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AccountInfo describes an account's configured provider and exit-country route.
type AccountInfo struct {
	Login       string `json:"login"`
	Provider    string `json:"provider"`
	ExitCountry string `json:"exit_country"`
}

// NodeHealth is the last probe result of one node proxy.
type NodeHealth struct {
	Name    string `json:"name"` // internal proxy name, never the provider name
	Alive   bool   `json:"alive"`
	DelayMS int64  `json:"delay_ms"` // 0 until a probe succeeded
}

// WarmInfo is the warm-up state of the running instance.
type WarmInfo struct {
	Probes        uint64       `json:"probes"`
	ProbeFailures uint64       `json:"probe_failures"`
	PoolHits      uint64       `json:"pool_hits"`
	PoolMisses    uint64       `json:"pool_misses"`
	PoolDiscards  uint64       `json:"pool_discards"`
	Nodes         []NodeHealth `json:"nodes"`
}

// StatsInfo describes the node statistics store of the running instance.
type StatsInfo struct {
	Path    string `json:"path"`
	Dropped uint64 `json:"dropped"` // samples lost to a full write queue
}

// AffinityInfo counts the site-to-node bindings of the running instance and
// its hedged dials.
type AffinityInfo struct {
	Bindings  int    `json:"bindings"`
	Rebinds   uint64 `json:"rebinds"`
	Hedges    uint64 `json:"hedges"`     // hedge attempts started
	HedgeWins uint64 `json:"hedge_wins"` // dials won by the hedge attempt
}

// RefreshInfo reports the automatic subscription refresh of the daemon.
type RefreshInfo struct {
	IntervalSeconds int64  `json:"interval_seconds"` // 0 when the refresh is off
	OK              uint64 `json:"ok"`               // refreshes that stored a new snapshot
	Failed          uint64 `json:"failed"`           // refreshes that failed or were rejected
	LastAt          string `json:"last_at,omitempty"`
	LastError       string `json:"last_error,omitempty"` // code and message, never a URL
}

// ReloadInfo reports how the daemon applied changed files to the running core.
type ReloadInfo struct {
	Count     uint64 `json:"count"` // restarts of the core caused by a changed configuration
	LastAt    string `json:"last_at,omitempty"`
	LastError string `json:"last_error,omitempty"` // why the newest files are not applied
}

// Status is the live answer of GET /v1/status.
type Status struct {
	Daemon     string        `json:"daemon"`
	Foreground bool          `json:"foreground"`
	PID        int           `json:"pid"`
	Listen     string        `json:"listen"`
	Protocols  []string      `json:"protocols"`
	Auth       string        `json:"auth"`
	Route      string        `json:"route"`
	StartedAt  string        `json:"started_at"`
	Version    string        `json:"version"`
	Accounts   []AccountInfo `json:"accounts,omitempty"`
	Warm       *WarmInfo     `json:"warm,omitempty"`
	Stats      *StatsInfo    `json:"stats,omitempty"`
	Affinity   *AffinityInfo `json:"affinity,omitempty"`
	Refresh    *RefreshInfo  `json:"refresh,omitempty"`
	Reload     *ReloadInfo   `json:"reload,omitempty"`
}

// Server is the loopback control HTTP server. It always binds 127.0.0.1:0,
// independent of the proxy bind address.
type Server struct {
	ln     net.Listener
	srv    *http.Server
	addr   string // "127.0.0.1:<port>"
	token  string
	status func() Status
	stop   func()
	once   sync.Once
}

// StartServer starts the control server. stop is invoked at most once, by
// the first POST /v1/stop.
func StartServer(token string, status func() Status, stop func()) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		return nil, fmt.Errorf("listen control port: %w", err)
	}
	s := &Server{ln: ln, addr: ln.Addr().String(), token: token, status: status, stop: stop}
	s.srv = &http.Server{
		Handler:           http.MaxBytesHandler(http.HandlerFunc(s.handle), 1<<20),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr returns the 127.0.0.1:<port> address the server listens on.
func (s *Server) Addr() string { return s.addr }

// Close shuts the server down gracefully (in-flight requests, such as the
// stop answer, finish) and forces it after two seconds. A server that never
// started closes as a no-op.
func (s *Server) Close() error {
	if s.srv == nil || s.ln == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		return s.srv.Close()
	}
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// Cheap DNS-rebinding guard: only the exact loopback host:port of this
	// listener is accepted.
	if r.Host != s.addr {
		writeCtlErr(w, http.StatusForbidden, "FORBIDDEN_HOST", "forbidden")
		return
	}
	if !s.authorized(r) {
		writeCtlErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	switch {
	case r.URL.Path == "/v1/status" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.status())
	case r.URL.Path == "/v1/stop" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusAccepted, map[string]bool{"stopping": true})
		// The answer must reach the client before the daemon starts to stop.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.once.Do(s.stop)
	case r.URL.Path == "/v1/status" || r.URL.Path == "/v1/stop":
		writeCtlErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	default:
		writeCtlErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
	}
}

// authorized checks the bearer token in constant time.
func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	got := h[len(prefix):]
	return len(got) == len(s.token) && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeCtlErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": map[string]string{"code": errCode, "message": msg}})
}

// ErrControlRejected marks a non-2xx control answer.
var ErrControlRejected = errors.New("control request rejected")

// parseControlStatus decodes a /v1/status answer.
func parseControlStatus(data []byte, code int) (Status, error) {
	if code != http.StatusOK {
		return Status{}, fmt.Errorf("%w: HTTP %d", ErrControlRejected, code)
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{}, fmt.Errorf("decode status answer: %w", err)
	}
	return st, nil
}
