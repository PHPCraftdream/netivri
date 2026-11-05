package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/stats"
)

// The Stats tab and the `stats` command share StatsRead, so for every mode
// the command's JSON output must be exactly what StatsRead produced
// (docs/design/parity.md).
func TestOpsStatsReadMatchesCommand(t *testing.T) {
	home := t.TempDir()
	seedStats(t, home)
	cases := []struct {
		name string
		args []string
		opts StatsOptions
	}{
		{"nodes over an hour", []string{"--since", "1h"}, StatsOptions{Window: time.Hour}},
		{"default window", nil, StatsOptions{}},
		{"by country", []string{"--by", "country"}, StatsOptions{By: "country"}},
		{"by provider", []string{"--by", "provider"}, StatsOptions{By: "provider"}},
		{"by account", []string{"--by", "account"}, StatsOptions{By: "account"}},
		{"sub filter", []string{"--sub", "alt"}, StatsOptions{Sub: "alt"}},
		{"country filter lowercase", []string{"--country", "de"}, StatsOptions{Country: "de"}},
		{"node filter", []string{"--node", "main/a"}, StatsOptions{Node: "main/a"}},
		{"account filter", []string{"--account", "alice"}, StatsOptions{Account: "alice"}},
		{"account and country", []string{"--by", "account", "--country", "DE"}, StatsOptions{By: "account", Country: "DE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runStats(append(append([]string{}, tc.args...), "--home", home), &out); err != nil {
				t.Fatal(err)
			}
			res, err := StatsRead(home, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			var shared bytes.Buffer
			cli.Success(&shared, map[string]any{
				"since": res.Window.String(),
				"by":    res.By,
				"items": res.Items,
			})
			if out.String() != shared.String() {
				t.Fatalf("StatsRead output differs from the command:\ncommand: %s\nshared:  %s", out.String(), shared.String())
			}
			var data map[string]any
			if err := json.Unmarshal(shared.Bytes(), &data); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
		})
	}
}

func TestOpsStatsReadErrors(t *testing.T) {
	home := t.TempDir()
	_, err := StatsRead(home, StatsOptions{})
	if !statsUnavailable(err) {
		t.Fatalf("without a database: %#v", err)
	}
	seedStats(t, home)
	if _, err := StatsRead(home, StatsOptions{By: "city"}); !isUsage(err) {
		t.Fatalf("bad by: %#v", err)
	}
	if _, err := StatsRead(home, StatsOptions{Country: "germany"}); !isUsage(err) {
		t.Fatalf("bad country: %#v", err)
	}
}

func TestOpsStatsReadOpenFailureIsInternal(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(statsPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := StatsRead(home, StatsOptions{})
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "INTERNAL" {
		t.Fatalf("not an internal error: %#v", err)
	}
}

// statsItems groups rows with an empty country under the documented
// "unknown" key.
func TestOpsStatsItemsUnknownCountryGroup(t *testing.T) {
	items := statsItems([]stats.NodeStats{{Label: "x/y", Provider: "p"}}, "country")
	if len(items) != 1 || items[0]["country"] != "unknown" {
		t.Fatalf("items = %v", items)
	}
}

func statsUnavailable(err error) bool {
	var ce *cli.Error
	return errors.As(err, &ce) && ce.Code == "STATS_UNAVAILABLE" && ce.Exit == cli.ExitNotFound
}

func isUsage(err error) bool {
	var ce *cli.Error
	return errors.As(err, &ce) && ce.Exit == cli.ExitUsage
}
