package core

import (
	"runtime"
	"sync/atomic"
	"testing"
)

// An unreachable object's finalizer must have run when drainFinalizers
// returns.
func TestDrainFinalizersRunsPendingFinalizers(t *testing.T) {
	var ran atomic.Int32
	func() {
		obj := new([128]byte)
		runtime.SetFinalizer(obj, func(*[128]byte) { ran.Add(1) })
	}() // obj is unreachable once this returns

	drainFinalizers()
	if got := ran.Load(); got != 1 {
		t.Fatalf("finalizer ran %d times after drainFinalizers, want 1", got)
	}
}

// chainHead holds another finalizable object, like a proxy group holds its
// member adapters.
type chainHead struct {
	member *[128]byte
	pad    [128]byte
}

// The finalizer of an object that is only reachable from another finalizable
// object runs a collection later: drainFinalizers must follow the chain.
func TestDrainFinalizersFollowsChains(t *testing.T) {
	var ran atomic.Int32
	func() {
		member := new([128]byte)
		runtime.SetFinalizer(member, func(*[128]byte) { ran.Add(1) })
		head := &chainHead{member: member}
		runtime.SetFinalizer(head, func(*chainHead) { ran.Add(1) })
	}()

	drainFinalizers()
	if got := ran.Load(); got != 2 {
		t.Fatalf("%d of 2 chained finalizers ran after drainFinalizers", got)
	}
}
