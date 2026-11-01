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

// A TCP and a UDP server may share one port number; two servers of the same
// transport on overlapping binds may not.
func TestServerAddAmbiguousTLSReturnsDedicatedError(t *testing.T) {
	home := serverTestHome(t)
	for _, args := range [][]string{
		{"add", "--protocol", "trojan", "--name", "tls-a", "--account", "alice", "--port", "38511", "--bind", "127.0.0.1", "--param", "sni=one.example"},
		{"add", "--protocol", "anytls", "--name", "tls-b", "--account", "alice", "--port", "38511", "--bind", "127.0.0.1", "--param", "sni=two.example"},
	} {
		if _, _, err := serverDispatch(t, home, args...); err != nil {
			t.Fatal(err)
		}
	}
	assertConflict := func(args ...string) {
		t.Helper()
		err := serverCommandError(t, home, args...)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Exit != cli.ExitConflict || ce.Hint == "" {
			t.Fatalf("error=%v, want hinted SERVER_PORT_AMBIGUOUS", err)
		}
	}
	assertConflict("add", "--protocol", "trojan", "--name", "tls-c", "--account", "alice", "--port", "38511", "--bind", "127.0.0.1", "--param", "sni=one.example")
	if _, _, err := serverDispatch(t, home, "add", "--protocol", "trojan", "--name", "tls-default", "--account", "alice", "--port", "38511", "--bind", "127.0.0.1", "--default", "--param", "sni=three.example"); err != nil {
		t.Fatalf("distinct SNI default server was refused: %v", err)
	}
	assertConflict("add", "--protocol", "trojan", "--name", "tls-default-two", "--account", "alice", "--port", "38511", "--bind", "127.0.0.1", "--default", "--param", "sni=four.example")
}

func TestServerAddMissingSNISharedTLSValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		second  []string
		wantErr bool
	}{
		{"shared no SNI refused", []string{"add", "--protocol", "anytls", "--name", "named", "--account", "alice", "--port", "38514", "--bind", "127.0.0.1", "--param", "sni=any.example"}, true},
		{"default permits missing SNI", []string{"add", "--protocol", "anytls", "--name", "named", "--account", "alice", "--port", "38515", "--bind", "127.0.0.1", "--param", "sni=any.example"}, false},
		{"explicit pair accepted", []string{"add", "--protocol", "anytls", "--name", "named", "--account", "alice", "--port", "38516", "--bind", "127.0.0.1", "--param", "sni=any.example"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := serverTestHome(t)
			first := []string{"add", "--protocol", "trojan", "--name", "plain", "--account", "alice", "--port", "38514", "--bind", "127.0.0.1"}
			if tc.name == "default permits missing SNI" {
				first = []string{"add", "--protocol", "trojan", "--name", "plain", "--account", "alice", "--port", "38515", "--bind", "127.0.0.1", "--default"}
			}
			if tc.name == "explicit pair accepted" {
				first = []string{"add", "--protocol", "trojan", "--name", "plain", "--account", "alice", "--port", "38516", "--bind", "127.0.0.1", "--param", "sni=trojan.example"}
			}
			if _, _, err := serverDispatch(t, home, first...); err != nil {
				t.Fatal(err)
			}
			_, _, err := serverDispatch(t, home, tc.second...)
			if tc.wantErr {
				var ce *cli.Error
				if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Hint != "set sni on plain or mark it as the default" {
					t.Fatalf("err=%v, want specific SERVER_PORT_AMBIGUOUS hint", err)
				}
			} else if err != nil {
				t.Fatalf("valid shared servers refused: %v", err)
			}
		})
	}
	// A lone server with no SNI is allowed.
	home := serverTestHome(t)
	if _, _, err := serverDispatch(t, home, "add", "--protocol", "trojan", "--name", "solo", "--account", "alice", "--port", "38517", "--bind", "127.0.0.1"); err != nil {
		t.Fatalf("solo no-SNI server refused: %v", err)
	}
}

func TestServerAddNonSnifferPortConflict(t *testing.T) {
	home := serverTestHome(t)
	if _, _, err := serverDispatch(t, home, "add", "--protocol", "shadowsocks", "--name", "ss-one", "--account", "alice", "--port", "38513", "--bind", "127.0.0.1", "--param", "cipher=aes-128-gcm"); err != nil {
		t.Fatal(err)
	}
	err := serverCommandError(t, home, "add", "--protocol", "trojan", "--name", "trojan-one", "--account", "alice", "--port", "38513", "--bind", "127.0.0.1", "--param", "sni=one.example")
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" || ce.Exit != cli.ExitConflict {
		t.Fatalf("error=%v, want PORT_IN_USE", err)
	}
}

func TestServerPublicListsSortedSharedEndpointPeers(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{{Name: "zeta", Protocol: "trojan", Bind: "127.0.0.1", Port: 38512}, {Name: "alpha", Protocol: "anytls", Bind: "127.0.0.1", Port: 38512, Default: true}}}
	got := serverPublic(cfg, cfg.Servers[0])
	if strings.Join(got.Members, ",") != "alpha,zeta" || len(got.Members) != 2 || !serverPublic(cfg, cfg.Servers[1]).Default {
		t.Fatalf("shared endpoint view: %#v", got)
	}
}

// A TCP and a UDP server may share one port number; two servers of the same
// transport on overlapping binds may not.
func TestServerAddPortConflictsArePerTransport(t *testing.T) {
	home := serverTestHome(t)
	if _, _, err := serverDispatch(t, home, "add", "--protocol", "shadowsocks", "--name", "tcp-one", "--account", "alice", "--port", "38500", "--bind", "127.0.0.1", "--param", "cipher=aes-128-gcm"); err != nil {
		t.Fatalf("first TCP server: %v", err)
	}
	if _, _, err := serverDispatch(t, home, "add", "--protocol", "hysteria2", "--name", "udp-one", "--account", "alice", "--port", "38500", "--bind", "127.0.0.1"); err != nil {
		t.Fatalf("a UDP server on the TCP server's port was refused: %v", err)
	}
	for name, args := range map[string][]string{
		"second TCP": {"add", "--protocol", "shadowsocks", "--name", "tcp-two", "--account", "alice", "--port", "38500", "--bind", "0.0.0.0", "--param", "cipher=aes-128-gcm"},
		"second UDP": {"add", "--protocol", "hysteria2", "--name", "udp-two", "--account", "alice", "--port", "38500", "--bind", "0.0.0.0"},
	} {
		err := serverCommandError(t, home, args...)
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" || ce.Exit != cli.ExitConflict {
			t.Errorf("%s: err = %v, want PORT_IN_USE", name, err)
		}
	}
}
