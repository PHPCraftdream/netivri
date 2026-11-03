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

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func runInteractiveBinary(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eFirstLineTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, args...)
	cmd.Stdin = strings.NewReader("")
	cmd.Env = append(os.Environ(), "NETIVRI_HOME="+home, "NETIVRI_OUTPUT=ktav")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("netivri did not exit within %v (stdout: %q, stderr: %q)", e2eFirstLineTimeout, stdout.String(), stderr.String())
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run netivri: %v", err)
	}
	return code, stdout.String(), stderr.String()
}

func TestInteractiveE2EOutputAndTerminalRequirements(t *testing.T) {
	t.Run("json is usage error", func(t *testing.T) {
		code, stdout, stderr := runInteractiveBinary(t, t.TempDir(), "-i", "--json")
		if code != cli.ExitUsage || stdout != "" {
			t.Fatalf("exit/stdout = %d/%q, want usage and empty stdout", code, stdout)
		}
		var env errEnvelope
		if err := json.Unmarshal([]byte(stderr), &env); err != nil || env.Error == nil || env.Error.Code != "USAGE" || env.SchemaVersion != cli.SchemaVersion || env.OK {
			t.Fatalf("stderr error = %q, want versioned JSON USAGE envelope", stderr)
		}
	})

	t.Run("pipes are not a terminal", func(t *testing.T) {
		code, stdout, stderr := runInteractiveBinary(t, t.TempDir(), "-i")
		if code != cli.ExitUsage || stdout != "" {
			t.Fatalf("exit/stdout = %d/%q, want usage and empty stdout", code, stdout)
		}
		if !strings.Contains(stderr, "NOT_A_TERMINAL") || !strings.Contains(stderr, "netivri -i") || !strings.Contains(stderr, "netivri sub -i") || !strings.Contains(stderr, "netivri account -i") || !strings.Contains(stderr, "netivri server -i") {
			t.Fatalf("stderr %q lacks NOT_A_TERMINAL or complete entry-point hint", stderr)
		}
	})

	t.Run("status interactive is usage error", func(t *testing.T) {
		code, stdout, stderr := runInteractiveBinary(t, t.TempDir(), "status", "-i")
		if code != cli.ExitUsage || stdout != "" {
			t.Fatalf("exit/stdout = %d/%q, want usage and empty stdout", code, stdout)
		}
		if !strings.Contains(stderr, "USAGE") || !strings.Contains(stderr, "netivri -i") || !strings.Contains(stderr, "netivri sub -i") || !strings.Contains(stderr, "netivri account -i") || !strings.Contains(stderr, "netivri server -i") {
			t.Fatalf("stderr %q lacks USAGE or complete entry-point hint", stderr)
		}
	})
}
