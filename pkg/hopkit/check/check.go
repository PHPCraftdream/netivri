// Package check probes hop reachability without interpreting native protocols.
package check

import (
	"context"
	"errors"

	"github.com/PHPCraftdream/netivri/pkg/hopkit/dial"
	pool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

// Prober runs a single hop-level health check.
type Prober interface {
	Probe(context.Context, dial.Hop) pool.ProbeOutcome
}

type prober struct {
	network string
	address string
}

// New returns a network prober which dials and closes the configured address.
func New(network, address string) Prober {
	return prober{network: network, address: address}
}

func (p prober) Probe(ctx context.Context, hop dial.Hop) pool.ProbeOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || hop == nil {
		return pool.ProbeInconclusive
	}
	conn, err := hop.DialContext(ctx, p.network, p.address)
	if ctx.Err() != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return pool.ProbeInconclusive
	}
	if err == nil {
		if conn == nil {
			return pool.ProbeInconclusive
		}
		_ = conn.Close()
		return pool.ProbeSuccess
	}
	if err != nil && conn != nil {
		_ = conn.Close()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return pool.ProbeInconclusive
	}
	var staged *dial.StageError
	if errors.As(err, &staged) && staged != nil &&
		(staged.Stage == dial.Connect || staged.Stage == dial.Refusal ||
			staged.Stage == dial.Handshake && !staged.NextUnreachable) {
		return pool.ProbeHardFailure
	}
	return pool.ProbeInconclusive
}
