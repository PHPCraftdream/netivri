//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runHelpBinary runs the real binary with exactly the given arguments (unlike
// runNetivri, which always runs `up --foreground`) and returns the exit code,
// stdout and stderr.
func runHelpBinary(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eFirstLineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, args...)
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("netivri %q did not exit within %v", args, e2eFirstLineTimeout)
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run netivri %q: %v", args, err)
	}
	return code, outBuf.String(), errBuf.String()
}

func TestE2EHelpIsTextOnStdoutWithExitZero(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}} {
		code, stdout, stderr := runHelpBinary(t, home, args...)
		if code != 0 || stderr != "" {
			t.Errorf("netivri %q: exit %d, stderr %q", args, code, stderr)
		}
		if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
			t.Errorf("netivri %q printed JSON, want text: %q", args, stdout)
		}
		for _, want := range []string{"USAGE", "QUICK START", "COMMANDS", "netivri sub add", "EXIT CODES"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("netivri %q: stdout lacks %q", args, want)
			}
		}
	}
}

func TestE2EHelpOfOneCommand(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"help", "sub", "add"},
		{"sub", "add", "--help"},
		{"sub", "add", "https://provider.example/sub/x", "--name", "main", "--help"},
	} {
		code, stdout, stderr := runHelpBinary(t, home, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("netivri %q: exit %d, stderr %q", args, code, stderr)
		}
		for _, want := range []string{"netivri sub add - ", "FLAGS", "--via {PROXY}", "EXAMPLES", "ERRORS", "SUB_EXISTS_NAME"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("netivri %q: stdout lacks %q", args, want)
			}
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("asking for help touched the state directory: %v", entries)
	}
}

func TestE2EHelpJSON(t *testing.T) {
	home := t.TempDir()
	code, stdout, stderr := runHelpBinary(t, home, "help", "up", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	assertOneLine(t, stdout)
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Help struct {
				Kind     string `json:"kind"`
				Name     string `json:"name"`
				Examples []struct {
					Comment string `json:"comment"`
					Command string `json:"command"`
				} `json:"examples"`
			} `json:"help"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if !env.OK || env.Data.Help.Kind != "topic" || env.Data.Help.Name != "up" || len(env.Data.Help.Examples) == 0 {
		t.Errorf("unexpected help JSON: %+v", env)
	}
}

func TestE2EUnknownCommandPointsToHelp(t *testing.T) {
	home := t.TempDir()
	code, stdout, stderr := runHelpBinary(t, home, "frobnicate")
	if code != 2 || stdout != "" {
		t.Fatalf("exit %d, stdout %q, want exit 2 and empty stdout", code, stdout)
	}
	env := decodeErrEnvelope(t, stderr)
	if env.Error.Code != "USAGE" || !strings.Contains(env.Error.Hint, "netivri help") {
		t.Errorf("error = %+v, want USAGE with a hint to netivri help", env.Error)
	}
}
