package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestSaveAccountConfigDirectHintOnlyForDirectRule(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct", ExitCountry: "DE"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "accounts[0].exit requires an empty exit_country") {
		t.Fatalf("fixture validation error = %v", err)
	}
	err := saveAccountConfig(home, cfg)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Hint == "" {
		t.Fatalf("direct validation error = %#v; want hinted cli.Error", err)
	}
	cfg = config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: "bad", Exit: "direct", ExitCountry: "DE"}}
	err = saveAccountConfig(home, cfg)
	// The ordinary validation hint stays; only the direct-exit repair hint is absent.
	if !errors.As(err, &ce) || strings.Contains(ce.Hint, "set-exit") || ce.Message != "accounts[0].password must be init or an argon2id PHC hash" {
		t.Fatalf("unrelated validation error = %#v; want the original password error without the set-exit hint", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "config.ktav")); !os.IsNotExist(statErr) {
		t.Fatalf("validation failure wrote config: stat error = %v", statErr)
	}
}

func TestAccountWritingDispatchUsesValidatedSaveBoundary(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "account.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "runAccount" {
			run = fn
			break
		}
	}
	if run == nil {
		t.Fatal("runAccount missing")
	}
	writers := map[string]bool{"add": false, "clone": false, "set-exit": false, "set-country": false, "set-provider": false, "set-clients": false, "set-socks5": false, "rm": false}
	for _, stmt := range run.Body.List {
		sw, ok := stmt.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			continue
		}
		if ident, ok := sw.Tag.(*ast.IndexExpr); !ok || ident.Index == nil {
			continue
		}
		for _, raw := range sw.Body.List {
			cc, ok := raw.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok {
					continue
				}
				command := strings.Trim(lit.Value, "\\\"")
				if _, writer := writers[command]; !writer {
					continue
				}
				writers[command] = caseUsesSave(cc)
			}
		}
	}
	for command, found := range writers {
		if !found {
			t.Errorf("writer case %q missing or does not use save closure", command)
		}
	}
	var closureValidated bool
	ast.Inspect(run.Body, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.Ident); ok && sel.Name == "saveAccountConfig" {
				closureValidated = true
			}
			return true
		})
		return false
	})
	if !closureValidated {
		t.Fatal("runAccount save closure does not call saveAccountConfig")
	}
}

func caseUsesSave(cc *ast.CaseClause) bool {
	used := false
	ast.Inspect(cc, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "save" {
			used = true
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && id.Name == "save" {
				used = true
			}
		}
		return true
	})
	return used
}

func TestAccountProductionHasNoSaveOutsideBoundary(t *testing.T) {
	files, err := filepath.Glob("account*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Save" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "config" && filepath.Base(path) != "account_exit.go" {
				t.Errorf("config.Save outside validated boundary in %s", path)
			}
			return true
		})
	}
}

func TestAccountSettersUseFullSaveValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*config.Config, func() error) error
	}{
		{"set-exit", func(c *config.Config, save func() error) error {
			return accountSetExit(t.TempDir(), c, []string{"alice", "route"}, save, &bytes.Buffer{})
		}},
		{"set-country", func(c *config.Config, save func() error) error {
			return accountSet("country", t.TempDir(), c, []string{"alice", "--none"}, save, &bytes.Buffer{})
		}},
		{"set-provider", func(c *config.Config, save func() error) error {
			return accountSet("provider", t.TempDir(), c, []string{"alice", "--none"}, save, &bytes.Buffer{})
		}},
		{"set-clients", func(c *config.Config, save func() error) error {
			return accountSet("clients", t.TempDir(), c, []string{"alice", "127.0.0.1"}, save, &bytes.Buffer{})
		}},
		{"set-socks5", func(c *config.Config, save func() error) error {
			return accountSetSOCKS5(c, []string{"alice", "--post", "none"}, save, &bytes.Buffer{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := config.Default()
			cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder}}
			if err := config.Save(config.ConfigPath(home), cfg); err != nil {
				t.Fatal(err)
			}
			stored, err := config.Load(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			stored.Listen.Port = 0
			called := false
			save := func() error { called = true; return saveAccountConfig(home, stored) }
			err = tc.run(stored, save)
			var ce *cli.Error
			if tc.name != "set-exit" && !called {
				t.Fatal("setter did not call save callback")
			}
			if !errors.As(err, &ce) || ce.Code != config.CodeConfigInvalid || !strings.Contains(ce.Message, "listen.port") {
				t.Fatalf("setter err=%v called=%v", err, called)
			}
			after, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected setter wrote config")
			}
		})
	}
}

func TestRunAccountRejectsDirectAndRemoteInitWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(string) error
		args []string
	}{
		{"reserved init direct", func(home string) error { return config.Save(config.ConfigPath(home), config.Default()) }, []string{"add", "init", "--exit", "direct"}},
		{"remote add init", func(home string) error {
			c := config.Default()
			c.Auth.Login = "legacy"
			c.Auth.Password = "$argon2id$valid-enough-for-validator"
			c.Listen.Bind = "0.0.0.0"
			if err := c.Validate(); err != nil {
				return err
			}
			if err := config.Save(config.ConfigPath(home), c); err != nil {
				return err
			}
			_, err := config.Load(config.ConfigPath(home))
			return err
		}, []string{"add", "alice"}},
		{"remote clone init", func(home string) error {
			c := config.Default()
			c.Auth.Login = "legacy"
			c.Auth.Password = "$argon2id$valid-enough-for-validator"
			c.Listen.Bind = "0.0.0.0"
			c.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$valid-enough-for-validator"}}
			if err := c.Validate(); err != nil {
				return err
			}
			if err := config.Save(config.ConfigPath(home), c); err != nil {
				return err
			}
			_, err := config.Load(config.ConfigPath(home))
			return err
		}, []string{"clone", "alice", "bob", "--password", "init"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := tc.seed(home); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = runAccount(append(tc.args, "--home", home, "--json"), &out)
			var ce *cli.Error
			if !errors.As(err, &ce) || ce.Code != config.CodeConfigInvalid {
				t.Fatalf("err=%v output=%s", err, out.String())
			}
			after, err := os.ReadFile(config.ConfigPath(home))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected command changed config")
			}
		})
	}
}

func TestRunAccountUnchangedSettersSucceedWithoutSnapshots(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: "$argon2id$valid-enough-for-validator"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"set-country", "alice", "--none"},
		{"set-provider", "alice", "--none"},
		{"set-clients", "alice", "--none"},
		{"set-socks5", "alice", "--post", "none"},
		{"set-socks5", "alice", "--post", "none"},
		{"set-exit", "alice", "route"},
		{"set-exit", "alice", "route"},
	}
	for _, args := range commands {
		var out bytes.Buffer
		if err := runAccount(append(append([]string(nil), args...), "--home", home, "--json"), &out); err != nil {
			t.Fatalf("account %v: %v output=%s", args, err, out.String())
		}
	}
	got, err := config.Load(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Login != "alice" {
		t.Fatalf("accounts after setters = %+v", got.Accounts)
	}
}

func TestRunAccountDefaultAddInitRejectedWithFullValidation(t *testing.T) {
	home := t.TempDir()
	if err := config.Save(config.ConfigPath(home), config.Default()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}

	candidate := config.Default()
	candidate.Accounts = []config.Account{{Login: "init", Password: config.InitPlaceholder}}
	validateErr := candidate.Validate()
	var expected *cli.Error
	if !errors.As(validateErr, &expected) {
		t.Fatalf("candidate validation error=%v; want cli.Error", validateErr)
	}

	var out bytes.Buffer
	err = runAccount([]string{"add", "init", "--home", home, "--json"}, &out)
	var actual *cli.Error
	if !errors.As(err, &actual) {
		t.Fatalf("reserved init add error=%v output=%s; want cli.Error", err, out.String())
	}
	if actual.Code != expected.Code || actual.Exit != expected.Exit || actual.Message != expected.Message {
		t.Fatalf("reserved init add error=(Code %q, Exit %d, Message %q); want validation error=(Code %q, Exit %d, Message %q)", actual.Code, actual.Exit, actual.Message, expected.Code, expected.Exit, expected.Message)
	}
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected default add changed config")
	}
}

func TestAccountDirectRejectionDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Login: "alice", Password: config.InitPlaceholder, Exit: "direct", ExitCountry: "DE"}}
	if err := config.Save(config.ConfigPath(home), config.Default()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveAccountConfig(home, cfg); err == nil {
		t.Fatal("saveAccountConfig accepted invalid direct account")
	}
	after, err := os.ReadFile(config.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("validation failure changed config on disk")
	}
}
