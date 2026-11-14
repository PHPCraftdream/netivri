package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	mihauth "github.com/metacubex/mihomo/component/auth"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

type upOptions struct {
	Foreground bool
	Port       int
	Bind       string
	PortSet    bool // --port given explicitly: overrides listen.port
	BindSet    bool // --bind given explicitly: overrides listen.bind
	Home       string
	Handshake  string // internal: handshake file of the spawning parent (angel child only)
}

// defaultHome resolves the state directory: NETIVRI_HOME, then
// userConfigDir()/netivri (docs/cli.md --home default).
func defaultHome(env func(string) string, userConfigDir func() (string, error)) (string, error) {
	if v := env("NETIVRI_HOME"); v != "" {
		return v, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "netivri"), nil
}

// isLoopbackHost accepts 127.0.0.0/8, ::1 and "localhost".
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseUp parses `up` flags. It performs no file or bind validation:
// merging with the config file happens in mergeUp.
func parseUp(args []string, env func(string) string, userConfigDir func() (string, error)) (upOptions, error) {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := upOptions{Port: 7890, Bind: "127.0.0.1"}
	fs.BoolVar(&opts.Foreground, "foreground", false, "run in the foreground instead of as a background angel")
	fs.IntVar(&opts.Port, "port", 7890, "mixed proxy port, 0 for a random free port")
	fs.StringVar(&opts.Bind, "bind", "127.0.0.1", "bind address")
	// Internal, not part of the CLI contract: set only by angel.Spawn.
	fs.StringVar(&opts.Handshake, "handshake", "", "internal: startup handshake file (requires --foreground)")
	homeFlag := fs.String("home", "", "state directory (default ./.netivri if it exists, NETIVRI_HOME, or the user config dir)")
	localFlag := fs.Bool("local", false, "use ./.netivri in the current directory as the state directory, creating it")
	// Global flags from docs/cli.md, accepted for compatibility: --json and
	// --no-color are no-ops (output is always JSON without ANSI); --quiet
	// suppresses nothing today.
	_ = fs.Bool("json", false, "machine output (always on)")
	_ = fs.Bool("quiet", false, "suppress human-oriented stderr output")
	_ = fs.Bool("no-color", false, "disable ANSI colors (never used)")

	if err := fs.Parse(args); err != nil {
		return opts, cli.Usage("up: %v", err)
	}
	if fs.NArg() > 0 {
		return opts, cli.Usage("up: unexpected argument %q", fs.Arg(0))
	}
	if opts.Handshake != "" && !opts.Foreground {
		return opts, cli.Usage("up: --handshake requires --foreground")
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			opts.PortSet = true
		case "bind":
			opts.BindSet = true
		}
	})
	home, _, err := resolveHome(*homeFlag, *localFlag, env, userConfigDir)
	if err != nil {
		return opts, err
	}
	opts.Home = home
	return opts, nil
}

// mergeUp combines the parsed flags with the loaded configuration into
// the final bind address and port. Explicit flags win over the config;
// with no config (defaults) the result is 127.0.0.1:7890. A bind that
// is neither an IP nor "localhost" is a usage error; a non-loopback
// bind is AUTH_REQUIRED unless proxy authentication is enabled.
func mergeUp(opts upOptions, cfg *config.Config) (string, int, error) {
	var bind string
	var port int
	if opts.BindSet {
		bind = opts.Bind
	} else {
		bind = cfg.Listen.Bind
	}
	if opts.PortSet {
		port = opts.Port
	} else {
		port = cfg.Listen.Port
	}
	if bind != "localhost" && net.ParseIP(bind) == nil {
		return "", 0, cli.Usage("up: --bind must be an IP address or \"localhost\", got an invalid value")
	}
	if port != 0 && (port < 1 || port > 65535) {
		return "", 0, cli.Usage("up: --port must be 0 (random) or between 1 and 65535")
	}
	status := authStatus(cfg.Auth.Login, cfg.Auth.Password, cfg.Accounts)
	authEnabled := status == "enabled" ||
		(status == "init-pending" && cfg.Auth.AllowInitRemote)
	if !isLoopbackHost(bind) && !authEnabled {
		return "", 0, &cli.Error{
			Code:    "AUTH_REQUIRED",
			Message: "non-loopback bind requires proxy authentication",
			Hint:    "add an account (netivri account add) or set auth.login and auth.password in config.ktav; an init password also needs auth.allow_init_remote: true; see netivri help config",
			Exit:    cli.ExitUsage,
		}
	}
	return bind, port, nil
}

// authStatus derives the value of the `auth` field of the `up` success
// output: none when neither `auth` nor an account is configured,
// init-pending while any placeholder is set, else enabled.
func authStatus(login, password string, accounts []config.Account) string {
	if login == config.InitPlaceholder || password == config.InitPlaceholder {
		return "init-pending"
	}
	for _, a := range accounts {
		if a.Password == config.InitPlaceholder {
			return "init-pending"
		}
	}
	if login == "" && password == "" && len(accounts) == 0 {
		return "none"
	}
	return "enabled"
}

// persistMu serialises the persist callback: the authenticator calls
// it at most once under its own lock, but the file write must not
// race with any other config writer in this process.
var persistMu sync.Mutex

// saveClaim writes a claimed credential into the file. Other commands may
// have changed the file since this process loaded its configuration, so the
// claim is applied to the copy on disk and nothing else is rewritten. A file
// that does not exist yet is created from the in-memory configuration. When
// the file no longer holds the placeholder that was claimed, the claim is
// refused: the angel applies the new file and the client tries again.
func saveClaim(path string, mem *config.Config, apply func(*config.Config) error) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return config.Save(path, mem)
	}
	disk, err := config.Load(path)
	if err != nil {
		return err
	}
	if err := apply(disk); err != nil {
		return err
	}
	return config.Save(path, disk)
}

var errClaimStale = errors.New("the configuration changed on disk; try again")

// newPersist returns the callback that stores the claimed credential
// in the in-memory config and rewrites the config file. It never
// returns or logs the credential.
func newPersist(path string, cfg *config.Config) func(login, passwordHash string) error {
	return newPersistWithTarget(path, cfg, nil)
}

func newPersistWithTarget(path string, cfg *config.Config, target *persistConfig) func(login, passwordHash string) error {
	claimedLogin, claimedPassword := cfg.Auth.Login, cfg.Auth.Password
	return func(login, passwordHash string) error {
		persistMu.Lock()
		defer persistMu.Unlock()
		mem := *cfg
		mem.Auth.Login, mem.Auth.Password = login, passwordHash
		err := saveClaim(path, &mem, func(disk *config.Config) error {
			if disk.Auth.Login != claimedLogin || disk.Auth.Password != claimedPassword {
				return errClaimStale
			}
			disk.Auth.Login, disk.Auth.Password = login, passwordHash
			return nil
		})
		if err != nil {
			return err
		}
		cfg.Auth.Login, cfg.Auth.Password = login, passwordHash
		if target != nil {
			target.recordLegacy(cfg, claimedLogin, claimedPassword, login, passwordHash)
		}
		return nil
	}
}

func newPersistAccount(path string, cfg *config.Config) func(login, passwordHash string) error {
	return newPersistAccountWithTarget(path, cfg, nil)
}

func newPersistAccountWithTarget(path string, cfg *config.Config, target *persistConfig) func(login, passwordHash string) error {
	return func(login, passwordHash string) error {
		persistMu.Lock()
		defer persistMu.Unlock()
		a, _ := cfg.Account(login)
		if a == nil {
			return errors.New("account no longer exists")
		}
		mem := *cfg
		mem.Accounts = append([]config.Account(nil), cfg.Accounts...)
		m, _ := mem.Account(login)
		m.Password = passwordHash
		err := saveClaim(path, &mem, func(disk *config.Config) error {
			d, _ := disk.Account(login)
			if d == nil || d.Password != config.InitPlaceholder {
				return errClaimStale
			}
			d.Password = passwordHash
			return nil
		})
		if err != nil {
			return err
		}
		a.Password = passwordHash
		if target != nil {
			target.recordAccount(cfg, login, passwordHash)
		}
		return nil
	}
}

// persistConfig tracks the config currently applied to the core and successful
// init claims. All fields and credential writes are protected by persistMu.
type persistConfig struct {
	cfg        *config.Config
	generation uint64
	legacy     *legacyCredentialClaim
	accounts   map[string]accountCredentialClaim
}

type legacyCredentialClaim struct {
	claimedLogin, claimedPassword string
	login, password               string
	generation                    uint64
}

type accountCredentialClaim struct {
	password   string
	generation uint64
}

func newPersistConfig(cfg *config.Config) *persistConfig {
	return &persistConfig{cfg: cfg, accounts: make(map[string]accountCredentialClaim)}
}

func (p *persistConfig) recordLegacy(source *config.Config, claimedLogin, claimedPassword, login, password string) {
	p.generation++
	p.legacy = &legacyCredentialClaim{claimedLogin: claimedLogin, claimedPassword: claimedPassword, login: login, password: password, generation: p.generation}
	if p.cfg != nil && p.cfg != source && p.cfg.Auth.Login == claimedLogin && p.cfg.Auth.Password == claimedPassword {
		p.cfg.Auth.Login, p.cfg.Auth.Password = login, password
	}
}

func (p *persistConfig) recordAccount(source *config.Config, login, password string) {
	p.generation++
	p.accounts[login] = accountCredentialClaim{password: password, generation: p.generation}
	if p.cfg != nil && p.cfg != source {
		if account, _ := p.cfg.Account(login); account != nil && account.Password == config.InitPlaceholder {
			account.Password = password
		}
	}
}

// rebind applies claims completed since cfg was loaded, then targets future
// successful persistence callbacks at the newly applied cfg.
func (p *persistConfig) mergeClaims(cfg *config.Config, since uint64) {
	if p == nil || cfg == nil {
		return
	}
	if claim := p.legacy; claim != nil && claim.generation > since && cfg.Auth.Login == claim.claimedLogin && cfg.Auth.Password == claim.claimedPassword {
		cfg.Auth.Login, cfg.Auth.Password = claim.login, claim.password
	}
	for login, claim := range p.accounts {
		if claim.generation <= since {
			continue
		}
		if account, _ := cfg.Account(login); account != nil && account.Password == config.InitPlaceholder {
			account.Password = claim.password
		}
	}
}

func (p *persistConfig) rebind(cfg *config.Config, since uint64) {
	if p == nil || cfg == nil {
		return
	}
	p.mergeClaims(cfg, since)
	p.cfg = cfg
}

func persistClaimGeneration(a mihauth.Authenticator) uint64 {
	var generation uint64
	withPersistConfig(a, func(p *persistConfig) { generation = p.generation })
	return generation
}

func rebindPersistConfigSince(a mihauth.Authenticator, cfg *config.Config, since uint64) {
	withPersistConfig(a, func(p *persistConfig) { p.rebind(cfg, since) })
}

func snapshotReloadCredentials(a mihauth.Authenticator, applied, candidate *config.Config, since uint64) (*config.Config, *config.Config) {
	persistMu.Lock()
	defer persistMu.Unlock()

	clone := func(cfg *config.Config) *config.Config {
		if cfg == nil {
			return nil
		}
		copy := *cfg
		copy.Accounts = append([]config.Account(nil), cfg.Accounts...)
		return &copy
	}
	appliedSnapshot, candidateSnapshot := clone(applied), clone(candidate)
	for a != nil {
		logged, ok := a.(*loggedAuth)
		if !ok {
			break
		}
		a = logged.inner
	}
	if policy, ok := a.(*accountPolicyAuthenticator); ok && policy.persist != nil {
		policy.persist.mergeClaims(candidateSnapshot, since)
	}
	return appliedSnapshot, candidateSnapshot
}

func withPersistConfig(a mihauth.Authenticator, update func(*persistConfig)) {
	persistMu.Lock()
	defer persistMu.Unlock()
	for a != nil {
		logged, ok := a.(*loggedAuth)
		if !ok {
			break
		}
		a = logged.inner
	}
	if policy, ok := a.(*accountPolicyAuthenticator); ok && policy.persist != nil {
		update(policy.persist)
	}
}

// liveAuth is the current `auth` value. The persist callbacks rewrite the
// credentials from a connection goroutine, always under persistMu.
func liveAuth(cfg *config.Config) string {
	persistMu.Lock()
	defer persistMu.Unlock()
	return authStatus(cfg.Auth.Login, cfg.Auth.Password, cfg.Accounts)
}

// loadConfig loads config.ktav under home; a missing file means the
// default configuration and no file is created. *cli.Error values are
// returned unchanged; anything else becomes INTERNAL.
func loadConfig(home string) (*config.Config, error) {
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, &cli.Error{Code: "INTERNAL", Message: "load config: " + err.Error(), Exit: cli.ExitInternal}
	}
	return cfg, nil
}

type accountPolicyAuthenticator struct {
	inner   mihauth.Authenticator
	persist *persistConfig
	mu      sync.RWMutex
	byUser  map[string]config.AllowedClients
}

func newAccountPolicyAuthenticator(inner mihauth.Authenticator, cfg *config.Config) *accountPolicyAuthenticator {
	if inner == nil {
		return nil
	}
	a := &accountPolicyAuthenticator{inner: inner, persist: newPersistConfig(cfg)}
	a.UpdateConfig(cfg)
	return a
}

// UpdateConfig replaces only the peer policy; the underlying credentials and
// their init claims and verification caches remain intact.
func (a *accountPolicyAuthenticator) UpdateConfig(cfg *config.Config) {
	policies := make(map[string]config.AllowedClients, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		policies[account.Login] = config.AllowedClientPrefixes(account.AllowedClients)
	}
	a.mu.Lock()
	a.byUser = policies
	a.mu.Unlock()
}

func (a *accountPolicyAuthenticator) Verify(user, pass string) bool {
	return a.inner.Verify(user, pass)
}

func (a *accountPolicyAuthenticator) VerifyFrom(user, pass string, remote net.Addr) bool {
	a.mu.RLock()
	policy, exists := a.byUser[user]
	a.mu.RUnlock()
	if exists && !policy.AllowsAddr(remote) {
		return false
	}
	return mihauth.VerifyWithRemote(a.inner, user, pass, remote)
}

func (a *accountPolicyAuthenticator) Users() []string { return a.inner.Users() }

// newAuthenticator builds the Mihomo authenticator from the config.
// A nil interface (auth disabled) is returned exactly as auth.New
// produced it. auth.New errors that are not *cli.Error become
// CONFIG_INVALID.
func newAuthenticator(cfg *config.Config, home string) (mihauth.Authenticator, error) {
	persistMu.Lock()
	defer persistMu.Unlock()
	persist := newPersistConfig(cfg)
	legacy := auth.Options{
		Login:    cfg.Auth.Login,
		Password: cfg.Auth.Password,
		Persist:  newPersistWithTarget(config.ConfigPath(home), cfg, persist),
	}
	accounts := make([]auth.AccountSpec, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		accounts = append(accounts, auth.AccountSpec{
			Login:    a.Login,
			Password: a.Password,
		})
	}
	a, err := auth.NewMulti(auth.MultiOptions{
		Legacy:         legacy,
		Accounts:       accounts,
		PersistAccount: newPersistAccountWithTarget(config.ConfigPath(home), cfg, persist),
	})
	if err != nil {
		var ce *cli.Error
		if errors.As(err, &ce) {
			return nil, ce
		}
		return nil, &cli.Error{
			Code:    config.CodeConfigInvalid,
			Exit:    cli.ExitUsage,
			Message: err.Error(),
			Hint:    "fix auth.login / auth.password in config.ktav; see netivri help config",
		}
	}
	if a == nil {
		return nil, nil
	}
	policy := newAccountPolicyAuthenticator(a, cfg)
	policy.persist = persist
	return policy, nil
}

func runUp(args []string, stdout io.Writer) error {
	opts, err := parseUp(args, os.Getenv, os.UserConfigDir)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(opts.Home)
	if err != nil {
		return failStartup(opts.Handshake, err)
	}
	bind, port, err := mergeUp(opts, cfg)
	if err != nil {
		return failStartup(opts.Handshake, err)
	}
	if _, err := accountRoutePool(opts.Home, cfg); err != nil {
		return failStartup(opts.Handshake, err)
	}
	if _, err := resolveServerSpecs(opts.Home, cfg); err != nil {
		return failStartup(opts.Handshake, err)
	}
	if !opts.Foreground {
		return runUpAngel(opts, args, stdout)
	}
	return runUpForeground(opts, stdout, bind, port, cfg)
}

// runUpAngel is plain `up`: a short-lived parent that answers idempotently
// when an angel already runs in this home and otherwise spawns the detached
// child that does the real work. Config problems were already reported by
// runUp, before anything is spawned.
func runUpAngel(opts upOptions, args []string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), angel.DefaultSpawnTimeout+angel.ProbeTimeout)
	defer cancel()
	_, live, probeErr := angelProbeLive(ctx, opts.Home)
	if errors.Is(probeErr, angel.ErrStatusShape) {
		// An angel of another build runs: its header answers idempotency.
		probeErr = nil
	}
	if errors.Is(probeErr, angel.ErrControlTimeout) {
		return angelNotResponding()
	}
	if probeErr == nil {
		if conflictsWithRunning(opts, live.Listen) {
			return &cli.Error{
				Code:    "ALREADY_RUNNING",
				Message: "an angel is already running with different settings",
				Hint:    "run netivri down first",
				Exit:    cli.ExitConflict,
			}
		}
		cli.Success(stdout, alreadyRunningData(live, true))
		return nil
	}
	if !errors.Is(probeErr, angel.ErrNotRunning) {
		return &cli.Error{Code: "INTERNAL", Message: probeErr.Error(), Exit: cli.ExitInternal}
	}
	exe, err := os.Executable()
	if err != nil {
		return &cli.Error{Code: "INTERNAL", Message: "resolve executable: " + err.Error(), Exit: cli.ExitInternal}
	}
	st, err := angelSpawn(ctx, angel.SpawnOptions{Exe: exe, Args: filterUpArgs(args, opts.Home), Home: opts.Home})
	if err != nil {
		return err
	}
	cli.Success(stdout, alreadyRunningData(statusOf(st), false))
	return nil
}

// runUpForeground runs the proxy in this process: the user's `--foreground`
// run or the angel child (opts.Handshake set). A failure of the angel child
// reaches the spawning parent through the handshake file.
func runUpForeground(opts upOptions, stdout io.Writer, bind string, port int, cfg *config.Config) error {
	if err := runForegroundInner(opts, stdout, bind, port, cfg); err != nil {
		return failStartup(opts.Handshake, err)
	}
	return nil
}

// runForegroundInner remains replaceable by tests so runUpForeground can be
// exercised without starting Mihomo.
var runForegroundInner = serve

func serve(opts upOptions, stdout io.Writer, bind string, port int, cfg *config.Config) error {
	pool, err := accountRoutePool(opts.Home, cfg)
	if err != nil {
		return err
	}
	serverSpecs, err := resolveServerSpecs(opts.Home, cfg)
	if err != nil {
		return err
	}
	// Foreground cannot be idempotent: refuse a second instance in one home.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), angel.ProbeTimeout)
	_, err = angelProbe(probeCtx, opts.Home)
	cancelProbe()
	if errors.Is(err, angel.ErrControlTimeout) {
		return angelNotResponding()
	}
	if err == nil {
		return &cli.Error{
			Code:    "ALREADY_RUNNING",
			Message: "an angel is already running in this state directory",
			Hint:    "run netivri down first",
			Exit:    cli.ExitConflict,
		}
	}
	if !errors.Is(err, angel.ErrNotRunning) {
		return &cli.Error{Code: "INTERNAL", Message: err.Error(), Exit: cli.ExitInternal}
	}
	authenticator, err := newAuthenticator(cfg, opts.Home)
	if err != nil {
		return err
	}

	// Installed before Start so a signal during startup is not lost.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Only the angel child has a log; a foreground run stays silent on stderr
	// unless NETIVRI_LOG_STDERR is set.
	logToStderr := opts.Handshake != "" || foregroundLogRequested()
	leveled := func(write func(string, ...any)) func(string, ...any) {
		return func(format string, args ...any) {
			if logToStderr {
				write(format, args...)
			}
		}
	}
	logf, warnf, errorf := leveled(angelInfo), leveled(angelWarn), leveled(angelError)
	debugf := leveled(angelDebug)
	store := openStats(opts.Home, pool.infos, warnf)
	stopRequested := make(chan struct{})
	requestStop := sync.OnceFunc(func() { close(stopRequested) })
	live := newLiveAngel(opts, logf, requestStop)
	live.warnf, live.errorf, live.debugf = warnf, errorf, debugf
	if err := live.start(ctx, desiredState{cfg: cfg, bind: bind, port: port, pool: pool, servers: serverSpecs}, authenticator, store); err != nil {
		closeStats(store)
		return err
	}
	// The store may be opened later by a reload, so ask the angel for it.
	defer func() { closeStats(live.currentStore()) }()
	if opts.Handshake != "" {
		defer armCrashDump(opts.Home, logf, errorf, warnf)()
	}

	token, err := angel.NewToken()
	if err != nil {
		live.close()
		return &cli.Error{Code: "INTERNAL", Message: "generate control token: " + err.Error(), Exit: cli.ExitInternal}
	}
	startedAt := time.Now().UTC().Format(time.RFC3339)
	status := func() angel.Status { return live.status(startedAt) }
	ctrl, err := angel.StartServer(token, status, requestStop)
	if err != nil {
		live.close()
		return err
	}

	st := stateOf(status(), ctrl.Addr(), token)
	// Closing order matters for `down`: the watcher and the refresh stop first,
	// then the core goes down, the statistics are flushed (a stats query after
	// `down` sees them), the control server follows, and the state file
	// disappears last.
	shutdown := func() {
		live.close()
		_ = ctrl.Close()
		angel.RemoveStateIf(opts.Home, st.PID, st.Token)
	}
	if err := angel.WriteState(opts.Home, st); err != nil {
		shutdown()
		return err
	}
	if opts.Handshake != "" {
		if err := angel.WriteHandshake(opts.Handshake, st); err != nil {
			shutdown()
			return err
		}
		logf("%s", startMessage(st.PID, st.Listen, st.Route))
	} else {
		cli.Success(stdout, map[string]any{
			"angel":      "running",
			"pid":        st.PID,
			"listen":     st.Listen,
			"protocols":  st.Protocols,
			"auth":       st.Auth,
			"foreground": true,
		})
	}

	live.startLoops()
	reason := "signal"
	select {
	case <-sig:
	case <-stopRequested:
		reason = "control"
	}
	// Logged before the state file goes: `down` returns once it is gone, and a
	// `logs` right after it must already see this line.
	logf("stop pid=%d reason=%s", st.PID, reason)
	shutdown()
	return nil
}
