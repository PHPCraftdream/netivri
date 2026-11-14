package main

import (
	"reflect"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func globalPostPoolConfig() *config.Config {
	cfg := config.Default()
	cfg.Chain.PostSOCKS5 = config.HopList{"socks5://first.example:1080", "http://second.example:8080"}
	return cfg
}

func TestGlobalPostPoolConfigLoadSaveConfigSetAndDoctor(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	want := globalPostPoolConfig()
	if err := config.Save(path, want); err != nil {
		t.Fatalf("save global post pool: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load global post pool: %v", err)
	}
	if !reflect.DeepEqual(loaded.Chain.PostSOCKS5, want.Chain.PostSOCKS5) {
		t.Fatalf("loaded global post pool = %v, want %v", loaded.Chain.PostSOCKS5, want.Chain.PostSOCKS5)
	}
	if check := (&doctorContext{home: home}).checkConfig(); check.Status != doctorOK {
		t.Fatalf("doctor config check = %+v, want ok", check)
	}

	result, err := ConfigSet(home, "listen.port", "7891")
	if err != nil {
		t.Fatalf("config set with global post pool: %v", err)
	}
	if !result.Changed || !result.RestartNeeded {
		t.Fatalf("config set result = %+v, want changed/restart", result)
	}
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("load config after setting port: %v", err)
	}
	if loaded.Listen.Port != 7891 || !reflect.DeepEqual(loaded.Chain.PostSOCKS5, want.Chain.PostSOCKS5) {
		t.Fatalf("config set lost settings: port=%d global post=%v", loaded.Listen.Port, loaded.Chain.PostSOCKS5)
	}
}

func TestGlobalPostPoolLegacyAuthClaimAndAccountRemoval(t *testing.T) {
	home := t.TempDir()
	path := config.ConfigPath(home)
	cfg := globalPostPoolConfig()
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save global post pool: %v", err)
	}
	if err := newPersist(path, cfg)("legacy", "$argon2id$claimed"); err != nil {
		t.Fatalf("legacy auth claim with global post pool: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load after legacy auth claim: %v", err)
	}
	if loaded.Auth.Login != "legacy" || !reflect.DeepEqual(loaded.Chain.PostSOCKS5, cfg.Chain.PostSOCKS5) {
		t.Fatalf("legacy claim lost config: auth login=%q global post=%v", loaded.Auth.Login, loaded.Chain.PostSOCKS5)
	}

	accountHome := t.TempDir()
	accountCfg := globalPostPoolConfig()
	accountCfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
	accountPath := config.ConfigPath(accountHome)
	if err := config.Save(accountPath, accountCfg); err != nil {
		t.Fatalf("save account config with global post pool: %v", err)
	}
	if err := AccountRemove(accountHome, "alice"); err != nil {
		t.Fatalf("remove last account with global post pool: %v", err)
	}
	loaded, err = config.Load(accountPath)
	if err != nil {
		t.Fatalf("load after account removal: %v", err)
	}
	if len(loaded.Accounts) != 0 || !reflect.DeepEqual(loaded.Chain.PostSOCKS5, accountCfg.Chain.PostSOCKS5) {
		t.Fatalf("account removal result: accounts=%d global post=%v", len(loaded.Accounts), loaded.Chain.PostSOCKS5)
	}
}
