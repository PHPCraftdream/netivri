//go:build e2e

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

type e2eDoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type e2eDoctorDoc struct {
	Checks  []e2eDoctorCheck `json:"checks"`
	Summary struct {
		Ok   int `json:"ok"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"summary"`
}

type e2eDoctorEnvelope struct {
	OK   bool         `json:"ok"`
	Data e2eDoctorDoc `json:"data"`
}

func e2eDoctorRun(t *testing.T, home string) (int, e2eDoctorDoc, string) {
	t.Helper()
	code, out, stderr := accountsRun(t, home, "doctor")
	var env e2eDoctorEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode doctor output %q (stderr %s): %v", out, stderr, err)
	}
	if !env.OK {
		t.Fatalf("doctor envelope not ok (exit %d, stderr %s)", code, stderr)
	}
	return code, env.Data, stderr
}

func e2eDoctorFind(t *testing.T, doc e2eDoctorDoc, name string) e2eDoctorCheck {
	t.Helper()
	for _, c := range doc.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("doctor has no check %q in %v", name, doc.Checks)
	return e2eDoctorCheck{}
}

// TestE2EDoctor runs `doctor` against a real angel, after `down`, and in a
// home without a configuration. Only deterministic facts are asserted: warm
// probe timing is not part of the contract, so the account check is only
// required to be present with a detail and a decidable status.
func TestE2EDoctor(t *testing.T) {
	h := newAngelHome(t)
	home := h.dir
	hash, err := auth.Hash(accountsDEPass)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, func(cfg *config.Config) {
		cfg.Listen.Port = freePort(t) // the probe port, released again at once
		cfg.Accounts = []config.Account{{Login: "alice", Password: hash, Provider: "p", ExitCountry: "DE"}}
		cfg.Subscriptions = []config.Subscription{{Name: "p", URL: "https://p.invalid/sub"}}
	})
	accountsSaveSnapshot(t, home, "p", []subscription.Node{accountsSocksNode("de-p", "DE", accountsServer(t))})

	// With the angel running: config, angel and listen are ok, and the
	// account check exists with a non-empty detail.
	code, stdout, stderr := angelRun(t, home, "up", "--port", "0")
	if code != 0 {
		t.Fatalf("up exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	h.pid = int(angelData(t, stdout)["pid"].(float64))
	code, doc, dstderr := e2eDoctorRun(t, home)
	// A completed warm probe that found no live node makes the account check
	// fail, which is legitimate here; whether the probe completed before this
	// call is timing. So both outcomes are accepted, but the exit code and the
	// stderr envelope must agree with the document.
	var nOK, nWarn, nFail int
	for _, c := range doc.Checks {
		switch c.Status {
		case "ok":
			nOK++
		case "warn":
			nWarn++
		case "fail":
			nFail++
			if c.Name != "account alice" {
				t.Errorf("only the account check may fail here, got %+v", c)
			}
		default:
			t.Errorf("check %q has status %q", c.Name, c.Status)
		}
	}
	if s := doc.Summary; s.Ok != nOK || s.Warn != nWarn || s.Fail != nFail {
		t.Errorf("summary %+v does not match the checks (ok %d, warn %d, fail %d)", s, nOK, nWarn, nFail)
	}
	if nFail == 0 {
		if code != 0 {
			t.Fatalf("doctor exit = %d with no failed check (stderr %s)", code, dstderr)
		}
	} else if code != cli.ExitCheckFailed || !strings.Contains(dstderr, "DOCTOR_FAILED") {
		t.Fatalf("doctor with a failed check: exit %d, stderr %s; want exit %d and DOCTOR_FAILED", code, dstderr, cli.ExitCheckFailed)
	}
	if c := e2eDoctorFind(t, doc, "config"); c.Status != "ok" {
		t.Errorf("config = %+v", c)
	}
	if c := e2eDoctorFind(t, doc, "angel"); c.Status != "ok" || !strings.Contains(c.Detail, "running, pid ") {
		t.Errorf("angel = %+v", c)
	}
	if c := e2eDoctorFind(t, doc, "listen"); c.Status != "ok" || !strings.Contains(c.Detail, "listen 127.0.0.1:") {
		t.Errorf("listen = %+v", c)
	}
	acc := e2eDoctorFind(t, doc, "account alice")
	switch acc.Status {
	case "ok", "warn", "fail":
	default:
		t.Fatalf("account status = %q, want ok, warn or fail", acc.Status)
	}
	if acc.Detail == "" {
		t.Error("account check has no detail")
	}

	// After `down`: the angel check warns, the account still counts its
	// stored nodes, and the command succeeds (nothing failed).
	code, stdout, stderr = angelRun(t, home, "down")
	if code != 0 {
		t.Fatalf("down exit = %d (stdout %q, stderr %q)", code, stdout, stderr)
	}
	dcode, ddoc, dstderr := e2eDoctorRun(t, home)
	if dcode != 0 {
		t.Fatalf("doctor after down: exit %d (stderr %s)", dcode, dstderr)
	}
	if c := e2eDoctorFind(t, ddoc, "angel"); c.Status != "warn" || c.Detail != "angel is not running" {
		t.Errorf("angel = %+v", c)
	}
	if c := e2eDoctorFind(t, ddoc, "account alice"); c.Status != "warn" || !strings.Contains(c.Detail, "1 stored node(s)") {
		t.Errorf("account = %+v", c)
	}
	if ddoc.Summary.Fail != 0 {
		t.Errorf("summary = %+v", ddoc.Summary)
	}

	// A home without a configuration: the defaults apply, exit 0.
	empty := t.TempDir()
	ecode, edoc, estderr := e2eDoctorRun(t, empty)
	if ecode != 0 {
		t.Fatalf("doctor without config: exit %d (stderr %s)", ecode, estderr)
	}
	if c := e2eDoctorFind(t, edoc, "config"); c.Status != "ok" {
		t.Errorf("config = %+v", c)
	}
	if c := e2eDoctorFind(t, edoc, "angel"); c.Status != "warn" {
		t.Errorf("angel = %+v", c)
	}
}
