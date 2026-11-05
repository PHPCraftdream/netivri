package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/core"
)

func TestDiagDialLabelRunsSingleStoredNode(t *testing.T) {
	home := diagHome(t)
	useDiagDial(t, func(ctx context.Context, o core.DiagOptions) ([]core.NodeDiag, error) {
		capturedDiagOptions = o
		if len(o.Nodes) != 1 || o.Nodes[0].Label != "main/a1" {
			t.Fatalf("nodes = %+v, want exactly main/a1", o.Nodes)
		}
		if o.URL != diagProbeURL {
			t.Fatalf("url = %q, want the warm-up default %q", o.URL, diagProbeURL)
		}
		if o.Idle == nil || len(o.Idle) != 0 {
			t.Fatalf("idle = %v, want the empty non-nil slice (idle test skipped)", o.Idle)
		}
		return []core.NodeDiag{{Label: "main/a1", Samples: o.Samples}}, nil
	})
	d, err := DiagDialLabel(context.Background(), home, "main/a1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Samples != 7 {
		t.Fatalf("samples = %d, want 7", d.Samples)
	}
	if capturedDiagOptions.Samples != 7 {
		t.Fatalf("options samples = %d", capturedDiagOptions.Samples)
	}
}

func TestDiagDialLabelValidatesSamples(t *testing.T) {
	home := diagHome(t)
	for _, n := range []int{0, -1, 21, 100} {
		if _, err := DiagDialLabel(context.Background(), home, "main/a1", n); err == nil {
			t.Fatalf("samples %d: want a usage error", n)
		} else {
			assertCode(t, err, "USAGE", cli.ExitUsage)
		}
	}
}

func TestDiagDialLabelUnknownLabelCarriesLabelOnly(t *testing.T) {
	home := diagHome(t)
	useDiagDial(t, func(context.Context, core.DiagOptions) ([]core.NodeDiag, error) {
		t.Fatal("the core run must not start for an unknown label")
		return nil, nil
	})
	_, err := DiagDialLabel(context.Background(), home, "main/zz", 5)
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "NODE_NOT_FOUND" {
		t.Fatalf("err = %v, want NODE_NOT_FOUND", err)
	}
	if ce.Message != "node not found: main/zz" {
		t.Fatalf("message = %q", ce.Message)
	}
	for _, secret := range []string{"srv1", "srv2", "secret", "8388"} {
		if strings.Contains(ce.Message, secret) {
			t.Fatalf("message %q leaks %q", ce.Message, secret)
		}
	}
}

func TestDiagDialLabelContextCancelReachesTheRun(t *testing.T) {
	home := diagHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	useDiagDial(t, func(ctx context.Context, _ core.DiagOptions) ([]core.NodeDiag, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	errCh := make(chan error, 1)
	go func() {
		_, err := DiagDialLabel(ctx, home, "main/a1", 5)
		errCh <- err
	}()
	cancel()
	select {
	case err := <-errCh:
		var ce *cli.Error
		if !errors.As(err, &ce) || ce.Code != "DIAG_FAILED" {
			t.Fatalf("err = %v, want DIAG_FAILED", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled run did not return")
	}
}

func TestDiagDialLabelMapsErrorsLikeTheCommand(t *testing.T) {
	home := diagHome(t)
	// A *cli.Error passes through with its code.
	useDiagDial(t, func(context.Context, core.DiagOptions) ([]core.NodeDiag, error) {
		return nil, &cli.Error{Code: "SUB_SNAPSHOT_INVALID", Message: "snapshot broken", Exit: cli.ExitInternal}
	})
	_, err := DiagDialLabel(context.Background(), home, "main/a1", 5)
	assertCode(t, err, "SUB_SNAPSHOT_INVALID", cli.ExitInternal)
	// A plain error becomes DIAG_FAILED with exit 3.
	useDiagDial(t, func(context.Context, core.DiagOptions) ([]core.NodeDiag, error) {
		return nil, errors.New("unusable node mapping")
	})
	_, err = DiagDialLabel(context.Background(), home, "main/a1", 5)
	assertCode(t, err, "DIAG_FAILED", cli.ExitNetwork)
}
