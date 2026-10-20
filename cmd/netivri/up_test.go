package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func okEnv(string) string       { return "" }
func okConfig() (string, error) { return tConfigDir, nil }

// tConfigDir is the fake user config dir of the running test; parseUp takes
// it as a function, so no global state of the os package is touched.
var tConfigDir string

func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	tConfigDir = dir
	t.Cleanup(func() { tConfigDir = "" })
}

func TestParseUpDefaults(t *testing.T) {
	setConfigDir(t, t.TempDir())
	opts, err := parseUp([]string{"--foreground"}, okEnv, okConfig)
	if err != nil {
		t.Fatalf("parseUp: %v", err)
	}
	if !opts.Foreground {
		t.Error("Foreground = false, want true")
	}
	if opts.Port != 7890 {
		t.Errorf("Port = %d, want 7890", opts.Port)
	}
	if opts.Bind != "127.0.0.1" {
		t.Errorf("Bind = %q, want 127.0.0.1", opts.Bind)
	}
	if opts.Home != filepath.Join(tConfigDir, "netivri") {
		t.Errorf("Home = %q, want config-dir/netivri", opts.Home)
	}
}

func TestParseUpFlags(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cases := []struct {
		name string
		args []string
		want upOptions
	}{
		{
			name: "explicit flags",
			args: []string{"--foreground", "--port", "1234", "--bind", "::1", "--home", "/tmp/h", "--json", "--quiet", "--no-color"},
			want: upOptions{Foreground: true, Port: 1234, Bind: "::1", PortSet: true, BindSet: true, Home: "/tmp/h"},
		},
		{
			name: "equals form and port 0",
			args: []string{"-foreground", "--port=0", "--bind=localhost"},
			want: upOptions{Foreground: true, Port: 0, Bind: "localhost", PortSet: true, BindSet: true, Home: filepath.Join(tConfigDir, "netivri")},
		},
		{
			name: "single-dash boolean form",
			args: []string{"-foreground"},
			want: upOptions{Foreground: true, Port: 7890, Bind: "127.0.0.1", Home: filepath.Join(tConfigDir, "netivri")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseUp(tc.args, okEnv, okConfig)
			if err != nil {
				t.Fatalf("parseUp(%v): %v", tc.args, err)
			}
			if opts != tc.want {
				t.Errorf("opts = %+v, want %+v", opts, tc.want)
			}
		})
	}
}

func TestParseUpUsageErrors(t *testing.T) {
	setConfigDir(t, t.TempDir())
	cases := []struct {
		name        string
		args        []string
		wantMessage string // fixed prefix read from the implementation
	}{
		{"invalid int", []string{"--foreground", "--port", "abc"}, "up: invalid value"},
		{"unknown flag", []string{"--foreground", "--wat"}, "up: flag provided but not defined"},
		{"positional argument", []string{"--foreground", "extra"}, "up: unexpected argument \"extra\""},
		{"handshake without foreground", []string{"--handshake", "hs"}, "up: --handshake requires"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseUp(tc.args, okEnv, okConfig)
			ce, ok := err.(*cli.Error)
			if !ok {
				t.Fatalf("want *cli.Error, got %T (%v)", err, err)
			}
			if ce.Code != "USAGE" || ce.Exit != cli.ExitUsage {
				t.Errorf("code/exit = %q/%d, want USAGE/%d", ce.Code, ce.Exit, cli.ExitUsage)
			}
			if len(ce.Message) < len(tc.wantMessage) || ce.Message[:len(tc.wantMessage)] != tc.wantMessage {
				t.Errorf("message = %q, want prefix %q", ce.Message, tc.wantMessage)
			}
		})
	}
}

func TestParseUpNonLoopbackBind(t *testing.T) {
	// parseUp itself no longer rejects non-loopback binds: that check
	// moves to mergeUp, which knows whether authentication is enabled.
	for _, bind := range []string{"0.0.0.0", "192.168.1.5", "example.com", ""} {
		t.Run("bind="+bind, func(t *testing.T) {
			opts, err := parseUp([]string{"--foreground", "--bind", bind}, okEnv, okConfig)
			if err != nil {
				t.Fatalf("parseUp(--bind %s): %v", bind, err)
			}
			if opts.Bind != bind || !opts.BindSet {
				t.Errorf("Bind/BindSet = %q/%v, want %q/true", opts.Bind, opts.BindSet, bind)
			}
		})
	}
}

func TestParseUpHomePrecedence(t *testing.T) {
	setConfigDir(t, filepath.Join(t.TempDir(), "conf"))
	envHome := filepath.Join(t.TempDir(), "envhome")
	flagHome := filepath.Join(t.TempDir(), "flaghome")

	t.Run("home flag wins", func(t *testing.T) {
		opts, err := parseUp([]string{"--foreground", "--home", flagHome}, func(string) string { return envHome }, okConfig)
		if err != nil {
			t.Fatalf("parseUp: %v", err)
		}
		if opts.Home != flagHome {
			t.Errorf("Home = %q, want --home value %q", opts.Home, flagHome)
		}
	})

	t.Run("env wins over config dir", func(t *testing.T) {
		opts, err := parseUp([]string{"--foreground"}, func(string) string { return envHome }, okConfig)
		if err != nil {
			t.Fatalf("parseUp: %v", err)
		}
		if opts.Home != envHome {
			t.Errorf("Home = %q, want NETIVRI_HOME %q", opts.Home, envHome)
		}
	})

	t.Run("config dir fallback", func(t *testing.T) {
		opts, err := parseUp([]string{"--foreground"}, okEnv, okConfig)
		if err != nil {
			t.Fatalf("parseUp: %v", err)
		}
		if want := filepath.Join(tConfigDir, "netivri"); opts.Home != want {
			t.Errorf("Home = %q, want %q", opts.Home, want)
		}
	})

	t.Run("env empty string falls through", func(t *testing.T) {
		opts, err := parseUp([]string{"--foreground"}, func(string) string { return "" }, okConfig)
		if err != nil {
			t.Fatalf("parseUp: %v", err)
		}
		if want := filepath.Join(tConfigDir, "netivri"); opts.Home != want {
			t.Errorf("Home = %q, want %q", opts.Home, want)
		}
	})
}

func TestParseUpUserConfigDirError(t *testing.T) {
	_, err := parseUp([]string{"--foreground"}, okEnv, func() (string, error) { return "", errors.New("no config dir") })
	ce, ok := err.(*cli.Error)
	if !ok {
		t.Fatalf("want *cli.Error, got %T (%v)", err, err)
	}
	if ce.Code != "INTERNAL" || ce.Exit != cli.ExitInternal {
		t.Errorf("code/exit = %q/%d, want INTERNAL/%d", ce.Code, ce.Exit, cli.ExitInternal)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.9.9.9", true}, // 127.0.0.0/8 per docs/cli.md
		{"::1", true},
		{"localhost", true},
		{"LOCALHOST", false}, // exact lowercase match in the implementation
		{"0.0.0.0", false},
		{"::", false},
		{"10.0.0.1", false},
		{"", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
