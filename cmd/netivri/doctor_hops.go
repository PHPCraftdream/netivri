package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/hopstate"
)

// checkHops reports the hop pools. Nothing is added when no dead-hop file
// exists and the angel reports no pool, so setups without pools keep their
// document unchanged. Hops appear only by role and short opaque id.
func (d *doctorContext) checkHops() []doctorCheck {
	var checks []doctorCheck
	if c, ok := d.checkHopFiles(); ok {
		checks = append(checks, c)
	}
	return append(checks, d.checkHopPools()...)
}

// checkHopFiles reads hops-pre.health and hops-post.health when they exist.
func (d *doctorContext) checkHopFiles() (doctorCheck, bool) {
	var parts []string
	status := doctorOK
	for _, role := range []string{"pre", "post"} {
		path := hopHealthPath(d.home, role)
		name := filepath.Base(path)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			parts = append(parts, name+" is unreadable")
			status = doctorWarn
			continue
		}
		entries, err := hopstate.NewStore(path).Load()
		if err != nil {
			parts = append(parts, name+" is unreadable")
			status = doctorWarn
			continue
		}
		parts = append(parts, fmt.Sprintf("%s readable, %d dead hop(s) stored", name, len(entries)))
	}
	if len(parts) == 0 {
		return doctorCheck{}, false
	}
	detail := strings.Join(parts, "; ")
	if status == doctorWarn {
		detail += "; the angel starts without the stored hop state and rewrites the file"
	}
	return doctorCheck{"hop state", status, detail}, true
}

type doctorHopPool struct {
	info   angel.HopPoolInfo
	logins []string
}

// checkHopPools reads the pools of the running angel: a pool with every hop
// quarantined is a warning, hops the state files cannot hold are info.
func (d *doctorContext) checkHopPools() []doctorCheck {
	if !d.angelUp {
		return nil
	}
	var order []string
	pools := make(map[string]*doctorHopPool)
	// The default route's pools are keyed apart: they never merge with an
	// account pool of the same shape.
	for _, info := range d.status.HopPools {
		sig := "default|" + hopPoolSignature(info)
		if pools[sig] == nil {
			pools[sig] = &doctorHopPool{info: info, logins: []string{defaultRouteLabel}}
			order = append(order, sig)
		}
	}
	for _, account := range d.status.Accounts {
		for _, info := range account.HopPools {
			sig := hopPoolSignature(info)
			if pools[sig] == nil {
				pools[sig] = &doctorHopPool{info: info}
				order = append(order, sig)
			}
			pools[sig].logins = append(pools[sig].logins, account.Login)
		}
	}
	if len(order) == 0 {
		return nil
	}
	var down []string
	hops, up, unsaved := 0, 0, 0
	for _, sig := range order {
		p := pools[sig]
		hops += p.info.HopCount
		up += p.info.ActiveCount
		unsaved += p.info.UnpersistableCount
		if hopPoolAllDown(p.info) {
			down = append(down, fmt.Sprintf("hop %s pool (%s): all %d hops are down, trying them anyway",
				p.info.Role, strings.Join(p.logins, ", "), p.info.HopCount))
		}
	}
	check := doctorCheck{"hop pools", doctorOK, fmt.Sprintf("%d pool(s), %d of %d hops up", len(order), up, hops)}
	if len(down) > 0 {
		check = doctorCheck{"hop pools", doctorWarn, strings.Join(down, "; ")}
	}
	checks := []doctorCheck{check}
	if unsaved > 0 {
		checks = append(checks, doctorCheck{"hop persistence", doctorOK,
			fmt.Sprintf("info: %d hop(s) cannot be stored in the hop state files; their quarantine is forgotten when the angel restarts", unsaved)})
	}
	return checks
}
