// Package core embeds Mihomo as a library and exposes it as a local
// dual-protocol (HTTP + SOCKS4/5) listener. See docs/design/mixed-port.md.
//
// With Options.Node set, Start builds a rule-mode config whose single
// MATCH rule routes every connection through the selected upstream node
// (fixed internal name "netivri-node"); domain destinations are passed to
// the node unresolved. There is no direct fallback: if the node is
// unreachable, connections fail. The node is chosen at Start time only;
// switching it while the instance is running is out of scope for now.
package core

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	auth "github.com/metacubex/mihomo/component/auth"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	authStore "github.com/metacubex/mihomo/listener/auth"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/listener/mixed"
	mihomolog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	logrus "github.com/sirupsen/logrus"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

// Options configures Start.
type Options struct {
	HomeDir       string             // required; created if missing, passed to constant.SetHomeDir
	Bind          string             // default "127.0.0.1"
	Port          int                // 0 = random free port
	Authenticator auth.Authenticator // nil = no authentication
	// Node optionally routes every connection accepted by the mixed listener
	// through this Mihomo proxy mapping (a Clash `proxies:` entry as produced
	// by internal/subscription). nil keeps Direct mode. The node is fixed for
	// the lifetime of the instance; hot-switching is not supported yet.
	Node map[string]any
}

// Instance is one running proxy listener.
type Instance struct {
	mu     sync.Mutex
	closed bool
	ln     *mixed.Listener
	done   chan struct{} // closed by Close; stops the ctx watcher
	tt     *trackedTunnel
	route  string // "direct" or "node"; immutable after Start publishes the instance
}

var (
	// lifecycleMu guards Mihomo's process-global state: it is held for the
	// whole of Start (check, config, listen, claim) and the shutdown part of
	// Close. Never taken while an instance lock is held.
	lifecycleMu sync.Mutex
	current     *Instance
	logOnce     sync.Once
	// everApplied is set after the first configuration was applied in this
	// process; guarded by lifecycleMu.
	everApplied bool
)

// silenceMihomoLog keeps Mihomo's logger off stdout: stdout carries only the
// JSON envelope. Even at SILENT level logrus could still be invoked directly,
// so its output is discarded as well.
func silenceMihomoLog() {
	logOnce.Do(func() {
		logrus.SetOutput(io.Discard)
		logrus.SetLevel(logrus.PanicLevel)
		mihomolog.SetLevel(mihomolog.SILENT)
	})
}

// Start applies a minimal direct-mode Mihomo config (all executor listeners
// on port 0, so Mihomo opens none) and opens netivri's own mixed listener.
// Mihomo holds process-global state, so only one Instance may be open at a
// time; Close it before starting another.
func Start(ctx context.Context, o Options) (*Instance, error) {
	if o.HomeDir == "" {
		return nil, errors.New("core: HomeDir is required")
	}
	// constant.SetHomeDir expects an existing directory.
	if err := os.MkdirAll(o.HomeDir, 0o700); err != nil {
		return nil, err
	}

	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if current != nil {
		return nil, errors.New("core: an instance is already running in this process")
	}

	silenceMihomoLog()
	C.SetHomeDir(o.HomeDir)

	raw := config.DefaultRawConfig()
	raw.Mode = tunnel.Direct
	raw.LogLevel = mihomolog.SILENT
	raw.Port, raw.SocksPort, raw.RedirPort, raw.TProxyPort, raw.MixedPort = 0, 0, 0, 0, 0
	raw.ExternalController = ""
	raw.AllowLan = false
	// Nothing is selected or cached in v1: no state database, no geodata
	// updates, nothing to persist across runs.
	raw.Profile.StoreSelected = false
	raw.Profile.StoreFakeIP = false
	raw.GeoAutoUpdate = false
	route := routeDirect
	if o.Node != nil {
		proxies, rules, err := prepareNode(o.Node)
		if err != nil {
			return nil, err
		}
		// Rule mode with a single MATCH rule: every connection is routed to
		// the node, and domain destinations are handed to it unresolved
		// (MATCH never triggers the local resolver; DNS stays disabled).
		raw.Mode = tunnel.Rule
		raw.Proxy = proxies
		raw.Rule = rules
		// No rule needs the process name; never look processes up.
		raw.FindProcessMode = process.FindProcessOff
		route = routeNode
	}

	cfg, err := config.ParseRawConfig(raw)
	if err != nil {
		return nil, err
	}
	if everApplied {
		// A previous configuration left adapters whose finalizers read the
		// logger level that ApplyConfig is about to rewrite.
		drainFinalizers()
	}
	executor.ApplyConfig(cfg, true)
	everApplied = true

	bind := o.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(o.Port))
	tt := newTrackedTunnel(tunnel.Tunnel)
	ln, err := mixed.NewWithConfig(
		LC.AuthServer{Enable: true, Listen: addr, AuthStore: authStore.NewAuthStore(o.Authenticator)},
		inbound.NewListenConfig(),
		tt,
		inbound.WithInName("NETIVRI-MIXED"), inbound.WithSpecialRules(""),
	)
	if err != nil {
		executor.Shutdown()
		if isAddrInUse(err) {
			return nil, &cli.Error{Code: "PORT_IN_USE", Message: "listen " + addr + ": port is already in use", Exit: cli.ExitConflict}
		}
		return nil, err
	}

	inst := &Instance{ln: ln, done: make(chan struct{}), route: route, tt: tt}
	current = inst

	// Optional lifecycle: cancel ctx to stop the instance.
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = inst.Close()
			case <-inst.done:
			}
		}()
	}
	return inst, nil
}

// Addr returns the actual host:port the listener is bound to.
func (i *Instance) Addr() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ln == nil {
		return ""
	}
	return i.ln.Address()
}

// Route reports how traffic leaves this instance: "direct" or "node".
func (i *Instance) Route() string {
	return i.route
}

// Close stops the listener and the embedded core. Idempotent.
func (i *Instance) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	ln := i.ln
	i.ln = nil
	i.mu.Unlock()

	// Instance locks are never held while waiting for lifecycleMu.
	lifecycleMu.Lock()
	if ln != nil {
		_ = ln.Close()
	}
	// No connection may outlive the instance: close the tracked ones and
	// wait for their handlers before Mihomo's global state is torn down.
	i.tt.closeAll(closeDrainTimeout)
	executor.Shutdown()
	if current == i {
		current = nil // claim released only after the core is fully down
	}
	lifecycleMu.Unlock()

	close(i.done)
	return nil
}
