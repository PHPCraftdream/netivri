package core

import (
	"runtime"
	"time"
)

// finalizerDrainTimeout bounds drainFinalizers so a stuck finalizer queue
// can never block Start.
const finalizerDrainTimeout = 2 * time.Second

// finalizerDrainRounds is how many dependent finalizers deep drainFinalizers
// follows a chain: a proxy group holds its member adapters, each with a
// finalizer of its own, and Go runs the finalizer of the holder first and
// only queues the member's after the holder was freed by a later collection.
const finalizerDrainRounds = 4

// drainFinalizers returns once the finalizers of every object that is
// already unreachable have run, including those of objects that only become
// unreachable once the finalizer of the object holding them has run.
// Mihomo's proxy adapters log through its process-global logger from a
// finalizer, at an arbitrary time, while executor.ApplyConfig rewrites the
// logger level without any synchronisation: applying a second configuration
// in one process (a second Start, or a test suite) could therefore race with
// the finalizers of the adapters the previous configuration left behind.
// Draining them first orders them before the write. The whole drain shares
// one finalizerDrainTimeout.
//
// Finalizers run one by one on a single goroutine in the order they were
// queued, so a sentinel queued after a collection runs after everything that
// collection queued.
func drainFinalizers() {
	deadline := time.Now().Add(finalizerDrainTimeout)
	for i := 0; i < finalizerDrainRounds; i++ {
		if !drainFinalizersOnce(deadline) {
			return
		}
	}
}

// drainFinalizersOnce runs one collection-and-sentinel round; false when the
// deadline passed before the sentinel ran.
func drainFinalizersOnce(deadline time.Time) bool {
	runtime.GC()
	done := make(chan struct{})
	// Large enough to bypass the tiny allocator, which may never finalize.
	sentinel := new([128]byte)
	runtime.SetFinalizer(sentinel, func(*[128]byte) { close(done) })
	sentinel = nil
	runtime.GC()
	select {
	case <-done:
		return true
	case <-time.After(time.Until(deadline)):
		return false
	}
}
