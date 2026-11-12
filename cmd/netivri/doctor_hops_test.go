package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/hopstate"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func doctorHopsHome(t *testing.T) string {
	t.Helper()
	doctorFixTime(t)
	doctorStubListen(t, nil)
	home := t.TempDir()
	doctorConfig(t, home, nil)
	if err := os.MkdirAll(angel.RunDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func doctorHopsAngel(t *testing.T, accounts []angel.AccountInfo) {
	t.Helper()
	doctorStubAngel(t, func(context.Context, string) (angel.State, angel.Status, error) {
		return angel.State{}, angel.Status{PID: 42, Listen: "127.0.0.1:7890", AllowedClients: "*", Accounts: accounts}, nil
	})
}

func doctorHasCheck(doc doctorDoc, name string) bool {
	for _, c := range doc.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDoctorHopsSilentWithoutPoolsOrFiles(t *testing.T) {
	home := doctorHopsHome(t)
	doctorHopsAngel(t, []angel.AccountInfo{{Login: "alice"}})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hop state", "hop pools", "hop persistence"} {
		if doctorHasCheck(doc, name) {
			t.Errorf("unexpected check %q in %v", name, doc.Checks)
		}
	}
}

func TestDoctorHopStateFilesReadableAndUnreadable(t *testing.T) {
	home := doctorHopsHome(t)
	doctorAngelDown(t)
	key := strings.Repeat("ab", 32)
	store := hopstate.NewStore(hopHealthPath(home, "pre"))
	if err := store.Save([]hoppool.Entry{{Key: key, Trips: 2, NextProbe: time.Date(2026, 10, 25, 12, 5, 0, 0, time.UTC)}}); err != nil {
		t.Fatal(err)
	}
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatal(err)
	}
	c := doctorFind(t, doc, "hop state")
	if c.Status != doctorOK || c.Detail != "hops-pre.health readable, 1 dead hop(s) stored" {
		t.Fatalf("readable file: %+v", c)
	}

	if err := os.WriteFile(hopHealthPath(home, "post"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err = doctorRunDoc(t, home)
	if err != nil {
		t.Fatal(err)
	}
	c = doctorFind(t, doc, "hop state")
	if c.Status != doctorWarn || !strings.Contains(c.Detail, "hops-pre.health readable, 1 dead hop(s) stored") ||
		!strings.Contains(c.Detail, "hops-post.health is unreadable") {
		t.Fatalf("unreadable file: %+v", c)
	}
	if strings.Contains(c.Detail, key) {
		t.Fatalf("detail leaks a hop key: %q", c.Detail)
	}
}

func TestDoctorHopPoolsWarnWhenEveryHopIsDown(t *testing.T) {
	home := doctorHopsHome(t)
	allDown := angel.HopPoolInfo{Role: "pre", HopCount: 3, QuarantinedCount: 3, UnpersistableCount: 1, Hops: []angel.HopInfo{
		{ID: "11111111", State: "quarantined", Trips: 1}, {ID: "22222222", State: "probing", Trips: 2}, {State: "quarantined", Trips: 1}}}
	partial := angel.HopPoolInfo{Role: "post", HopCount: 2, ActiveCount: 1, QuarantinedCount: 1, Hops: []angel.HopInfo{
		{ID: "33333333", State: "active"}, {ID: "44444444", State: "quarantined", Trips: 1}}}
	doctorHopsAngel(t, []angel.AccountInfo{
		{Login: "alice", HopPools: []angel.HopPoolInfo{allDown}},
		{Login: "bob", HopPools: []angel.HopPoolInfo{partial}},
		{Login: "eve", HopPools: []angel.HopPoolInfo{allDown}},
	})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatalf("a pool warning must not fail doctor: %v", err)
	}
	pools := doctorFind(t, doc, "hop pools")
	if pools.Status != doctorWarn || pools.Detail != "hop pre pool (alice, eve): all 3 hops are down, trying them anyway" {
		t.Fatalf("hop pools = %+v", pools)
	}
	persist := doctorFind(t, doc, "hop persistence")
	if persist.Status != doctorOK || !strings.HasPrefix(persist.Detail, "info: 1 hop(s) cannot be stored") {
		t.Fatalf("hop persistence = %+v", persist)
	}
	if doctorHasCheck(doc, "hop state") {
		t.Fatalf("no state file exists but a hop state check was reported: %v", doc.Checks)
	}
	if doc.Summary.Warn != 1 {
		t.Fatalf("summary = %+v", doc.Summary)
	}
}

func TestDoctorHopPoolsOKWhenHopsAreUp(t *testing.T) {
	home := doctorHopsHome(t)
	pool := angel.HopPoolInfo{Role: "post", HopCount: 2, ActiveCount: 1, QuarantinedCount: 1, Hops: []angel.HopInfo{
		{ID: "33333333", State: "active"}, {ID: "44444444", State: "probing", Trips: 1}}}
	doctorHopsAngel(t, []angel.AccountInfo{{Login: "alice", HopPools: []angel.HopPoolInfo{pool}}})
	doc, err := doctorRunDoc(t, home)
	if err != nil {
		t.Fatal(err)
	}
	pools := doctorFind(t, doc, "hop pools")
	if pools.Status != doctorOK || pools.Detail != "1 pool(s), 1 of 2 hops up" {
		t.Fatalf("hop pools = %+v", pools)
	}
	if doctorHasCheck(doc, "hop persistence") {
		t.Fatalf("persistence info without unpersistable hops: %v", doc.Checks)
	}
}

func TestHopPoolViewHelpers(t *testing.T) {
	p := angel.HopPoolInfo{Role: "post", HopCount: 3, ActiveCount: 2, QuarantinedCount: 1}
	if got := hopPoolLine(p); got != "post 3 hops: 2 up, 1 down" {
		t.Fatalf("line = %q", got)
	}
	if got := hopPoolLine(angel.HopPoolInfo{Role: "pre", HopCount: 2, ActiveCount: 2}); got != "pre 2 hops: 2 up" {
		t.Fatalf("line = %q", got)
	}
	if hopPoolAllDown(p) || !hopPoolAllDown(angel.HopPoolInfo{HopCount: 2, QuarantinedCount: 2}) || hopPoolAllDown(angel.HopPoolInfo{}) {
		t.Fatal("hopPoolAllDown")
	}
}
