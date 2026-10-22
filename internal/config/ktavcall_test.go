package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	ktav "github.com/ktav-lang/golang"

	"github.com/PHPCraftdream/netivri/internal/cli"
)

func TestKtavDumpsRendersDocument(t *testing.T) {
	requireKtavLib(t)
	got, err := KtavDumps(map[string]any{"ok": true, "name": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ok", "name", "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered document lacks %q:\n%s", want, got)
		}
	}
}

func TestKtavLoadsIntoReadsDocument(t *testing.T) {
	requireKtavLib(t)
	var got map[string]any
	if err := KtavLoadsInto("name: alice\n", &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "alice" {
		t.Fatalf("parsed = %v", got)
	}
}

// A parse error must not carry the source: for a stored subscription the
// text is a node credential.
func TestKtavLoadsIntoErrorHidesSource(t *testing.T) {
	requireKtavLib(t)
	const secret = "TOPSECRETVALUE"
	var got map[string]any
	err := KtavLoadsInto("outer: {\n    token: "+secret+"\n", &got)
	if err == nil {
		t.Fatal("an unclosed table was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error quotes the source: %v", err)
	}
}

func TestKtavWrappersReportMissingLibrary(t *testing.T) {
	t.Setenv("KTAV_LIB_PATH", filepath.Join(t.TempDir(), "no-such-lib.so"))
	var dst map[string]any
	for name, call := range map[string]func() error{
		"KtavDumps":     func() error { _, err := KtavDumps(map[string]any{"a": "b"}); return err },
		"KtavLoadsInto": func() error { return KtavLoadsInto("a: b\n", &dst) },
	} {
		err := call()
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != CodeKtavLibMissing {
			t.Errorf("%s with a missing library: %v, want %s", name, err, CodeKtavLibMissing)
		}
	}
}

func TestBindingErrorSummary(t *testing.T) {
	if got := bindingErrorSummary(errors.New("boom: quoted source")); got != "the document is not valid Ktav" {
		t.Errorf("a foreign error gives %q", got)
	}
	wrapped := fmt.Errorf("load: %w", &ktav.Error{Class: "syntax", Reason: "unclosed table", Line: 3, Msg: "secret text"})
	got := bindingErrorSummary(wrapped)
	if got != "Ktav parse error: syntax (unclosed table) at line 3" {
		t.Errorf("summary = %q", got)
	}
	if got := bindingErrorSummary(&ktav.Error{Msg: "secret text"}); got != "Ktav parse error" {
		t.Errorf("a bare error gives %q", got)
	}
}
