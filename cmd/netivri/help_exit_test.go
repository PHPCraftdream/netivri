package main

import (
	"strings"
	"testing"
)

func TestAccountExitHelpDocumentsDirectSafetyAndCommands(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"account", []string{"set-exit", "direct", "exposes this machine's address"}},
		{"account add", []string{"--exit {route|direct}", "CONFIG_INVALID", "post proxy"}},
		{"account clone", []string{"--exit {route|direct}", "CONFIG_INVALID"}},
		{"account show", []string{"direct", "no node tiers"}},
		{"config", []string{"accounts.exit", "direct"}},
		{"routing", []string{"direct", "post proxy"}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			topic := findHelpTopic(tc.name)
			if topic == nil {
				t.Fatalf("missing help topic %q", tc.name)
			}
			var out strings.Builder
			out.WriteString(topic.Summary + "\n" + topic.Description + "\n")
			for _, flag := range topic.Flags {
				out.WriteString(flag.Name + " " + flag.Meaning + "\n")
			}
			for _, err := range topic.Errors {
				out.WriteString(err.Code + " " + err.Meaning + "\n")
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("help %q lacks %q", tc.name, want)
				}
			}
		})
	}
}

func TestAccountExitHelpExamplesUseFlagsAndComments(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"account add", "--exit direct"},
		{"account clone", "--exit direct"},
		{"account set-exit", "set-exit"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			topic := findHelpTopic(tc.name)
			if topic == nil {
				t.Fatalf("missing help topic %q", tc.name)
			}
			for _, example := range topic.Examples {
				if strings.TrimSpace(example.Comment) != "" && strings.Contains(example.Command, tc.want) {
					return
				}
			}
			t.Errorf("help %q has no commented example containing %q", tc.name, tc.want)
		})
	}
}
