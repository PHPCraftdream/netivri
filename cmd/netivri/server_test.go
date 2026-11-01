package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

type serverEnvelope struct {
	SchemaVersion int            `json:"schema_version"`
	OK            bool           `json:"ok"`
	Data          map[string]any `json:"data"`
	Error         *cli.Error     `json:"error"`
}

func serverDispatch(t *testing.T, home string, args ...string) (serverEnvelope, string, error) {
	t.Helper()
	args = append(args, "--home", home, "--json")
	var out bytes.Buffer
	err := dispatch(append([]string{"server"}, args...), &out)
	if err != nil {
		return serverEnvelope{}, out.String(), err
	}
	var env serverEnvelope
	if e := json.Unmarshal(out.Bytes(), &env); e != nil {
		t.Fatalf("invalid envelope JSON: %v", e)
	}
	return env, out.String(), err
}
func serverCommandError(t *testing.T, home string, args ...string) error {
	t.Helper()
	args = append(args, "--home", home, "--json")
	var out bytes.Buffer
	return dispatch(append([]string{"server"}, args...), &out)
}

func serverTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal("save server test config")
	}
	return home
}
func TestServerCommandsGoldenJSON(t *testing.T) {
	home := serverTestHome(t)
	cases := []struct {
		name  string
		args  []string
		want  string
		check func(*testing.T, serverEnvelope)
	}{
		{"protocols", []string{"protocols"}, `"ok":true`, func(t *testing.T, e serverEnvelope) {
			p := e.Data["protocols"].([]any)
			if len(p) == 0 {
				t.Fatal("expected registry protocols")
			}
		}},
		{"add", []string{"add", "--protocol", "shadowsocks", "--account", "alice", "--port", "38493", "--bind", "127.0.0.1", "--public-host", "edge.example.org", "--param", "cipher=aes-128-gcm"}, `"bind":"127.0.0.1"`, func(t *testing.T, e serverEnvelope) {
			s := e.Data["server"].(map[string]any)
			if s["name"] != "ss-38493" || s["protocol"] != "shadowsocks" {
				t.Fatalf("add data=%v", s)
			}
		}},
		{"list", []string{"list"}, `"params":["cipher"]`, func(t *testing.T, e serverEnvelope) {
			raw, _ := json.Marshal(e.Data)
			if strings.Contains(string(raw), "aes-128-gcm") || strings.Contains(string(raw), "password") {
				t.Fatal("list exposed params or secret")
			}
		}},
		{"show", []string{"show", "ss-38493"}, `"public_host":"edge.example.org"`, func(t *testing.T, e serverEnvelope) {
			raw, _ := json.Marshal(e.Data)
			if strings.Contains(string(raw), "aes-128-gcm") || strings.Contains(string(raw), "password") {
				t.Fatal("show exposed params or secret")
			}
		}},
		{"link", []string{"link", "ss-38493", "--host", "override.example.net"}, `"format":"uri"`, func(t *testing.T, e serverEnvelope) {
			if e.Data["format"] != "uri" || !strings.Contains(e.Data["link"].(string), "override.example.net") {
				t.Fatal("link format/host incorrect")
			}
		}},
		{"rotate", []string{"rotate", "ss-38493", "--account", "alice"}, `"rotated":"ss-38493"`, func(t *testing.T, e serverEnvelope) {
			if e.Data["rotated"] != "ss-38493" {
				t.Fatalf("rotate data=%v", e.Data)
			}
		}},
		{"remove", []string{"remove", "ss-38493"}, `"removed":"ss-38493"`, func(t *testing.T, e serverEnvelope) {
			if e.Data["removed"] != "ss-38493" {
				t.Fatalf("remove data=%v", e.Data)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, raw, err := serverDispatch(t, home, tc.args...)
			if err != nil || !env.OK || env.SchemaVersion != cli.SchemaVersion {
				t.Fatalf("unexpected envelope: ok=%v err=%v", env.OK, err)
			}
			if !strings.Contains(raw, tc.want) {
				t.Fatalf("response did not contain expected JSON fields")
			}
			tc.check(t, env)
		})
	}
}
func TestServerCommandErrorsAndSubcommandFlags(t *testing.T) {
	home := serverTestHome(t)
	cases := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"missing", []string{"show", "missing"}, "SERVER_NOT_FOUND", cli.ExitNotFound},
		{"unknown arg on show", []string{"show", "x", "--host", "h"}, "USAGE", cli.ExitUsage},
		{"unknown arg on remove", []string{"remove", "x", "--format", "uri"}, "USAGE", cli.ExitUsage},
		{"unsupported qr", []string{"link", "x", "--qr"}, "USAGE", cli.ExitUsage},
		{"invalid host", []string{"link", "ss-38494", "--host", "bad host"}, "USAGE", cli.ExitUsage},
	}
	for _, tc := range cases {
		if tc.name == "unsupported qr" {
			if _, _, err := serverDispatch(t, home, "add", "--protocol", "shadowsocks", "--account", "alice", "--port", "38494", "--bind", "127.0.0.1", "--param", "cipher=aes-128-gcm"); err != nil {
				t.Fatalf("fixture add failed: %v", err)
			}
			tc.args = []string{"link", "ss-38494", "--qr"}
		}
		err := serverCommandError(t, home, tc.args...)
		var ce *cli.Error
		if !errors.As(err, &ce) {
			t.Errorf("%s: got err=%v, want cli error", tc.name, err)
		} else if ce.Code != tc.code || ce.Exit != tc.exit {
			t.Errorf("%s: code/exit=%s/%d want %s/%d", tc.name, ce.Code, ce.Exit, tc.code, tc.exit)
		}
	}
}
