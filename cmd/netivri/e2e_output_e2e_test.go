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

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

// runOutput runs the real binary with NETIVRI_OUTPUT removed from the
// environment, plus extra variables.
func runOutput(t *testing.T, extraEnv []string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eIOTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, netivriBin, args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NETIVRI_OUTPUT=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run netivri: %v", err)
	}
	return code, stdout.String(), stderr.String()
}

func loadKtavDoc(t *testing.T, text string) map[string]any {
	t.Helper()
	doc, err := ktav.Loads(text)
	if err != nil {
		t.Fatalf("not Ktav: %v\n%s", err, text)
	}
	m, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("a Ktav document is not an object: %v", doc)
	}
	return m
}

func TestE2EOutputIsKtavByDefault(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()

	code, stdout, stderr := runOutput(t, nil, "version")
	if code != 0 || stderr != "" {
		t.Fatalf("version: exit %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "schema_version: 1\nok: true\n") {
		t.Errorf("a Ktav result starts with the schema version and ok: %q", stdout)
	}
	doc := loadKtavDoc(t, stdout)
	if doc["ok"] != true || doc["data"].(map[string]any)["version"] == nil {
		t.Errorf("version document = %v", doc)
	}

	// A list that is empty and a list with records.
	code, stdout, _ = runOutput(t, nil, "sub", "list", "--home", home)
	if code != 0 {
		t.Fatalf("sub list exit %d: %s", code, stdout)
	}
	if subs, ok := loadKtavDoc(t, stdout)["data"].(map[string]any)["subscriptions"].([]any); !ok || len(subs) != 0 {
		t.Errorf("sub list data = %v", loadKtavDoc(t, stdout)["data"])
	}

	// An error is Ktav on stderr and the exit code is unchanged.
	code, stdout, stderr = runOutput(t, nil, "account", "rm", "ghost", "--home", home)
	if code != cli.ExitNotFound || stdout != "" {
		t.Fatalf("account rm ghost: exit %d, stdout %q", code, stdout)
	}
	e := loadKtavDoc(t, stderr)
	if e["ok"] != false || e["error"].(map[string]any)["code"] != "ACCOUNT_NOT_FOUND" {
		t.Errorf("error document = %v", e)
	}

	// Usage errors before any command parsing are Ktav too.
	code, _, stderr = runOutput(t, nil, "frobnicate")
	if code != cli.ExitUsage {
		t.Fatalf("unknown command exit = %d", code)
	}
	if loadKtavDoc(t, stderr)["error"].(map[string]any)["code"] != "USAGE" {
		t.Errorf("usage error = %s", stderr)
	}
}

func TestE2EJSONOnRequest(t *testing.T) {
	testutil.RequireKtavLib(t)
	home := t.TempDir()
	for name, c := range map[string]struct {
		env  []string
		args []string
	}{
		"flag":        {nil, []string{"version", "--json"}},
		"environment": {[]string{"NETIVRI_OUTPUT=json"}, []string{"version"}},
	} {
		code, stdout, stderr := runOutput(t, c.env, c.args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: exit %d, stderr %q", name, code, stderr)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(stdout), &m); err != nil || m["ok"] != true {
			t.Errorf("%s: not the JSON envelope: %v\n%s", name, err, stdout)
		}
	}
	code, _, stderr := runOutput(t, nil, "account", "rm", "ghost", "--home", home, "--json")
	var m map[string]any
	if code != cli.ExitNotFound || json.Unmarshal([]byte(stderr), &m) != nil || m["ok"] != false {
		t.Errorf("JSON error: exit %d, stderr %q", code, stderr)
	}
}

// Help is text unless --json is given, in either default format.
func TestE2EHelpIsTextAndJSONOnRequest(t *testing.T) {
	code, stdout, _ := runOutput(t, nil, "help")
	if code != 0 || !strings.Contains(stdout, "USAGE") || strings.HasPrefix(stdout, "schema_version") {
		t.Errorf("help is not text: exit %d\n%s", code, stdout)
	}
	code, stdout, _ = runOutput(t, nil, "help", "--json")
	var m map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &m) != nil || m["ok"] != true {
		t.Errorf("help --json is not the JSON envelope: exit %d\n%s", code, stdout)
	}
}
