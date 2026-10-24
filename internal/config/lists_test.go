package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestParseCountryList(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"de", []string{"DE"}},
		{"de,NL", []string{"DE", "NL"}},
		{" nl , de ", []string{"NL", "DE"}},
		{"DE,de,NL,DE", []string{"DE", "NL"}}, // the first of a repeat keeps its place
	} {
		got, ok := ParseCountryList(tc.in)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseCountryList(%q) = %v, %v; want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{",", "DE,", ",DE", "DE,,NL", "D", "DEU", "D1", "é1", "DE NL"} {
		if got, ok := ParseCountryList(in); ok {
			t.Errorf("ParseCountryList(%q) = %v, want invalid", in, got)
		}
	}
	codes := make([]string, 0, MaxAccountCountries+1)
	for i := 0; i <= MaxAccountCountries; i++ {
		codes = append(codes, string(rune('A'+i/26))+string(rune('A'+i%26)))
	}
	if _, ok := ParseCountryList(strings.Join(codes[:MaxAccountCountries], ",")); !ok {
		t.Error("the longest allowed country list was rejected")
	}
	if _, ok := ParseCountryList(strings.Join(codes, ",")); ok {
		t.Error("a country list over the limit was accepted")
	}
}

func TestParseProviderList(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"main", []string{"main"}},
		{"main,backup", []string{"main", "backup"}},
		{"main, backup ,main", []string{"main", "backup"}},
	} {
		got, ok := ParseProviderList(tc.in)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseProviderList(%q) = %v, %v; want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{",", "a,", "a,,b", "a b", "a/b", "é", strings.Repeat("x", 65)} {
		if got, ok := ParseProviderList(in); ok {
			t.Errorf("ParseProviderList(%q) = %v, want invalid", in, got)
		}
	}
	names := make([]string, 0, MaxAccountProviders+1)
	for i := 0; i <= MaxAccountProviders; i++ {
		names = append(names, "p"+string(rune('a'+i)))
	}
	if _, ok := ParseProviderList(strings.Join(names[:MaxAccountProviders], ",")); !ok {
		t.Error("the longest allowed provider list was rejected")
	}
	if _, ok := ParseProviderList(strings.Join(names, ",")); ok {
		t.Error("a provider list over the limit was accepted")
	}
}

func TestValidateAccountLists(t *testing.T) {
	valid := func(provider, country string) *Config {
		c := Default()
		c.Accounts = []Account{{Login: "alice", Password: InitPlaceholder, Provider: provider, ExitCountry: country}}
		return c
	}
	for _, tc := range [][2]string{{"main,backup", "DE,NL"}, {"main", "DE"}, {"", "DE,NL"}, {"main,backup", ""}} {
		if err := valid(tc[0], tc[1]).Validate(); err != nil {
			t.Errorf("%v: %v", tc, err)
		}
	}
	for _, tc := range []struct{ provider, country, field string }{
		{"main,", "", "provider"},
		{"main,,b", "", "provider"},
		{"", "DE,", "exit_country"},
		{"", "DE,NLD", "exit_country"},
	} {
		err := valid(tc.provider, tc.country).Validate()
		ce, ok := err.(*cli.Error)
		if !ok || ce.Code != CodeConfigInvalid || !strings.HasPrefix(ce.Message, "accounts[0]."+tc.field+" must be") {
			t.Errorf("%+v: error = %#v", tc, err)
		}
	}
}

func TestLoadCanonicalizesAccountLists(t *testing.T) {
	fixture := `listen.bind: 127.0.0.1
listen.port: 7890
accounts: [
    {
        login: alice
        password:: init
        provider: p2, p1 ,p2
        exit_country: nl, de,NL
    }
]
`
	path := filepath.Join(t.TempDir(), "lists.ktav")
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if a := loaded.Accounts[0]; a.Provider != "p2,p1" || a.ExitCountry != "NL,DE" {
		t.Fatalf("loaded %+v, want provider p2,p1 and exit_country NL,DE", a)
	}
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, loaded) {
		t.Fatalf("roundtrip mismatch: %#v", reloaded)
	}
}
