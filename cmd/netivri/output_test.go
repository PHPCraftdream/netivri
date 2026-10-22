package main

import (
	"bytes"
	"testing"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/testutil"
)

func TestWantsJSON(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "NETIVRI_OUTPUT" {
				return v
			}
			return ""
		}
	}
	cases := []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"nothing", []string{"status"}, "", false},
		{"flag", []string{"status", "--json"}, "", true},
		{"flag first", []string{"--json", "status"}, "", true},
		{"single dash", []string{"status", "-json"}, "", true},
		{"explicit true", []string{"status", "--json=true"}, "", true},
		{"explicit false", []string{"status", "--json=false"}, "", false},
		{"after a bare --", []string{"sub", "add", "--", "--json"}, "", false},
		{"environment", []string{"status"}, "json", true},
		{"other environment value", []string{"status"}, "ktav", false},
		{"similar flag", []string{"status", "--jsonl"}, "", false},
	}
	for _, tc := range cases {
		if got := wantsJSON(tc.args, env(tc.env)); got != tc.want {
			t.Errorf("%s: wantsJSON(%v, %q) = %v, want %v", tc.name, tc.args, tc.env, got, tc.want)
		}
	}
}

func useKtavOutput(t *testing.T) {
	t.Helper()
	testutil.RequireKtavLib(t)
	if err := config.EnsureKtavLib(); err != nil {
		t.Fatal(err)
	}
	cli.UseKtav(config.KtavDumps)
	t.Cleanup(cli.UseJSON)
}

// Real Ktav rendering of the shapes the commands print: nested objects, lists
// of records, empty lists, null, numbers, booleans and strings that look like
// numbers.
func TestKtavOutputRoundTrips(t *testing.T) {
	useKtavOutput(t)
	var out bytes.Buffer
	cli.Success(&out, map[string]any{
		"version":  "0.1.0",
		"accounts": []accountInfo{{Login: "123456", Provider: "main", ExitCountry: "DE", PasswordSet: true}, {Login: "bob"}},
		"empty":    []any{},
		"none":     nil,
		"count":    3,
		"ratio":    0.5,
		"running":  true,
		"nested":   map[string]any{"name": "\U0001F1FA\U0001F1F8 USA", "delay_ms": int64(177)},
	})
	got, err := ktav.Loads(out.String())
	if err != nil {
		t.Fatalf("the output is not Ktav: %v\n%s", err, out.String())
	}
	doc := got.(map[string]any)
	if doc["ok"] != true || doc["schema_version"] != int64(cli.SchemaVersion) {
		t.Fatalf("envelope header = %v / %v\n%s", doc["ok"], doc["schema_version"], out.String())
	}
	data := doc["data"].(map[string]any)
	if data["version"] != "0.1.0" || data["count"] != int64(3) || data["running"] != true || data["none"] != nil {
		t.Errorf("scalars = %v", data)
	}
	accounts := data["accounts"].([]any)
	first := accounts[0].(map[string]any)
	if len(accounts) != 2 || first["login"] != "123456" || first["password_set"] != true || first["exit_country"] != "DE" {
		t.Errorf("accounts = %v (a numeric-looking login must stay a string)", accounts)
	}
	if list, ok := data["empty"].([]any); !ok || len(list) != 0 {
		t.Errorf("empty list = %v", data["empty"])
	}
	if nested := data["nested"].(map[string]any); nested["delay_ms"] != int64(177) {
		t.Errorf("nested = %v", nested)
	}
}

func TestKtavErrorOutput(t *testing.T) {
	useKtavOutput(t)
	var out bytes.Buffer
	code := cli.Render(&out, &cli.Error{Code: "SUB_NOT_FOUND", Message: "no subscription with that name", Hint: "run: netivri sub list", Exit: cli.ExitNotFound})
	if code != cli.ExitNotFound {
		t.Errorf("exit = %d", code)
	}
	got, err := ktav.Loads(out.String())
	if err != nil {
		t.Fatalf("the error is not Ktav: %v\n%s", err, out.String())
	}
	doc := got.(map[string]any)
	e := doc["error"].(map[string]any)
	if doc["ok"] != false || e["code"] != "SUB_NOT_FOUND" || e["hint"] != "run: netivri sub list" {
		t.Errorf("error document = %v", doc)
	}
	if _, has := doc["data"]; has {
		t.Error("an error document has data")
	}
}

func TestKtavOutputOfACommand(t *testing.T) {
	useKtavOutput(t)
	var out bytes.Buffer
	if err := runVersion(nil, &out); err != nil {
		t.Fatal(err)
	}
	got, err := ktav.Loads(out.String())
	if err != nil {
		t.Fatalf("version output is not Ktav: %v\n%s", err, out.String())
	}
	if data := got.(map[string]any)["data"].(map[string]any); data["version"] != version {
		t.Errorf("data = %v, want version %q", data, version)
	}
}
