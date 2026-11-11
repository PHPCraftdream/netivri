package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func runAccountOKNoSecret(t *testing.T, home string, args ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := runAccount(append(args, "--home", home), &out); err != nil {
		t.Fatal("account operation failed")
	}
	return out.Bytes()
}

func TestAccountSOCKS5DispatchAddFlagsAndPersistence(t *testing.T) {
	home := t.TempDir()
	pre := "socks5://pre-user:pre-secret@pre.example:1080"
	post := "socks5://post-user:post-secret@post.example:1080"
	out := runAccountOKNoSecret(t, home, "add", "alice", "--pre-socks5", pre, "--post-socks5", post)
	if strings.Contains(string(out), "pre-secret") || strings.Contains(string(out), "post-secret") {
		t.Fatal("add output disclosed credentials")
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Account("alice")
	if a == nil || !slices.Equal(a.PreSOCKS5, config.HopList{pre}) || !slices.Equal(a.PostSOCKS5, config.HopList{post}) {
		t.Fatal("persisted account hops did not match the requested chains")
	}
	// Flags after the positional login are also accepted.
	got := runAccountOK(t, home, "add", "bob", "--pre-socks5", "socks5://pre.example:1080", "--post-socks5", "socks5://post.example:1080")
	if !bytes.Contains(got, []byte(`"login":"bob"`)) {
		t.Fatal("add output did not contain the created account")
	}
}

func TestAccountSOCKS5DispatchRepeatedReplacementRejectsOverCapWithoutWrite(t *testing.T) {
	home := t.TempDir()
	runAccountOK(t, home, "add", "alice")
	path := config.ConfigPath(home)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := "socks5://user:secret@proxy.example:1080"
	var out bytes.Buffer
	err = runAccount([]string{"set-socks5", "alice", "--pre", secret, "--pre", "socks5://other.example:1080", "--home", home}, &out)
	var cliErr *cli.Error
	if !errors.As(err, &cliErr) || cliErr.Code != "CONFIG_INVALID" {
		t.Fatal("repeated replacement did not return CONFIG_INVALID")
	}
	if err != nil && strings.Contains(err.Error()+out.String(), "secret") {
		t.Fatal("repeated replacement error leaked credentials")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("over-cap repeated replacement changed configuration")
	}
}

func TestAccountSOCKS5DispatchIncrementalEditUsesOriginalIndices(t *testing.T) {
	home := t.TempDir()
	runAccountOK(t, home, "add", "alice", "--pre-socks5", "socks5://pre.example:1080")
	runAccountOK(t, home, "set-socks5", "alice", "--remove-pre", "1")
	cfg, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Accounts[0].PreSOCKS5) != 0 {
		t.Fatal("removing original index 1 did not clear singleton list")
	}
	runAccountOK(t, home, "set-socks5", "alice", "--add-pre", "socks5://next.example:1080")
	cfg, err = config.Load(config.ConfigPath(home))
	if err != nil || !slices.Equal(cfg.Accounts[0].PreSOCKS5, config.HopList{"socks5://next.example:1080"}) {
		t.Fatal("incremental addition did not persist a singleton list")
	}
}

func TestAccountSOCKS5DispatchRepeatedFlagsAndConflictErrors(t *testing.T) {
	home := t.TempDir()
	runAccountOK(t, home, "add", "alice")
	for i, tc := range []struct {
		name string
		args []string
	}{
		{"replacement with none", []string{"set-socks5", "alice", "--pre", "none", "--pre", "socks5://other.example:1080"}},
		{"replacement and addition", []string{"set-socks5", "alice", "--pre", "inherit", "--add-pre", "socks5://other.example:1080"}},
		{"addition and removal", []string{"set-socks5", "alice", "--add-pre", "socks5://one.example:1080", "--remove-pre", "1"}},
		{"missing remove index", []string{"set-socks5", "alice", "--remove-pre"}},
		{"duplicate remove index", []string{"set-socks5", "alice", "--remove-pre", "1", "--remove-pre", "1"}},
		{"invalid remove index", []string{"set-socks5", "alice", "--remove-pre", "x"}},
	} {
		before, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runAccount(append(tc.args, "--home", home), &out)
		if err == nil {
			t.Fatalf("case %d (%s) accepted invalid flags", i, tc.name)
		}
		after, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("case %d (%s) wrote configuration on error", i, tc.name)
		}
	}
}

func TestAccountSOCKS5DispatchSetAndErrors(t *testing.T) {
	home := t.TempDir()
	globalPre, globalPost := "socks5://global-pre.example:1080", "socks5://global-post.example:1080"
	cfg := config.Default()
	cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5 = config.HopList{globalPre}, config.HopList{globalPost}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	runAccountOK(t, home, "add", "alice")
	pre, post := "socks5://pre-user:pre-secret@pre.example:1080", "socks5://post-user:post-secret@post.example:1080"
	runAccountOKNoSecret(t, home, "set-socks5", "alice", "--pre", pre, "--post", post)
	runAccountOK(t, home, "set-socks5", "alice", "--pre", "inherit")
	runAccountOK(t, home, "set-socks5", "alice", "--post", "none")
	got, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts[0].PreSOCKS5) != 0 || !slices.Equal(got.Accounts[0].PostSOCKS5, config.HopList{"none"}) {
		t.Error("account hops were not updated")
	}
	for i, tc := range []struct {
		name string
		args []string
	}{
		{"no flags", []string{"set-socks5", "alice"}},
		{"invalid pre URL", []string{"set-socks5", "alice", "--pre", "http://user:super-secret@invalid.example"}},
		{"unknown flag", []string{"set-socks5", "alice", "--unknown", "x"}},
		{"missing account", []string{"set-socks5", "missing", "--pre", "inherit"}},
	} {
		before, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runAccount(append(tc.args, "--home", home), &out)
		var ce *cli.Error
		if !errors.As(err, &ce) {
			t.Errorf("case %d (%s) did not return *cli.Error", i, tc.name)
			continue
		}
		wantCode, wantExit := "USAGE", cli.ExitUsage
		wantMessage := "account set-socks5: requires --pre/--post replacement or --add-pre/--add-post/--remove-pre/--remove-post"
		switch {
		case strings.Contains(strings.Join(tc.args, " "), "invalid.example"):
			wantCode, wantMessage = "CONFIG_INVALID", "--pre must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port"
		case strings.Contains(strings.Join(tc.args, " "), "--remove-pre"):
			wantMessage = "--remove-pre: expected one or more one-based indices"
		case strings.Contains(strings.Join(tc.args, " "), "--add-pre"):
			wantMessage = "--pre and --add-pre/--remove-pre cannot be combined"
		case strings.Contains(strings.Join(tc.args, " "), "--unknown"):
			wantMessage = "account set-socks5: flag provided but not defined: -unknown"
		case strings.Contains(strings.Join(tc.args, " "), "missing"):
			wantCode, wantExit, wantMessage = "ACCOUNT_NOT_FOUND", cli.ExitNotFound, "account not found: missing"
		}
		if ce.Code != wantCode || ce.Exit != wantExit || ce.Message != wantMessage {
			t.Errorf("case %d (%s) returned an unexpected code, exit, or message", i, tc.name)
		}
		if strings.Contains(ce.Error()+out.String(), "super-secret") {
			t.Errorf("case %d (%s) disclosed URL credentials", i, tc.name)
		}
		after, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("case %d (%s) changed configuration on error", i, tc.name)
		}
	}
}

func TestAccountSOCKS5TopLevelErrorsAreRedactedAndDoNotWrite(t *testing.T) {
	home := t.TempDir()
	runAccountOK(t, home, "add", "alice")
	secret := "super-secret"
	cases := []struct {
		name, action  string
		code, message string
		exit          int
	}{
		{"no flags", "set-socks5 alice", "USAGE", "account set-socks5: requires --pre/--post replacement or --add-pre/--add-post/--remove-pre/--remove-post", cli.ExitUsage},
		{"bad --pre URL", "set-socks5 alice --pre http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "--pre must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", cli.ExitUsage},
		{"bad --post URL", "set-socks5 alice --post http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "--post must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", cli.ExitUsage},
		{"bad --pre-socks5 URL", "add bob --pre-socks5 http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "--pre-socks5 must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", cli.ExitUsage},
		{"bad --post-socks5 URL", "add bob --post-socks5 http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "--post-socks5 must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port", cli.ExitUsage},
		{"unknown flag", "set-socks5 alice --unknown x", "USAGE", "account set-socks5: flag provided but not defined: -unknown", cli.ExitUsage},
		{"missing account", "set-socks5 missing --pre inherit", "ACCOUNT_NOT_FOUND", "account not found: missing", cli.ExitNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := config.ConfigPath(home)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			args := append(strings.Fields(tc.action), "--home", home, "--json")
			command := append([]string{"account"}, args...)
			var stdout, stderr bytes.Buffer
			cli.UseJSON()
			defer cli.UseJSON()
			err = dispatch(command, &stdout)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != tc.code || ce.Exit != tc.exit || ce.Message != tc.message {
				t.Fatalf("case %q returned an unexpected CLI error", tc.name)
			}
			exit := cli.Render(&stderr, err)
			if exit != tc.exit {
				t.Fatalf("case %q rendered an unexpected exit code", tc.name)
			}
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatalf("case %q rendered invalid JSON", tc.name)
			}
			if envelope.OK || envelope.Error.Code != tc.code || envelope.Error.Message != tc.message || stdout.Len() != 0 {
				t.Fatalf("case %q rendered an unexpected error envelope", tc.name)
			}
			if strings.Contains(stdout.String()+stderr.String()+ce.Error(), secret) {
				t.Fatalf("case %q disclosed URL credentials", tc.name)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("configuration file changed on failure")
			}
		})
	}
}
func TestAccountSOCKS5DispatchClonePersistenceAndOutputRedaction(t *testing.T) {
	home := t.TempDir()
	pre := "socks5://pre-user:pre-secret@pre.example:1080"
	post := "socks5://post-user:post-secret@post.example:1080"
	runAccountOKNoSecret(t, home, "add", "alice", "--pre-socks5", pre, "--post-socks5", post)
	clone := runAccountOK(t, home, "clone", "alice", "bob")
	if strings.Contains(string(clone), "pre-secret") || strings.Contains(string(clone), "post-secret") {
		t.Fatal("clone output disclosed credentials")
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := cfg.Account("bob")
	if bob == nil || !slices.Equal(bob.PreSOCKS5, config.HopList{pre}) || !slices.Equal(bob.PostSOCKS5, config.HopList{post}) {
		t.Fatal("cloned account hops did not match the source chains")
	}
	if _, err := os.Stat(config.ConfigPath(home)); err != nil {
		t.Fatalf("config.ktav not persisted: %v", err)
	}
	var stdout, stderr bytes.Buffer
	for i, args := range [][]string{{"list"}, {"show", "alice"}} {
		stdout.Reset()
		stderr.Reset()
		if err := runAccount(append(args, "--home", home), &stdout); err != nil {
			t.Fatalf("account output case %d failed", i)
		}
		for _, secret := range []string{"pre-user", "pre-secret", "post-user", "post-secret"} {
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Errorf("account output case %d disclosed credentials", i)
			}
		}
	}
}
