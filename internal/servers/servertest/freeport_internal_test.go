package servertest

import "testing"

// The OS picks the TCP port for the first attempts; later attempts draw from
// the fixed fallback range so a runner with large UDP excluded ranges still
// finds a port that works for both transports.
func TestFreePortCandidate(t *testing.T) {
	for attempt := 0; attempt < freePortOSAttempts; attempt++ {
		if got := freePortCandidate(attempt); got != 0 {
			t.Fatalf("attempt %d: candidate %d, want 0 (let the OS pick)", attempt, got)
		}
	}
	for attempt := freePortOSAttempts; attempt < freePortAttempts; attempt++ {
		if got := freePortCandidate(attempt); got < 20000 || got >= 40000 {
			t.Fatalf("attempt %d: candidate %d outside the fallback range [20000, 40000)", attempt, got)
		}
	}
	if freePortAttempts <= freePortOSAttempts {
		t.Fatal("the search has no fallback attempts")
	}
}
