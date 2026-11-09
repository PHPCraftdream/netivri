package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/auth"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
)

func TestDoctorCoreDownFailsAngelAndListen(t *testing.T) {
	doctorFixTime(t)
	doctorStubListen(t, nil)
	const secretURL = "https://provider.example/sub/doctor-secret"
	secretHash, err := auth.Hash("doctor-secret-password")
	if err != nil {
		t.Fatal(err)
	}
	doctorStubAngel(t, func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{
			Angel: "running",
			PID:   42,
			Core:  "down",
			Reload: &angel.ReloadInfo{
				LastError: "PORT_IN_USE: " + secretURL + " " + secretHash,
			},
		}, nil
	})
	home := t.TempDir()
	doctorConfig(t, home, func(cfg *config.Config) {
		cfg.Auth.Login = "legacy"
		cfg.Auth.Password = secretHash
		cfg.Subscriptions = []config.Subscription{{Name: "main", URL: secretURL}}
	})
	cli.UseJSON()
	var out bytes.Buffer
	err = runDoctor([]string{"--home", home}, &out)
	doctorAssertFailed(t, err)

	var env doctorEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal("doctor output was not valid JSON")
	}
	if check := doctorFind(t, env.Data, "angel"); check.Status != doctorFail || !strings.Contains(check.Detail, "core is down") || !strings.Contains(check.Detail, "PORT_IN_USE") {
		t.Error("angel check did not report core down and reload error")
	}
	if check := doctorFind(t, env.Data, "listen"); check.Status != doctorFail || !strings.Contains(check.Detail, "core is down") || strings.Contains(check.Detail, "PORT_IN_USE") || strings.Contains(check.Detail, "listen ,") {
		t.Error("listen check did not report core down")
	}
	text := out.String()
	for _, secret := range []string{secretURL, secretHash, "doctor-secret-password"} {
		if strings.Contains(text, secret) {
			t.Fatal("doctor output leaked a configured secret")
		}
	}
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "DOCTOR_FAILED" || ce.Exit != cli.ExitCheckFailed {
		t.Errorf("doctor error = %v, want DOCTOR_FAILED exit %d", err, cli.ExitCheckFailed)
	}
}

func TestDoctorCoreDownWithoutReloadError(t *testing.T) {
	d := &doctorContext{angelUp: true, status: angel.Status{Core: "down"}}
	for _, check := range []doctorCheck{d.checkAngel(), d.checkListen()} {
		if check.Status != doctorFail || strings.Contains(check.Detail, "<nil>") {
			t.Errorf("check = %+v, want a safe core-down failure", check)
		}
	}
}
