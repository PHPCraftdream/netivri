package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/servers"
)

func TestServerAddTrialShareEligibilityAndDefault(t *testing.T) {
	home := serverTestHome(t)
	add := func(args ...string) error { return serverCommandError(t, home, args...) }
	if err := add("add", "--protocol", "shadowsocks", "--name", "trial-ss", "--account", "alice", "--port", "39101", "--bind", "127.0.0.1", "--param", "cipher=2022-blake3-aes-128-gcm"); err != nil {
		t.Fatal(err)
	}
	if err := add("add", "--protocol", "vmess", "--name", "trial-vm", "--account", "alice", "--port", "39101", "--bind", "127.0.0.1"); err != nil {
		t.Fatalf("eligible trial sharing failed: %v", err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{"legacy SS", []string{"add", "--protocol", "shadowsocks", "--name", "legacy", "--account", "alice", "--port", "39102", "--bind", "127.0.0.1", "--param", "cipher=aes-128-gcm"}, ""},
		{"snell", []string{"add", "--protocol", "snell", "--name", "snell", "--account", "alice", "--port", "39103", "--bind", "127.0.0.1"}, ""},
	} {
		if err := add(tc.args...); err != nil {
			t.Fatal(err)
		}
		var ce *cli.Error
		err := add("add", "--protocol", "vmess", "--name", "extra-"+strings.ToLower(strings.ReplaceAll(tc.name, " ", "-")), "--account", "alice", "--port", map[string]string{"legacy SS": "39102", "snell": "39103"}[tc.name], "--bind", "127.0.0.1")
		if !errors.As(err, &ce) || ce.Code != "PORT_IN_USE" {
			t.Errorf("%s conflict error=%v", tc.name, err)
		}
	}
	var ce *cli.Error
	err := add("add", "--protocol", "vmess", "--name", "default-trial", "--account", "alice", "--port", "39104", "--bind", "127.0.0.1", "--default")
	if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" || ce.Exit != cli.ExitConflict {
		t.Fatalf("trial default error=%v", err)
	}
}

func TestServerAddTrialSecretCollisionRollsBack(t *testing.T) {
	home := serverTestHome(t)
	first := []string{"add", "--protocol", "vmess", "--name", "vm-one", "--account", "alice", "--port", "39121", "--bind", "127.0.0.1"}
	if err := serverCommandError(t, home, first...); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	old := servers.Spec{Name: "vm-one", Protocol: "vmess", Bind: "127.0.0.1", Port: 39121, Accounts: []string{"alice"}}
	loaded, err := store.Apply(old)
	if err != nil {
		t.Fatal(err)
	}
	oldID := loaded.Users["alice"]["uuid"]
	if err = os.WriteFile(store.Path(), []byte("servers: {\n  vm-one: { material: {}, users: { alice: { uuid: \""+oldID+"\" } } }\n  vm-two: { material: {}, users: { alice: { uuid: \""+oldID+"\" } } }\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var ce *cli.Error
	err = serverCommandError(t, home, "add", "--protocol", "vmess", "--name", "vm-two", "--account", "alice", "--port", "39121", "--bind", "127.0.0.1")
	if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
		t.Fatalf("collision err=%v", err)
	}
	cfg, loadErr := loadConfig(home)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, index := cfg.GetServer("vm-two"); index >= 0 {
		t.Fatal("ambiguous server was saved")
	}
}

func TestServerAddVMessUserLimit(t *testing.T) {
	for _, n := range []int{513, 1024, 1025} {
		home := serverTestHome(t)
		cfg, err := loadConfig(home)
		if err != nil {
			t.Fatal(err)
		}
		logins := make([]string, n)
		for i := range logins {
			login := "u" + strconv.Itoa(i)
			logins[i] = login
			cfg.Accounts = append(cfg.Accounts, config.Account{Login: login, Password: config.InitPlaceholder})
		}
		if err := config.Save(config.ConfigPath(home), cfg); err != nil {
			t.Fatal(err)
		}
		args := []string{"add", "--protocol", "vmess", "--name", "vm-limit", "--account", strings.Join(logins, ","), "--port", "39210", "--bind", "127.0.0.1"}
		err = serverCommandError(t, home, args...)
		if n <= 1024 && err != nil {
			t.Fatalf("%d VMess users rejected: %v", n, err)
		}
		if n == 1025 {
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
				t.Fatalf("1025 VMess users error=%v", err)
			}
		}
	}
}

func TestServerAddTrialGroupLimitIncludesSSMembersNotUsers(t *testing.T) {
	for _, count := range []int{16, 17} {
		home := serverTestHome(t)
		for i := 0; i < count; i++ {
			proto, name := "shadowsocks", "ss"+strconv.Itoa(i)
			args := []string{"add", "--protocol", proto, "--name", name, "--account", "alice", "--port", "39211", "--bind", "127.0.0.1", "--param", "cipher=2022-blake3-aes-128-gcm"}
			err := serverCommandError(t, home, args...)
			if i < 16 && err != nil {
				t.Fatalf("adding SS member %d: %v", i+1, err)
			}
			if i == 16 {
				var ce *cli.Error
				if !errors.As(err, &ce) || ce.Code != "SERVER_PORT_AMBIGUOUS" {
					t.Fatalf("adding 17th SS member error=%v", err)
				}
			}
		}
	}
}

func TestServerAddSSAfterMultiuserVMess(t *testing.T) {
	home := serverTestHome(t)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	users := make([]string, 20)
	for i := range users {
		users[i] = "u" + strconv.Itoa(i)
		cfg.Accounts = append(cfg.Accounts, config.Account{Login: users[i], Password: config.InitPlaceholder})
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if err := serverCommandError(t, home, "add", "--protocol", "vmess", "--name", "vm-many", "--account", strings.Join(users, ","), "--port", "39250", "--bind", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := serverCommandError(t, home, "add", "--protocol", "shadowsocks", "--name", "ss-one", "--account", "alice", "--port", "39250", "--bind", "127.0.0.1", "--param", "cipher=2022-blake3-aes-128-gcm"); err != nil {
		t.Fatalf("Shadowsocks sharing beside multiuser VMess rejected: %v", err)
	}
}

func TestServerAddMixedTLSAndTrialBothOrders(t *testing.T) {
	for i, protocols := range [][]string{{"trojan", "vmess"}, {"vmess", "trojan"}} {
		home := serverTestHome(t)
		for j, protocol := range protocols {
			name := "member" + strconv.Itoa(j)
			args := []string{"add", "--protocol", protocol, "--name", name, "--account", "alice", "--port", strconv.Itoa(39220 + i), "--bind", "127.0.0.1"}
			if protocol == "trojan" {
				args = append(args, "--param", "sni=tls.example")
			}
			if err := serverCommandError(t, home, args...); err != nil {
				t.Fatalf("order %v adding %s: %v", protocols, protocol, err)
			}
		}
	}
}

func TestServerAddTCPAndUDPMaySharePort(t *testing.T) {
	home := serverTestHome(t)
	if err := serverCommandError(t, home, "add", "--protocol", "vmess", "--name", "tcp", "--account", "alice", "--port", "39230", "--bind", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := serverCommandError(t, home, "add", "--protocol", "hysteria2", "--name", "udp", "--account", "alice", "--port", "39230", "--bind", "127.0.0.1"); err != nil {
		t.Fatalf("UDP peer on TCP port rejected: %v", err)
	}
}

func TestRound1CLIAndConfigNonTrialDefaultErrorMatch(t *testing.T) {
	cases := []struct {
		name, protocol string
		params         []string
	}{{"snell", "snell", nil}, {"legacy shadowsocks", "shadowsocks", []string{"--param", "cipher=aes-128-gcm"}}, {"UDP hysteria2", "hysteria2", nil}}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := serverTestHome(t)
			args := []string{"add", "--protocol", tc.protocol, "--name", "default", "--account", "alice", "--port", strconv.Itoa(39110 + i), "--bind", "127.0.0.1", "--default"}
			args = append(args, tc.params...)
			err := serverCommandError(t, home, args...)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != "CONFIG_INVALID" || ce.Message != "servers[0].default default requires a TLS-sniffable protocol" || ce.Hint != "fix the value in config.ktav; see netivri help config" || ce.Exit != cli.ExitUsage {
				t.Fatalf("CLI error = %#v", err)
			}
		})
	}
}

func TestRound1CLIAndConfigTrialDefaultErrorMatch(t *testing.T) {
	cases := []struct {
		protocol string
		params   []string
	}{{"shadowsocks", []string{"--param", "cipher=2022-blake3-aes-128-gcm"}}, {"vmess", nil}}
	for i, tc := range cases {
		home := serverTestHome(t)
		port := 39120 + i
		args := []string{"add", "--protocol", tc.protocol, "--name", "trial", "--account", "alice", "--port", strconv.Itoa(port), "--bind", "127.0.0.1", "--default"}
		args = append(args, tc.params...)
		err := serverCommandError(t, home, args...)
		var cliErr *cli.Error
		if !errors.As(err, &cliErr) {
			t.Fatalf("CLI error = %v", err)
		}
		cfg, loadErr := loadConfig(home)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		params := map[string]string{}
		if len(tc.params) > 0 {
			params["cipher"] = "2022-blake3-aes-128-gcm"
		}
		cfg.Servers = append(cfg.Servers, config.Server{Name: "trial", Protocol: tc.protocol, Bind: "127.0.0.1", Port: port, Accounts: "alice", Default: true, Params: params})
		configErr := cfg.Validate()
		var configCLI *cli.Error
		if !errors.As(configErr, &configCLI) || cliErr.Code != configCLI.Code || cliErr.Message != configCLI.Message || cliErr.Hint != configCLI.Hint || cliErr.Exit != configCLI.Exit || cliErr.Code != "SERVER_PORT_AMBIGUOUS" || cliErr.Hint == "" {
			t.Fatalf("CLI error = %#v, config error = %#v", cliErr, configCLI)
		}
	}
}

func TestDoctorGroupsTrialPeersByTransport(t *testing.T) {
	home := serverTestHome(t)
	cfg, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers = []config.Server{
		{Name: "ss", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39240, Accounts: "alice", Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}},
		{Name: "vm", Protocol: "vmess", Bind: "127.0.0.1", Port: 39240, Accounts: "alice"},
		{Name: "udp", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 39240, Accounts: "alice"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	store := servers.NewStore(home)
	for _, spec := range []servers.Spec{
		{Name: "ss", Protocol: "shadowsocks", Bind: "127.0.0.1", Port: 39240, Accounts: []string{"alice"}, Params: map[string]string{"cipher": "2022-blake3-aes-128-gcm"}},
		{Name: "vm", Protocol: "vmess", Bind: "127.0.0.1", Port: 39240, Accounts: []string{"alice"}},
		{Name: "udp", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 39240, Accounts: []string{"alice"}},
	} {
		if _, err := store.EnsureSecrets(spec); err != nil {
			t.Fatal(err)
		}
	}
	d := &doctorContext{home: home, cfg: cfg, angelUp: true}
	for _, check := range d.checkServers() {
		if check.Status == doctorFail {
			t.Fatalf("doctor rejected TCP/UDP peer group: %+v", check)
		}
	}
}
