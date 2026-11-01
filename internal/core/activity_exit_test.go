package core

import (
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestActivityLabelForDirectPostHelpers(t *testing.T) {
	for _, name := range []string{"netivri-r0-0-post", "netivri-g2-r4-0-post"} {
		t.Run(name, func(t *testing.T) {
			if got := activityLabel(C.Chain{name}, nil); got != ActivityDirect {
				t.Fatalf("activityLabel(%q) = %q, want %q", name, got, ActivityDirect)
			}
		})
	}
}

func TestActivityLabelPrefersKnownRoutePostLabel(t *testing.T) {
	const helper = "netivri-g2-r4-0-post"
	labels := map[string]string{helper: "main/de-1"}
	if got := activityLabel(C.Chain{helper}, labels); got != "main/de-1" {
		t.Fatalf("activityLabel(%q) = %q, want retained route label", helper, got)
	}
}

func TestActivityLabelDirectAndUnrecognizedChains(t *testing.T) {
	cases := []struct {
		name  string
		chain C.Chain
		want  string
	}{
		{"plain direct", C.Chain{"DIRECT"}, ActivityDirect},
		{"unknown chain", C.Chain{"netivri-global-post"}, ActivityNode},
		{"empty chain", C.Chain{}, ActivityNode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activityLabel(tc.chain, nil); got != tc.want {
				t.Fatalf("activityLabel(%v) = %q, want %q", tc.chain, got, tc.want)
			}
		})
	}
}
