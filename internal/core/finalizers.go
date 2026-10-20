package core

import (
	"runtime"
	"time"
)

// finalizerDrainTimeout bounds drainFinalizers so a stuck finalizer queue
// can never block Start.
const finalizerDrainTimeout = 2 * time.Second

// drainFinalizers returns once the finalizers of every object that is
// already unreachable have run. Mihomo's proxy adapters log through its
// process-global logger from a finalizer, at an arbitrary time, while
// executor.ApplyConfig rewrites the logger level without any
// synchronisation: applying a second configuration in one process (a second
// Start, or a test suite) could therefore race with the finalizers of the
// adapters the previous configuration left behind. Draining them first
// orders them before the write.
//
// Finalizers run one by one on a single goroutine in the order they were
// queued, so a sentinel queued after the first collection runs after
// everything that collection queued.
func drainFinalizers() {
	runtime.GC()
	done := make(chan struct{})
	// Large enough to bypass the tiny allocator, which may never finalize.
	sentinel := new([128]byte)
	runtime.SetFinalizer(sentinel, func(*[128]byte) { close(done) })
	sentinel = nil
	runtime.GC()
	select {
	case <-done:
	case <-time.After(finalizerDrainTimeout):
	}
}
