package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestAccountSOCKS5DispatchAddFlagsAndPersistence(t *testing.T) {
	home := t.TempDir()
	pre := "socks5://pre-user:pre-secret@pre.example:1080"
	post := "socks5://post-user:post-secret@post.example:1080"
	out := runAccountOK(t, home, "add", "alice", "--pre-socks5", pre, "--post-socks5", post)
	if strings.Contains(string(out), "pre-secret") || strings.Contains(string(out), "post-secret") {
		t.Fatalf("add disclosed credentials: %s", out)
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.Account("alice")
	if a == nil || a.PreSOCKS5 != pre || a.PostSOCKS5 != post {
		t.Fatalf("persisted chain = %+v", a)
	}
	// Flags after the positional login are also accepted.
	if got := runAccountOK(t, home, "add", "bob", "--pre-socks5", "socks5://pre.example:1080", "--post-socks5", "socks5://post.example:1080"); !bytes.Contains(got, []byte(`"login":"bob"`)) {
		t.Fatalf("add output %s", got)
	}
}

func TestAccountSOCKS5DispatchSetAndErrors(t *testing.T) {
	home := t.TempDir()
	globalPre, globalPost := "socks5://global-pre.example:1080", "socks5://global-post.example:1080"
	cfg := config.Default()
	cfg.Chain.PreSOCKS5, cfg.Chain.PostSOCKS5 = globalPre, globalPost
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	runAccountOK(t, home, "add", "alice")
	pre, post := "socks5://pre-user:pre-secret@pre.example:1080", "socks5://post-user:post-secret@post.example:1080"
	runAccountOK(t, home, "set-socks5", "alice", "--pre", pre, "--post", post)
	runAccountOK(t, home, "set-socks5", "alice", "--pre", "inherit")
	runAccountOK(t, home, "set-socks5", "alice", "--post", "none")
	got, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := got.Account("alice")
	if a.PreSOCKS5 != "" || a.PostSOCKS5 != "none" {
		t.Fatalf("chain = %+v", a)
	}
	for _, args := range [][]string{
		{"set-socks5", "alice"},
		{"set-socks5", "alice", "--pre", "http://user:super-secret@invalid.example"},
		{"set-socks5", "alice", "--unknown", "x"},
		{"set-socks5", "missing", "--pre", "inherit"},
	} {
		before, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = runAccount(append(args, "--home", home), &out)
		var ce *cli.Error
		if !errors.As(err, &ce) {
			t.Errorf("runAccount(%q) error = %v, want *cli.Error", args, err)
			continue
		}
		wantCode, wantExit := "USAGE", cli.ExitUsage
		wantMessage := "account set-socks5: requires --pre and/or --post"
		switch {
		case strings.Contains(strings.Join(args, " "), "invalid.example"):
			wantCode, wantMessage = "CONFIG_INVALID", "invalid --pre SOCKS5 URL"
		case strings.Contains(strings.Join(args, " "), "--unknown"):
			wantMessage = "account set-socks5: requires ACCOUNT[,ACCOUNT...] and --pre and/or --post"
		case strings.Contains(strings.Join(args, " "), "missing"):
			wantCode, wantExit, wantMessage = "ACCOUNT_NOT_FOUND", cli.ExitNotFound, "account not found: missing"
		}
		if ce.Code != wantCode || ce.Exit != wantExit || ce.Message != wantMessage {
			t.Errorf("runAccount(%q) error = %#v, want code/exit/message %q/%d/%q", args, ce, wantCode, wantExit, wantMessage)
		}
		if strings.Contains(ce.Error()+out.String(), "super-secret") {
			t.Errorf("runAccount(%q) disclosed URL credentials", args)
		}
		after, err := os.ReadFile(config.ConfigPath(home))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("runAccount(%q) changed config on error", args)
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
		{"no flags", "set-socks5 alice", "USAGE", "account set-socks5: requires --pre and/or --post", cli.ExitUsage},
		{"bad --pre URL", "set-socks5 alice --pre http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "invalid --pre SOCKS5 URL", cli.ExitUsage},
		{"bad --post URL", "set-socks5 alice --post http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "invalid --post SOCKS5 URL", cli.ExitUsage},
		{"bad --pre-socks5 URL", "add bob --pre-socks5 http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "invalid --pre-socks5 SOCKS5 URL", cli.ExitUsage},
		{"bad --post-socks5 URL", "add bob --post-socks5 http://user:" + secret + "@invalid.example", "CONFIG_INVALID", "invalid --post-socks5 SOCKS5 URL", cli.ExitUsage},
		{"unknown flag", "set-socks5 alice --unknown x", "USAGE", "account set-socks5: requires ACCOUNT[,ACCOUNT...] and --pre and/or --post", cli.ExitUsage},
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
				t.Fatalf("dispatch error = %#v, want %s/%d/%q", err, tc.code, tc.exit, tc.message)
			}
			exit := cli.Render(&stderr, err)
			if exit != tc.exit {
				t.Fatalf("render exit = %d, want %d", exit, tc.exit)
			}
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatalf("stderr is not JSON: %v (%q)", err, stderr.String())
			}
			if envelope.OK || envelope.Error.Code != tc.code || envelope.Error.Message != tc.message || stdout.Len() != 0 {
				t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String()+ce.Error(), secret) {
				t.Fatalf("credential leaked: stdout=%q stderr=%q error=%v", stdout.String(), stderr.String(), ce)
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
	runAccountOK(t, home, "add", "alice", "--pre-socks5", pre, "--post-socks5", post)
	clone := runAccountOK(t, home, "clone", "alice", "bob")
	if strings.Contains(string(clone), "pre-secret") || strings.Contains(string(clone), "post-secret") {
		t.Fatalf("clone disclosed credentials: %s", clone)
	}
	cfg, err := config.LoadOrDefault(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := cfg.Account("bob")
	if bob == nil || bob.PreSOCKS5 != pre || bob.PostSOCKS5 != post {
		t.Fatalf("cloned account = %+v", bob)
	}
	if _, err := os.Stat(config.ConfigPath(home)); err != nil {
		t.Fatalf("config.ktav not persisted: %v", err)
	}
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"list"}, {"show", "alice"}} {
		stdout.Reset()
		stderr.Reset()
		if err := runAccount(append(args, "--home", home), &stdout); err != nil {
			t.Fatalf("account %v: %v", args, err)
		}
		for _, secret := range []string{"pre-user", "pre-secret", "post-user", "post-secret"} {
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Errorf("account %v disclosed %q in stdout/stderr", args, secret)
			}
		}
	}
}
