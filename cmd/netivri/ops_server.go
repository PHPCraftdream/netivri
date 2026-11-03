package main

import (
	"github.com/PHPCraftdream/netivri/internal/config"
)

// ServerList returns safe public views of configured servers. Runtime counters
// are included when the local angel is available.
func ServerList(home string) ([]serverView, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	return serverListConfig(home, cfg)
}

func serverListConfig(home string, cfg *config.Config) ([]serverView, error) {
	items := make([]serverView, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		items = append(items, serverPublic(cfg, server))
	}
	overlayServerRuntimeCounters(home, items)
	return items, nil
}

// ServerShow returns one safe public view, or SERVER_NOT_FOUND.
func ServerShow(home, name string) (serverView, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return serverView{}, err
	}
	return serverShowConfig(home, cfg, name)
}

func serverShowConfig(home string, cfg *config.Config, name string) (serverView, error) {
	entry, _ := cfg.GetServer(name)
	if entry == nil {
		return serverView{}, serverNotFound()
	}
	views := []serverView{serverPublic(cfg, *entry)}
	overlayServerRuntimeCounters(home, views)
	return views[0], nil
}
