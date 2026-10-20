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
