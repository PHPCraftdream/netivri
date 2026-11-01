package core

import (
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestPrepareRoutesDirectBypassesStickyGroups(t *testing.T) {
	tests := []struct {
		name      string
		opts      Options
		wantRule  string
		wantProxy string
	}{
		{
			name:     "plain direct",
			opts:     Options{AccountRoute: map[string]string{"alice": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "none"}},
			wantRule: "IN-USER,alice,DIRECT",
		},
		{
			name:      "post direct",
			opts:      Options{AccountRoute: map[string]string{"alice": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "socks5://post.example:1080"}},
			wantRule:  "IN-USER,alice,netivri-r0-0-post",
			wantProxy: "netivri-r0-0-post",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies, groups, rules, err := prepareRoutes(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(groups) != 0 {
				t.Fatalf("direct route installed groups: %v", groups)
			}
			if rules[0] != tt.wantRule || rules[len(rules)-1] != "MATCH,DIRECT" {
				t.Fatalf("rules = %v", rules)
			}
			if tt.wantProxy == "" {
				if len(proxies) != 0 {
					t.Fatalf("plain direct proxies = %v", proxies)
				}
				return
			}
			if len(proxies) != 1 || proxies[0]["name"] != tt.wantProxy || proxies[0]["dialer-proxy"] != "DIRECT" {
				t.Fatalf("direct-post proxy = %v", proxies)
			}
		})
	}
}

func TestDirectPlansHaveNoNodeLabelsOrHealthMembers(t *testing.T) {
	for _, options := range []Options{
		{AccountRoute: map[string]string{"alice": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "none"}},
		{AccountRoute: map[string]string{"alice": "direct"}, RoutePre: map[string]string{"direct": "none"}, RoutePost: map[string]string{"direct": "socks5://post.example:1080"}},
	} {
		plans, err := buildRoutePlans(options)
		if err != nil {
			t.Fatal(err)
		}
		if len(plans) != 0 || len(planLabels(plans)) != 0 || len(memberNameLabels(plans)) != 0 {
			t.Fatalf("direct route plans/labels = %#v/%v/%v", plans, planLabels(plans), memberNameLabels(plans))
		}
	}
}

func TestDirectPlanOmissionKeepsRouteIndexing(t *testing.T) {
	opts := Options{
		AccountRoute: map[string]string{"directUser": "direct", "nodeUser": "r"},
		RoutePre:     map[string]string{"direct": "none"},
		RoutePost:    map[string]string{"direct": "none"},
		RouteNodes:   map[string][]map[string]any{"r": {socks5Node()}},
	}
	proxies, groups, rules, err := prepareRoutesGen(opts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0]["name"] != "netivri-g2-group-1" {
		t.Fatalf("route groups = %v", groups)
	}
	if rules[0] != "IN-USER,directUser,DIRECT" || rules[1] != "IN-USER,nodeUser,netivri-g2-group-1" {
		t.Fatalf("rules = %v", rules)
	}
	plans, err := buildRoutePlansGen(opts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].index != 1 || len(proxies) == 0 {
		t.Fatalf("plans/proxies = %#v/%v", plans, proxies)
	}
}

func TestDirectRouteDoesNotRelaxMissingNodeFailure(t *testing.T) {
	_, _, _, err := prepareRoutes(Options{AccountRoute: map[string]string{"alice": "route"}})
	if err == nil {
		t.Fatal("route with missing nodes unexpectedly succeeded")
	}
	if cliErr, ok := err.(*cli.Error); !ok || cliErr.Code != "NO_NODES_FOR_COUNTRY" {
		t.Fatalf("error = %v, want NO_NODES_FOR_COUNTRY", err)
	}
}
