package main

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/daemon"
)

// Seams over internal/daemon: the cmd-level decisions (idempotency,
// ALREADY_RUNNING rules, not-running outputs) are unit-tested without
// sockets or processes, following the subFetch pattern.
var (
	daemonProbe       = daemon.Probe
	daemonProbeLive   = daemon.ProbeLive
	daemonSpawn       = daemon.Spawn
	daemonControlStop = func(ctx context.Context, addr, token string) error {
		return (daemon.Client{Addr: addr, Token: token}).Stop(ctx)
	}
)

// daemonLog is written by the daemon child only, whose stderr is the
// append-only run/netivri.log. It never carries credentials or tokens.
var daemonLog = log.New(os.Stderr, "netivri-daemon ", log.LstdFlags|log.LUTC)

// daemonProtocols are the protocols the mixed port speaks.
var daemonProtocols = []string{"socks5", "socks4", "http"}

// filterUpArgs builds the daemon child's command line: `up`, the user's
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
// bind differs from the running daemon's listen address. An explicit
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

// alreadyRunningData is the success data of the daemon `up`: the keys of
// `up --foreground` plus the additive `foreground` and `already_running`.
func alreadyRunningData(st daemon.Status, already bool) map[string]any {
	return map[string]any{
		"daemon":          st.Daemon,
		"pid":             st.PID,
		"listen":          st.Listen,
		"protocols":       st.Protocols,
		"auth":            st.Auth,
		"foreground":      st.Foreground,
		"already_running": already,
	}
}

// statusOf is the status view of a state file written at startup.
func statusOf(st daemon.State) daemon.Status {
	return daemon.Status{
		Daemon:     "running",
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
func stateOf(s daemon.Status, control, token string) daemon.State {
	return daemon.State{
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

// failStartup reports a startup failure of the daemon child to its parent
// through the handshake file (no-op without a handshake) and returns err.
func failStartup(handshake string, err error) error {
	if handshake == "" {
		return err
	}
	if werr := daemon.WriteHandshakeError(handshake, daemon.NewHandshakeError(err)); werr != nil {
		daemonLog.Printf("write handshake failed: %v", werr)
	}
	return err
}
