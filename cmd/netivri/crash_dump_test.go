package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
)

func TestArmCrashDumpReportsEarlierCrashAndArmsOwn(t *testing.T) {
	home := t.TempDir()
	dir := angel.CrashDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	crashAt := time.Date(2026, 10, 29, 11, 7, 0, 0, time.UTC)
	crashed := filepath.Join(dir, "crash-20261029T110653Z-20488.pending")
	body := "netivri angel crash dump\nstarted: 2026-10-29T11:06:53Z\npid: 20488\nversion: 0.9.0\n\n" +
		"panic: buffer overflow: capacity 32768,start 0, need 2\n\ngoroutine 841 [running]:\n"
	if err := os.WriteFile(crashed, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(crashed, crashAt, crashAt); err != nil {
		t.Fatal(err)
	}
	killed := filepath.Join(dir, "crash-20261030T080000Z-777.pending")
	if err := os.WriteFile(killed, []byte("netivri angel crash dump\nstarted: x\npid: 777\nversion: 0.9.1\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var infos, errs, warns []string
	rec := func(dst *[]string) func(string, ...any) {
		return func(format string, args ...any) { *dst = append(*dst, fmt.Sprintf(format, args...)) }
	}
	disarm := armCrashDump(home, rec(&infos), rec(&errs), rec(&warns))

	if len(errs) != 1 || !strings.Contains(errs[0], "pid 20488, version 0.9.0") ||
		!strings.Contains(errs[0], "crashed at 2026-10-29T11:07:00Z") ||
		!strings.Contains(errs[0], "run/crash/crash-20261029T110653Z-20488.txt") {
		t.Fatalf("crash report = %q", errs)
	}
	if strings.Contains(errs[0], home) {
		t.Fatalf("the report names the state directory: %q", errs[0])
	}
	if len(infos) != 1 || !strings.Contains(infos[0], "pid 777") || !strings.Contains(infos[0], "did not stop cleanly") {
		t.Fatalf("unclean report = %q", infos)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings = %q", warns)
	}
	if _, err := os.Stat(filepath.Join(dir, "crash-20261029T110653Z-20488.txt")); err != nil {
		t.Fatalf("the dump was not kept: %v", err)
	}

	own := pendingDumps(t, dir)
	if len(own) != 1 || !strings.HasSuffix(own[0], fmt.Sprintf("-%d.pending", os.Getpid())) {
		t.Fatalf("own dump = %v", own)
	}
	disarm()
	if left := pendingDumps(t, dir); len(left) != 0 {
		t.Fatalf("a clean stop left %v", left)
	}
}

func TestArmCrashDumpUnwritableDirectoryWarns(t *testing.T) {
	home := t.TempDir()
	// run/crash is a file: the directory cannot be made.
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(angel.CrashDir(home), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warns []string
	noop := func(string, ...any) {}
	disarm := armCrashDump(home, noop, noop, func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) })
	disarm()
	if len(warns) != 1 || !strings.Contains(warns[0], "the crash dump is off") {
		t.Fatalf("warnings = %q", warns)
	}
}

func pendingDumps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pending") {
			out = append(out, e.Name())
		}
	}
	return out
}
