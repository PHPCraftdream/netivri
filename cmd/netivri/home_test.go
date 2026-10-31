package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// inTempDir makes a new temp directory the current one for the test and
// returns the directory as the program sees it (a symlinked temp root, as on
// macOS, comes back resolved).
func inTempDir(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func envOf(home string) func(string) string {
	return func(key string) string {
		if key == "NETIVRI_HOME" {
			return home
		}
		return ""
	}
}

func fakeConfigDir(t *testing.T) func() (string, error) {
	dir := t.TempDir()
	return func() (string, error) { return dir, nil }
}

func TestResolveHomePrecedence(t *testing.T) {
	cwd := inTempDir(t)
	local := filepath.Join(cwd, ".netivri")
	confDir := fakeConfigDir(t)
	userDir, _ := confDir()
	envHome := filepath.Join(t.TempDir(), "env")

	// Nothing asked, nothing in the directory: the environment, then the user config directory.
	if got, src, err := resolveHome("", false, envOf(envHome), confDir); err != nil || got != envHome || src != homeFromEnv {
		t.Errorf("env: %q, %q, %v", got, src, err)
	}
	if got, src, err := resolveHome("", false, envOf(""), confDir); err != nil || got != filepath.Join(userDir, "netivri") || src != homeFromDefault {
		t.Errorf("default: %q, %q, %v", got, src, err)
	}
	if _, err := os.Stat(local); err == nil {
		t.Fatal("resolving without --local created the directory")
	}

	// A .netivri directory in the current directory is picked up on its own and beats the environment.
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, src, err := resolveHome("", false, envOf(envHome), confDir); err != nil || got != local || src != homeFromLocalDir {
		t.Errorf("auto: %q, %q, %v; want %q", got, src, err, local)
	}
	// --home beats it, as given.
	if got, src, err := resolveHome("/some/where", false, envOf(envHome), confDir); err != nil || got != "/some/where" || src != homeFromFlag {
		t.Errorf("--home: %q, %q, %v", got, src, err)
	}
	// --local names the same directory.
	if got, src, err := resolveHome("", true, envOf(envHome), confDir); err != nil || got != local || src != homeFromLocalFlag {
		t.Errorf("--local: %q, %q, %v", got, src, err)
	}
}

func TestResolveHomeLocalFlagCreatesAPrivateDirectoryThatGitIgnores(t *testing.T) {
	cwd := inTempDir(t)
	dir := filepath.Join(cwd, ".netivri")
	got, src, err := resolveHome("", true, envOf(""), fakeConfigDir(t))
	if err != nil || got != dir || src != homeFromLocalFlag {
		t.Fatalf("resolveHome = %q, %q, %v", got, src, err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("the directory was not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || string(ignore) != "*\n" {
		t.Errorf(".gitignore = %q, %v; want everything ignored", ignore, err)
	}

	// An existing directory is left as it is: its files and its own .gitignore stay.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.ktav"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveHome("", true, envOf(""), fakeConfigDir(t)); err != nil {
		t.Fatal(err)
	}
	if ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(ignore) != "custom\n" {
		t.Errorf(".gitignore of an existing directory was rewritten: %q", ignore)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.ktav")); err != nil {
		t.Errorf("a file of the directory is gone: %v", err)
	}
}

func TestResolveHomeSearchesOnlyTheCurrentDirectory(t *testing.T) {
	cwd := inTempDir(t)
	if err := os.Mkdir(filepath.Join(cwd, ".netivri"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(cwd, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	envHome := filepath.Join(t.TempDir(), "env")
	if got, src, err := resolveHome("", false, envOf(envHome), fakeConfigDir(t)); err != nil || got != envHome || src != homeFromEnv {
		t.Errorf("from a subdirectory: %q, %q, %v; the parent's .netivri must not be used", got, src, err)
	}
}

func TestResolveHomeIgnoresAFileNamedLikeTheDirectory(t *testing.T) {
	cwd := inTempDir(t)
	file := filepath.Join(cwd, ".netivri")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	envHome := filepath.Join(t.TempDir(), "env")
	if got, src, err := resolveHome("", false, envOf(envHome), fakeConfigDir(t)); err != nil || got != envHome || src != homeFromEnv {
		t.Errorf("auto: %q, %q, %v; a file is not a state directory", got, src, err)
	}
	_, _, err := resolveHome("", true, envOf(envHome), fakeConfigDir(t))
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || !strings.Contains(ce.Message, "not a directory") {
		t.Errorf("--local over a file: %v, want an error that says it is not a directory", err)
	}
}

func TestResolveHomeFlagsConflict(t *testing.T) {
	inTempDir(t)
	_, _, err := resolveHome("/h", true, envOf(""), fakeConfigDir(t))
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "USAGE" || ce.Exit != cli.ExitUsage || !strings.Contains(ce.Message, "--home and --local") {
		t.Fatalf("err = %v, want a usage error naming both flags", err)
	}
	if _, statErr := os.Stat(".netivri"); statErr == nil {
		t.Error("a conflicting call created the directory")
	}
}

func TestResolveHomeWithoutACurrentDirectory(t *testing.T) {
	saved := getwd
	t.Cleanup(func() { getwd = saved })
	getwd = func() (string, error) { return "", errors.New("gone") }
	envHome := filepath.Join(t.TempDir(), "env")
	if got, src, err := resolveHome("", false, envOf(envHome), fakeConfigDir(t)); err != nil || got != envHome || src != homeFromEnv {
		t.Errorf("auto without a current directory: %q, %q, %v", got, src, err)
	}
	_, _, err := resolveHome("", true, envOf(envHome), fakeConfigDir(t))
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Errorf("--local without a current directory: %v", err)
	}
}

func TestResolveHomeUserConfigDirError(t *testing.T) {
	inTempDir(t)
	_, _, err := resolveHome("", false, envOf(""), func() (string, error) { return "", errors.New("no config dir") })
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" || !strings.Contains(ce.Message, "no config dir") {
		t.Errorf("err = %v", err)
	}
}

func TestParseUpLocal(t *testing.T) {
	cwd := inTempDir(t)
	want := filepath.Join(cwd, ".netivri")
	opts, err := parseUp([]string{"--foreground", "--local"}, envOf("/env"), fakeConfigDir(t))
	if err != nil || opts.Home != want {
		t.Fatalf("--local: Home = %q, %v; want %q", opts.Home, err, want)
	}
	// A directory that exists is used with no flag at all.
	opts, err = parseUp([]string{"--foreground"}, envOf("/env"), fakeConfigDir(t))
	if err != nil || opts.Home != want {
		t.Fatalf("auto: Home = %q, %v; want %q", opts.Home, err, want)
	}
	// --local=false is no --local, and the directory still wins over the environment by itself.
	opts, err = parseUp([]string{"--foreground", "--local=false"}, envOf("/env"), fakeConfigDir(t))
	if err != nil || opts.Home != want {
		t.Fatalf("--local=false: Home = %q, %v", opts.Home, err)
	}
	if _, err = parseUp([]string{"--foreground", "--local", "--home", "/h"}, okEnv, fakeConfigDir(t)); err == nil {
		t.Error("--local with --home: want a usage error")
	} else {
		assertCode(t, err, "USAGE", cli.ExitUsage)
	}
}

func TestParseHomeArgsLocal(t *testing.T) {
	cwd := inTempDir(t)
	local := filepath.Join(cwd, ".netivri")
	t.Setenv("NETIVRI_HOME", filepath.Join(t.TempDir(), "env"))
	for _, tc := range []struct {
		name       string
		args       []string
		wantHome   string
		wantSource string
		wantRest   []string
	}{
		{"flag", []string{"--local", "a"}, local, homeFromLocalFlag, []string{"a"}},
		{"single dash", []string{"-local", "a"}, local, homeFromLocalFlag, []string{"a"}},
		{"equals true", []string{"a", "--local=true", "--name", "n"}, local, homeFromLocalFlag, []string{"a", "--name", "n"}},
		{"terminator keeps the rest", []string{"--", "--local"}, local, homeFromLocalDir, []string{"--", "--local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, source, rest, err := parseHomeArgsFrom("sub test", tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if home != tc.wantHome || source != tc.wantSource || !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("home %q source %q rest %#v; want %q %q %#v", home, source, rest, tc.wantHome, tc.wantSource, tc.wantRest)
			}
		})
	}
	// parseHomeArgs is the same without the source.
	if home, rest, err := parseHomeArgs("sub test", []string{"--local", "x"}); err != nil || home != local || !reflect.DeepEqual(rest, []string{"x"}) {
		t.Errorf("parseHomeArgs: %q %v %v", home, rest, err)
	}
	for _, args := range [][]string{{"--local", "--home", "/h"}, {"--home=/h", "--local=true"}, {"--local=maybe"}} {
		if _, _, err := parseHomeArgs("sub test", args); err == nil {
			t.Errorf("%v: want a usage error", args)
		} else {
			assertCode(t, err, "USAGE", cli.ExitUsage)
		}
	}
	// --local=false is no flag: the directory (it exists) is still found, the flag is stripped.
	if home, rest, err := parseHomeArgs("sub test", []string{"--local=false", "x"}); err != nil || home != local || !reflect.DeepEqual(rest, []string{"x"}) {
		t.Errorf("--local=false: %q %v %v", home, rest, err)
	}
}

func TestParseHomeArgsFirstUseCreatesTheDirectory(t *testing.T) {
	cwd := inTempDir(t)
	t.Setenv("NETIVRI_HOME", filepath.Join(t.TempDir(), "env"))
	home, source, _, err := parseHomeArgsFrom("status", []string{"--local"})
	if err != nil || source != homeFromLocalFlag {
		t.Fatalf("%q %q %v", home, source, err)
	}
	if info, err := os.Stat(filepath.Join(cwd, ".netivri")); err != nil || !info.IsDir() {
		t.Errorf("the directory was not created: %v", err)
	}
}

func TestFilterUpArgsDropsLocalAndPassesTheResolvedHome(t *testing.T) {
	got := filterUpArgs([]string{"--local", "--port", "0", "--local=true", "-local", "--bind=::1"}, "/abs/.netivri")
	want := []string{"up", "--port", "0", "--bind=::1", "--home", "/abs/.netivri"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterUpArgs = %v, want %v", got, want)
	}
}

// status and doctor say which state directory they looked at and why.
func TestStatusAndDoctorSayWhereTheStateDirectoryIs(t *testing.T) {
	st := angelTestState()
	_ = stubAngels(t, probeRunning(st), nil)
	cwd := inTempDir(t)
	t.Setenv("NETIVRI_HOME", filepath.Join(t.TempDir(), "env"))
	cli.UseJSON()

	var out bytes.Buffer
	if err := runStatus([]string{"--local"}, &out); err != nil {
		t.Fatal(err)
	}
	data, ok := decodeUpData(t, out)
	if !ok || data["home"] != filepath.Join(cwd, ".netivri") || data["home_source"] != homeFromLocalFlag {
		t.Errorf("--local: home %v source %v", data["home"], data["home_source"])
	}
	out.Reset()
	if err := runStatus(nil, &out); err != nil { // the directory exists now
		t.Fatal(err)
	}
	if data, _ = decodeUpData(t, out); data["home_source"] != homeFromLocalDir {
		t.Errorf("auto: source %v", data["home_source"])
	}
	out.Reset()
	other := testutil.TempHome(t)
	if err := runStatus([]string{"--home", other}, &out); err != nil {
		t.Fatal(err)
	}
	if data, _ = decodeUpData(t, out); data["home"] != other || data["home_source"] != homeFromFlag {
		t.Errorf("--home: home %v source %v", data["home"], data["home_source"])
	}

	doctorFixTime(t)
	doctorAngelDown(t)
	doctorStubListen(t, nil)
	out.Reset()
	_ = runDoctor([]string{"--home", other}, &out)
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("doctor output: %v\n%s", err, out.String())
	}
	if env.Data["home"] != other || env.Data["home_source"] != homeFromFlag {
		t.Errorf("doctor: home %v source %v", env.Data["home"], env.Data["home_source"])
	}
}
