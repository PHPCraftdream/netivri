package main

import (
	"reflect"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestServerPublicMultiportMembers(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{
		{Name: "b", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345},
		{Name: "a", Protocol: "trojan", Bind: "127.0.0.1", Port: 12345},
	}}
	want := []string{"a", "b"}
	for _, server := range cfg.Servers {
		if got := serverPublic(cfg, server).Members; !reflect.DeepEqual(got, want) {
			t.Errorf("serverPublic(%q).Members = %v, want %v", server.Name, got, want)
		}
	}

	cfg.Servers = append(cfg.Servers, config.Server{Name: "udp", Protocol: "hysteria2", Bind: "127.0.0.1", Port: 12345})
	if got := serverPublic(cfg, cfg.Servers[0]).Members; !reflect.DeepEqual(got, want) {
		t.Errorf("TCP members = %v, want %v (UDP peer excluded)", got, want)
	}
	if got := serverPublic(nil, cfg.Servers[0]).Members; got != nil {
		t.Errorf("members without peers = %v, want nil", got)
	}
}
