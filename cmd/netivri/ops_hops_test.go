package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestValidateCLIHopListPreservesConfigErrors(t *testing.T) {
	const secretUser = "fake-hop-user"
	const secretPassword = "fake-hop-password"
	cases := []struct {
		name        string
		hops        config.HopList
		wantMessage string
	}{
		{
			name:        "duplicate",
			hops:        config.HopList{"socks5://same.example:1080", "socks5://same.example:1080"},
			wantMessage: "pre_socks5 must not contain duplicate hop URLs",
		},
		{
			name:        "none must stand alone",
			hops:        config.HopList{"none", "socks5://other.example:1080"},
			wantMessage: "pre_socks5: none must stand alone (must not mix none with hop URLs)",
		},
		{
			name:        "invalid singleton URL",
			hops:        config.HopList{"ftp://" + secretUser + ":" + secretPassword + "@invalid.example:21"},
			wantMessage: "pre_socks5 must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port",
		},
		{
			name:        "invalid second URL",
			hops:        config.HopList{"socks5://one.example:1080", "ftp://" + secretUser + ":" + secretPassword + "@invalid.example:21"},
			wantMessage: "pre_socks5[1] must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port",
		},
		{
			name:        "empty second entry",
			hops:        config.HopList{"socks5://one.example:1080", ""},
			wantMessage: "pre_socks5[1] must not contain empty hop entries",
		},
		{
			name:        "invalid third URL",
			hops:        config.HopList{"socks5://one.example:1080", "http://two.example:8080", "ftp://" + secretUser + ":" + secretPassword + "@invalid.example:21"},
			wantMessage: "pre_socks5[2] must be empty, none, or a valid socks5://, http:// or https:// URL with a host and port",
		},
		{
			name:        "empty third entry",
			hops:        config.HopList{"socks5://one.example:1080", "http://two.example:8080", ""},
			wantMessage: "pre_socks5[2] must not contain empty hop entries",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCLIHopList(tc.hops, "pre_socks5")
			if err == nil {
				t.Fatal("invalid hop list was accepted")
			}
			outputs := []string{err.Error()}
			var cliErr *cli.Error
			if errors.As(err, &cliErr) {
				outputs = append(outputs, cliErr.Message, cliErr.Hint)
			}
			for _, output := range outputs {
				if strings.Contains(output, secretUser) || strings.Contains(output, secretPassword) {
					t.Fatal("hop validation error exposed fake credentials")
				}
			}
			if !errors.As(err, &cliErr) {
				t.Fatal("hop validation error was not a CLI error")
			}
			if cliErr.Code != config.CodeConfigInvalid || cliErr.Exit != cli.ExitUsage {
				t.Fatalf("hop validation returned code %q and exit %d", cliErr.Code, cliErr.Exit)
			}
			if cliErr.Message != tc.wantMessage {
				t.Fatalf("hop validation message = %q, want %q", cliErr.Message, tc.wantMessage)
			}
		})
	}
}

func TestApplyHopEditUsesOriginalIndicesAndCopies(t *testing.T) {
	original := config.HopList{"first", "second", "third"}
	validate := func(hops config.HopList) error {
		if len(hops) > 3 {
			return errors.New("test capacity exceeded")
		}
		return nil
	}
	got, err := applyHopEdit(original, HopEdit{Remove: []int{1, 3}}, validate)
	if err != nil || !reflect.DeepEqual(got, config.HopList{"second"}) {
		t.Fatalf("remove = %v, %v", got, err)
	}
	got[0] = "changed"
	if !reflect.DeepEqual(original, config.HopList{"first", "second", "third"}) {
		t.Fatalf("edit modified original: %v", original)
	}

	added, err := applyHopEdit(config.HopList{"first"}, HopEdit{Add: config.HopList{"second"}}, validate)
	if err != nil || !reflect.DeepEqual(added, config.HopList{"first", "second"}) {
		t.Fatalf("add = %v, %v", added, err)
	}
	if _, err := applyHopEdit(original, HopEdit{Add: config.HopList{"fourth"}}, validate); err == nil {
		t.Fatal("over-cap candidate accepted")
	}
	if !reflect.DeepEqual(original, config.HopList{"first", "second", "third"}) {
		t.Fatalf("failed edit modified original: %v", original)
	}
}

func TestApplyHopEditRejectsConflictsAndBadIndices(t *testing.T) {
	validate := func(config.HopList) error { return nil }
	replacement := config.HopList{"replacement"}
	cases := []HopEdit{
		{Replace: &replacement, Add: config.HopList{"another"}},
		{Add: config.HopList{"another"}, Remove: []int{1}},
		{Remove: []int{0}},
		{Remove: []int{2}},
		{Remove: []int{1, 1}},
	}
	for i, edit := range cases {
		if _, err := applyHopEdit(config.HopList{"original"}, edit, validate); err == nil {
			t.Errorf("case %d accepted: %+v", i, edit)
		}
	}
}

func TestParseHopIndicesRejectsMalformedAndEmptyInput(t *testing.T) {
	for _, input := range [][]string{nil, {""}, {"0"}, {"-1"}, {"+1"}, {" 1"}, {"1x"}, {"1", "1"}} {
		if _, err := parseHopIndices(input, "--remove-pre"); err == nil {
			t.Errorf("indices %q accepted", input)
		}
	}
	got, err := parseHopIndices([]string{"3", "1"}, "--remove-pre")
	if err != nil || !reflect.DeepEqual(got, []int{3, 1}) {
		t.Fatalf("valid index order = %v, %v", got, err)
	}
}

func TestParseHopEditFlagsRejectsMixedModes(t *testing.T) {
	cases := []struct {
		name                                string
		replace, add, remove                []string
		replaceGiven, addGiven, removeGiven bool
	}{
		{"replace and add", []string{"socks5://one.example:1080"}, []string{"socks5://two.example:1080"}, nil, true, true, false},
		{"replace and remove", []string{"none"}, nil, []string{"1"}, true, false, true},
		{"add and remove", nil, []string{"socks5://two.example:1080"}, []string{"1"}, false, true, true},
		{"empty replacement", []string{""}, nil, nil, true, false, false},
		{"empty add", nil, []string{""}, nil, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseHopEditFlags(tc.replace, tc.add, tc.remove, tc.replaceGiven, tc.addGiven, tc.removeGiven, "pre"); err == nil {
				t.Fatal("invalid flag combination accepted")
			}
		})
	}
}

func TestApplyHopEditUsesInjectedCapacityAboveProductionLimit(t *testing.T) {
	validateTwo := func(hops config.HopList) error {
		if len(hops) > 2 {
			return errors.New("test capacity exceeded")
		}
		return nil
	}
	got, err := applyHopEdit(config.HopList{"first"}, HopEdit{Add: config.HopList{"second"}}, validateTwo)
	if err != nil || !reflect.DeepEqual(got, config.HopList{"first", "second"}) {
		t.Fatalf("two-hop injected candidate = %v, %v", got, err)
	}
}

func TestParseHopEditFlagsSingletonsAndValidation(t *testing.T) {
	edit, given, err := parseHopEditFlags([]string{"inherit"}, nil, nil, true, false, false, "pre")
	if err != nil || !given || edit.Replace == nil || len(*edit.Replace) != 0 {
		t.Fatalf("inherit = %+v, %v, given %v", edit, err, given)
	}
	edit, given, err = parseHopEditFlags([]string{"none"}, nil, nil, true, false, false, "post")
	if err != nil || !given || !reflect.DeepEqual(*edit.Replace, config.HopList{"none"}) {
		t.Fatalf("none = %+v, %v, given %v", edit, err, given)
	}
	for _, values := range [][]string{
		{"inherit", "socks5://host.example:1080"},
		{"none", "socks5://host.example:1080"},
		{"socks5://host.example:1080", "socks5://host.example:1080"},
		{"bad"},
	} {
		if _, _, err := parseHopEditFlags(values, nil, nil, true, false, false, "pre"); err == nil {
			t.Errorf("invalid replacement %q accepted", values)
		}
	}
	if _, _, err := parseHopEditFlags(nil, []string{"none"}, nil, false, true, false, "pre"); err == nil {
		t.Fatal("none accepted as an incremental addition")
	}
	if _, _, err := parseHopEditFlags(nil, []string{"socks5://same.example:1080", "socks5://same.example:1080"}, nil, false, true, false, "pre"); err == nil {
		t.Fatal("duplicate incremental additions accepted")
	}
}

func TestRedactChainsReturnsArrayAndRedactsEveryHop(t *testing.T) {
	got := redactChains(config.HopList{
		"socks5://first-user:first-secret@first.example:1080",
		"https://second-user:second-secret@second.example:8443",
	})
	if !reflect.DeepEqual(got, []string{"socks5://first.example:1080", "https://second.example:8443"}) {
		t.Fatalf("redacted array = %v", got)
	}
	for _, secret := range []string{"first-user", "first-secret", "second-user", "second-secret"} {
		if strings.Contains(strings.Join(got, ","), secret) {
			t.Fatalf("redaction leaked %q", secret)
		}
	}
	if got := redactChains(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty redaction must be a JSON array: %#v", got)
	}
}
