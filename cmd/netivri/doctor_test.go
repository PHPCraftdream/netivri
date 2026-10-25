package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// doctorDoc is the decoded doctor document.
type doctorDoc struct {
	Checks  []doctorCheck `json:"checks"`
	Summary doctorSummary `json:"summary"`
}

type doctorEnvelope struct {
	OK   bool      `json:"ok"`
	Data doctorDoc `json:"data"`
}

// doctorFixTime pins doctorNow so snapshot ages and the log window are
// deterministic.
func doctorFixTime(t *testing.T) time.Time {
	t.Helper()
	fixed := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	old := doctorNow
	doctorNow = func() time.Time { return fixed }
	t.Cleanup(func() { doctorNow = old })
	return fixed
}

func doctorStubAngel(t *testing.T, fn func(context.Context, string) (angel.State, angel.Status, error)) {
	t.Helper()
	old := angelProbeLive
	angelProbeLive = fn
	t.Cleanup(func() { angelProbeLive = old })
}

func doctorAngelDown(t *testing.T) {
	doctorStubAngel(t, func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{}, angel.ErrNotRunning
	})
}

// doctorAngelUp stubs a running angel that reports the given warm nodes.
func doctorAngelUp(t *testing.T, warm ...angel.NodeHealth) {
	doctorStubAngel(t, func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{
			PID:            42,
			Listen:         "127.0.0.1:7890",
			AllowedClients: "*",
			Warm:           &angel.WarmInfo{Nodes: warm},
		}, nil
	})
}

// doctorStubListen replaces the port probe: err nil means the address can be
// bound, any other value means the bind fails.
func doctorStubListen(t *testing.T, err error) {
	t.Helper()
	old := doctorListen
	doctorListen = func(string) error { return err }
	t.Cleanup(func() { doctorListen = old })
}

// doctorConfig writes a valid configuration: it is loaded back through
// config.Load so an invalid fixture fails the test instead of silently
// turning into the config-fail path of doctor.
func doctorConfig(t *testing.T, home string, mutate func(*config.Config)) {
	t.Helper()
	cfg := config.Default()
	if mutate != nil {
		mutate(cfg)
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(config.ConfigPath(home)); err != nil {
		t.Fatalf("test fixture is not a valid configuration: %v", err)
	}
}

// doctorWriteInvalidConfig writes a raw configuration file that config.Load
// rejects: the config-fail path of doctor needs a genuinely invalid file.
func doctorWriteInvalidConfig(t *testing.T, home, text string) {
	t.Helper()
	if err := os.WriteFile(config.ConfigPath(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func doctorSnapshot(t *testing.T, home, name string, snap subscription.Snapshot) {
	t.Helper()
	if err := subscription.NewStore(filepath.Join(home, "subscriptions")).Save(name, snap); err != nil {
		t.Fatal(err)
	}
}

func doctorWriteLog(t *testing.T, home string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(angel.RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(angel.LogPath(home), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// doctorRunDoc runs `doctor` for the home and decodes its JSON document.
func doctorRunDoc(t *testing.T, home string) (doctorDoc, error) {
	t.Helper()
	cli.UseJSON()
	var out bytes.Buffer
	err := runDoctor([]string{"--home", home}, &out)
	var env doctorEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("doctor output is not one JSON object: %v\n%s", err, out.String())
	}
	if !env.OK {
		t.Fatalf("doctor envelope not ok: %s", out.String())
	}
	return env.Data, err
}

// doctorAssertFailed asserts the error of a run with failing checks: the
// DOCTOR_FAILED code with the exit status of a failed check.
func doctorAssertFailed(t *testing.T, err error) {
	t.Helper()
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "DOCTOR_FAILED" || ce.Exit != cli.ExitCheckFailed {
		t.Fatalf("doctor = %v, want DOCTOR_FAILED with exit %d", err, cli.ExitCheckFailed)
	}
}

func doctorFind(t *testing.T, doc doctorDoc, name string) doctorCheck {
	t.Helper()
	for _, c := range doc.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %v", name, doc.Checks)
	return doctorCheck{}
}

func doctorRoutedAccount() config.Account {
	return config.Account{Login: "alice", Password: config.InitPlaceholder, Provider: "main", ExitCountry: "DE"}
}

func doctorStatusMap(doc doctorDoc) map[string]string {
	m := make(map[string]string, len(doc.Checks))
	for _, c := range doc.Checks {
		m[c.Name] = c.Status
	}
	return m
}

func TestDoctorAllOK(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelUp(t, angel.NodeHealth{Label: "main/m-de", Alive: true})
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://provider.example/sub/secret"}}
		cfg.Accounts = []config.Account{doctorRoutedAccount()}
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: fixed.Add(-time.Minute), Nodes: []subscription.Node{node("m-de", "DE")}})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	want := map[string]string{
		"config": doctorOK, "angel": doctorOK, "listen": doctorOK,
		"subscriptions": doctorOK, "account alice": doctorOK, "log": doctorOK,
	}
	if got := doctorStatusMap(doc); !equalStrings(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if doc.Summary != (doctorSummary{Ok: 6}) {
		t.Errorf("summary = %+v", doc.Summary)
	}
	acc := doctorFind(t, doc, "account alice")
	if !strings.Contains(acc.Detail, "tier 0") || !strings.Contains(acc.Detail, "1/1") {
		t.Errorf("account detail = %q", acc.Detail)
	}
}

func equalStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestDoctorConfigInvalid(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	home := t.TempDir()
	// Port 0 is not a valid configured value (only the --port flag allows
	// it); the file is raw text, not the guarded fixture helper.
	doctorWriteInvalidConfig(t, home, "listen.port: 0\n")
	doc, err := doctorRunDoc(t, home)
	doctorAssertFailed(t, err)
	cfgCheck := doctorFind(t, doc, "config")
	if cfgCheck.Status != doctorFail || !strings.Contains(cfgCheck.Detail, "listen.port") {
		t.Errorf("config = %+v", cfgCheck)
	}
	// The other checks still run; the ones that need the configuration warn.
	if c := doctorFind(t, doc, "listen"); c.Status != doctorWarn || !strings.Contains(c.Detail, "configuration is invalid") {
		t.Errorf("listen = %+v", c)
	}
	if c := doctorFind(t, doc, "subscriptions"); c.Status != doctorWarn || !strings.Contains(c.Detail, "configuration is invalid") {
		t.Errorf("subscriptions = %+v", c)
	}
	if doc.Summary.Fail != 1 || doc.Summary.Warn != 3 || doc.Summary.Ok != 1 {
		t.Errorf("summary = %+v for %d checks", doc.Summary, len(doc.Checks))
	}
	if doc.Summary.Ok+doc.Summary.Warn+doc.Summary.Fail != len(doc.Checks) {
		t.Errorf("summary does not add up: %+v for %d checks", doc.Summary, len(doc.Checks))
	}
}

func TestDoctorAngelDown(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://provider.example/sub/secret"}}
		cfg.Accounts = []config.Account{doctorRoutedAccount()}
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: doctorNow(), Nodes: []subscription.Node{node("m-de", "DE")}})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	if c := doctorFind(t, doc, "angel"); c.Status != doctorWarn || c.Detail != "angel is not running" {
		t.Errorf("angel = %+v", c)
	}
	if c := doctorFind(t, doc, "listen"); c.Status != doctorOK || !strings.Contains(c.Detail, "is free") {
		t.Errorf("listen = %+v", c)
	}
	acc := doctorFind(t, doc, "account alice")
	if acc.Status != doctorWarn || !strings.Contains(acc.Detail, "1 stored node(s)") || !strings.Contains(acc.Detail, "angel is not running") {
		t.Errorf("account = %+v", acc)
	}
	if doc.Summary.Warn != 2 || doc.Summary.Fail != 0 {
		t.Errorf("summary = %+v", doc.Summary)
	}
}

func TestDoctorSnapshotMissingAndStale(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{
			{Name: "fresh", URL: "https://a.example/sub/x"},
			{Name: "stale", URL: "https://b.example/sub/y"},
			{Name: "gone", URL: "https://c.example/sub/z"},
		}
	})
	doctorSnapshot(t, home, "fresh", subscription.Snapshot{FetchedAt: fixed.Add(-30 * time.Minute)})
	doctorSnapshot(t, home, "stale", subscription.Snapshot{FetchedAt: fixed.Add(-3 * time.Hour)})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c := doctorFind(t, doc, "subscriptions")
	if c.Status != doctorWarn {
		t.Errorf("subscriptions = %+v", c)
	}
	for _, want := range []string{"snapshot of gone is not stored", "snapshot of stale is 3h0m0s old"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail %q lacks %q", c.Detail, want)
		}
	}
}

func TestDoctorSnapshotAgeSkippedWhenIntervalOff(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Fetch.UpdateInterval = config.UpdateOff
		cfg.Subscriptions = []config.Subscription{{Name: "old", URL: "https://a.example/sub/x"}}
	})
	doctorSnapshot(t, home, "old", subscription.Snapshot{FetchedAt: fixed.Add(-30 * 24 * time.Hour)})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	if c := doctorFind(t, doc, "subscriptions"); c.Status != doctorOK {
		t.Errorf("subscriptions = %+v", c)
	}
}

func TestDoctorSnapshotWithoutFetchTime(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "nofetch", URL: "https://a.example/sub/x"}}
	})
	doctorSnapshot(t, home, "nofetch", subscription.Snapshot{})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c := doctorFind(t, doc, "subscriptions")
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "snapshot of nofetch has no fetch time, its age is unknown") {
		t.Errorf("subscriptions = %+v", c)
	}
}

func TestDoctorProviderNotConfigured(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "ghost", ExitCountry: "DE"}}
	})
	doc, err := doctorRunDoc(t, home)
	doctorAssertFailed(t, err)
	c := doctorFind(t, doc, "subscriptions")
	if c.Status != doctorFail || !strings.Contains(c.Detail, "account alice: provider ghost is not a configured subscription") {
		t.Errorf("subscriptions = %+v", c)
	}
	acc := doctorFind(t, doc, "account alice")
	if acc.Status != doctorFail || !strings.Contains(acc.Detail, "no nodes available") {
		t.Errorf("account = %+v", acc)
	}
	if doc.Summary.Fail != 2 { // subscriptions plus the account route
		t.Errorf("summary = %+v", doc.Summary)
	}
	if doc.Summary.Ok+doc.Summary.Warn+doc.Summary.Fail != len(doc.Checks) {
		t.Errorf("summary does not add up: %+v for %d checks", doc.Summary, len(doc.Checks))
	}
}

func TestDoctorRouteWithoutNodes(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://a.example/sub/x"}}
		cfg.Accounts = []config.Account{doctorRoutedAccount()} // wants DE, snapshot has FR only
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: doctorNow(), Nodes: []subscription.Node{node("m-fr", "FR")}})
	doc, err := doctorRunDoc(t, home)
	doctorAssertFailed(t, err)
	acc := doctorFind(t, doc, "account alice")
	if acc.Status != doctorFail || !strings.Contains(acc.Detail, "no nodes available") {
		t.Errorf("account = %+v", acc)
	}
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Hint != "see the failed checks above" {
		t.Fatalf("error = %v, want DOCTOR_FAILED", err)
	}
}

func TestDoctorDirectAccount(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Accounts = []config.Account{{Login: "bob", Password: config.InitPlaceholder}}
	})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	if c := doctorFind(t, doc, "account bob"); c.Status != doctorOK || c.Detail != "direct" {
		t.Errorf("account bob = %+v", c)
	}
}

func TestDoctorFallbackTierWarn(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelUp(t,
		angel.NodeHealth{Label: "main/m-de", Alive: false},
		angel.NodeHealth{Label: "backup/b-de", Alive: true},
	)
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{
			{Name: "main", URL: "https://a.example/sub/x"},
			{Name: "backup", URL: "https://b.example/sub/y"},
		}
		cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Provider: "main,backup", ExitCountry: "DE"}}
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: fixed, Nodes: []subscription.Node{node("m-de", "DE")}})
	doctorSnapshot(t, home, "backup", subscription.Snapshot{FetchedAt: fixed, Nodes: []subscription.Node{node("b-de", "DE")}})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	acc := doctorFind(t, doc, "account alice")
	if acc.Status != doctorWarn {
		t.Errorf("account = %+v", acc)
	}
	for _, want := range []string{"no live node in tier 0 DE (main)", "fallback tier 1 DE (backup)"} {
		if !strings.Contains(acc.Detail, want) {
			t.Errorf("detail %q lacks %q", acc.Detail, want)
		}
	}
}

func TestDoctorNoLiveNode(t *testing.T) {
	doctorFixTime(t)
	doctorAngelUp(t, angel.NodeHealth{Label: "main/m-de", Alive: false})
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: "https://a.example/sub/x"}}
		cfg.Accounts = []config.Account{doctorRoutedAccount()}
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: doctorNow(), Nodes: []subscription.Node{node("m-de", "DE")}})
	doc, err := doctorRunDoc(t, home)
	doctorAssertFailed(t, err)
	if c := doctorFind(t, doc, "account alice"); c.Status != doctorFail || !strings.Contains(c.Detail, "no live node") {
		t.Errorf("account = %+v", c)
	}
	if doc.Summary.Fail != 1 || doc.Summary.Warn != 0 {
		t.Errorf("summary = %+v", doc.Summary)
	}
}

func TestDoctorPortTaken(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, errors.New("bind: address already in use"))
	home := t.TempDir()
	doctorConfig(t, home, nil)
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c := doctorFind(t, doc, "listen")
	if c.Status != doctorWarn || !strings.HasSuffix(c.Detail, "is taken") {
		t.Errorf("listen = %+v", c)
	}
}

func TestDoctorLogWarnEntries(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, nil)
	doctorWriteLog(t, home,
		angel.FormatLogLine(fixed.Add(-2*time.Hour), angel.LevelWarn, "old warn"),
		angel.FormatLogLine(fixed.Add(-time.Minute), angel.LevelWarn, "route node main/m-de is down"),
		angel.FormatLogLine(fixed.Add(-time.Minute), angel.LevelError, "boom"),
	)
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	c := doctorFind(t, doc, "log")
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "2 warn/error entries") || !strings.Contains(c.Detail, "boom") {
		t.Errorf("log = %+v", c)
	}
}

func TestDoctorLogCleanAndMissing(t *testing.T) {
	fixed := doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, nil)
	// A log with only old or harmless entries is fine; a missing one too.
	doctorWriteLog(t, home,
		angel.FormatLogLine(fixed.Add(-2*time.Hour), angel.LevelError, "old"),
		angel.FormatLogLine(fixed.Add(-time.Minute), angel.LevelInfo, "fine"),
	)
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	if c := doctorFind(t, doc, "log"); c.Status != doctorOK {
		t.Errorf("log = %+v", c)
	}
	doc, err = doctorRunDoc(t, t.TempDir())
	if err != nil {
		t.Fatalf("doctor = %v, want nil", err)
	}
	if c := doctorFind(t, doc, "log"); c.Status != doctorOK || !strings.Contains(c.Detail, "no log yet") {
		t.Errorf("log = %+v", c)
	}
}

func TestDoctorNoSecretsInOutput(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	const (
		secretURL  = "https://provider.example/sub/supersecret"
		authSecret = "legacy-password-secret"
		accSecret  = "alice-password-secret"
	)
	authHash, err := auth.Hash(authSecret)
	if err != nil {
		t.Fatal(err)
	}
	accHash, err := auth.Hash(accSecret)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Auth.Login = "legacy"
		cfg.Auth.Password = authHash
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: secretURL}}
		cfg.Accounts = []config.Account{{Login: "alice", Password: accHash, Provider: "main", ExitCountry: "DE"}}
	})
	doctorSnapshot(t, home, "main", subscription.Snapshot{FetchedAt: doctorNow(), Nodes: []subscription.Node{node("m-de", "DE")}})
	cli.UseJSON()
	var out bytes.Buffer
	if err := runDoctor([]string{"--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, s := range []string{secretURL, authSecret, accSecret, authHash, accHash} {
		if strings.Contains(text, s) {
			t.Errorf("output leaks a secret (%d characters)", len(s))
		}
	}
}

func TestDoctorKtavShape(t *testing.T) {
	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, nil)
	useKtavOutput(t)
	var out bytes.Buffer
	if err := runDoctor([]string{"--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	got, err := ktav.Loads(out.String())
	if err != nil {
		t.Fatalf("output is not Ktav: %v\n%s", err, out.String())
	}
	doc := got.(map[string]any)
	data, ok := doc["data"].(map[string]any)
	if !ok || doc["ok"] != true {
		t.Fatalf("envelope = %v", doc)
	}
	checks, ok := data["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("checks = %v", data["checks"])
	}
	first := checks[0].(map[string]any)
	if first["name"] != "config" || first["status"] != doctorOK {
		t.Errorf("first check = %v", first)
	}
	summary := data["summary"].(map[string]any)
	if summary["ok"] == nil || summary["warn"] == nil || summary["fail"] == nil {
		t.Errorf("summary = %v", summary)
	}
}

func TestDoctorUsage(t *testing.T) {
	cli.UseJSON()
	var out bytes.Buffer
	err := runDoctor([]string{"extra"}, &out)
	ce, ok := err.(*cli.Error)
	if !ok || ce.Code != "USAGE" {
		t.Fatalf("doctor extra = %v, want USAGE", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}
