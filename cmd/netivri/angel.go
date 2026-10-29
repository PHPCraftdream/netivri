package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
)

// Seams over internal/angel: the cmd-level decisions (idempotency,
// ALREADY_RUNNING rules, not-running outputs) are unit-tested without
// sockets or processes, following the subFetch pattern.
var (
	angelProbe     = angel.Probe
	angelProbeLive = angel.ProbeLive
	// angelProbeLiveKeep is the status watch's poll: it never removes the
	// state file, so a transient control failure stays retryable.
	angelProbeLiveKeep = angel.ProbeLiveKeep
	angelSpawn         = angel.Spawn
	angelControlStop   = func(ctx context.Context, addr, token string) error {
		return (angel.Client{Addr: addr, Token: token}).Stop(ctx)
	}
)

// angelLogf returns the writer of one level of the angel log. It is used by
// the angel child only, whose stderr is the append-only run/netivri.log
// (`netivri logs` reads it). It never carries credentials or tokens.
func angelLogf(level string) func(format string, args ...any) {
	return func(format string, args ...any) {
		line := angel.FormatLogLine(time.Now(), level, fmt.Sprintf(format, args...))
		_, _ = io.WriteString(os.Stderr, line+"\n")
	}
}

var (
	angelDebug = angelLogf(angel.LevelDebug)
	angelInfo  = angelLogf(angel.LevelInfo)
	angelWarn  = angelLogf(angel.LevelWarn)
	angelError = angelLogf(angel.LevelError)
)

// angelProtocols are the protocols the mixed port speaks.
var angelProtocols = []string{"socks5", "socks4", "http"}

// filterUpArgs builds the angel child's command line: `up`, the user's
// arguments verbatim minus the global compatibility flags and any --home,
// and the resolved --home so the child uses the same state directory.
func filterUpArgs(args []string, home string) []string {
	out := []string{"up"}
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		switch a {
		case "--home", "-home":
			skipNext = true
			continue
		case "--json", "-json", "--quiet", "-quiet", "--no-color", "-no-color":
			continue
		}
		if strings.HasPrefix(a, "--home=") || strings.HasPrefix(a, "-home=") {
			continue
		}
		out = append(out, a)
	}
	return append(out, "--home", home)
}

// conflictsWithRunning reports whether an explicitly requested port or
// bind differs from the running angel's listen address. An explicit
// `--port 0` means "don't care".
func conflictsWithRunning(opts upOptions, listen string) bool {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return true
	}
	if opts.PortSet && opts.Port != 0 {
		if p, err := strconv.Atoi(port); err != nil || p != opts.Port {
			return true
		}
	}
	if opts.BindSet && opts.Bind != host && !(opts.Bind == "localhost" && isLoopbackHost(host)) {
		return true
	}
	return false
}

// alreadyRunningData is the success data of the angel `up`: the keys of
// `up --foreground` plus the additive `foreground` and `already_running`.
func alreadyRunningData(st angel.Status, already bool) map[string]any {
	return map[string]any{
		"angel":           st.Angel,
		"pid":             st.PID,
		"listen":          st.Listen,
		"protocols":       st.Protocols,
		"auth":            st.Auth,
		"foreground":      st.Foreground,
		"already_running": already,
	}
}

// statusOf is the status view of a state file written at startup.
func statusOf(st angel.State) angel.Status {
	return angel.Status{
		Angel:      "running",
		Foreground: st.Foreground,
		PID:        st.PID,
		Listen:     st.Listen,
		Protocols:  st.Protocols,
		Auth:       st.Auth,
		Route:      st.Route,
		StartedAt:  st.StartedAt,
		Version:    st.Version,
	}
}

// stateOf builds the state file content from a live status.
func stateOf(s angel.Status, control, token string) angel.State {
	return angel.State{
		PID:        s.PID,
		Listen:     s.Listen,
		Control:    control,
		Token:      token,
		Foreground: s.Foreground,
		StartedAt:  s.StartedAt,
		Version:    s.Version,
		Auth:       s.Auth,
		Protocols:  s.Protocols,
		Route:      s.Route,
	}
}

// failStartup reports a startup failure of the angel child to its parent
// through the handshake file (no-op without a handshake) and returns err.
func failStartup(handshake string, err error) error {
	if handshake == "" {
		return err
	}
	if werr := angel.WriteHandshakeError(handshake, angel.NewHandshakeError(err)); werr != nil {
		angelError("write handshake failed: %v", werr)
	}
	return err
}
