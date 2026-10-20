package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// singleflightBurst verifies the credential from n goroutines released
// together while the counting verifyHash seam holds the first argon2 call,
// and checks that no second argon2 run starts and every caller is
// accepted. The negative wait can only miss a duplicate run (a pass),
// never report a false failure.
func singleflightBurst(t *testing.T, verify func(user, pass string) bool) {
	t.Helper()
	const n = 8
	orig := verifyHash
	var invocations atomic.Int32
	entered := make(chan struct{}, n)
	release := make(chan struct{})
	verifyHash = func(hash, pass string) (bool, error) {
		invocations.Add(1)
		entered <- struct{}{}
		<-release
		return VerifyHash(hash, pass)
	}
	t.Cleanup(func() { verifyHash = orig })

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = verify("user", "hunter2")
		}(i)
	}
	close(start)

	abort := func(format string, args ...any) {
		close(release)
		wg.Wait()
		t.Fatalf(format, args...)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		abort("VerifyHash was never called")
	}
	// While the first run is held, every other caller waits for it.
	select {
	case <-entered:
		abort("a second argon2 run started while the first was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if got := invocations.Load(); got != 1 {
		t.Fatalf("VerifyHash called %d times, want 1", got)
	}
	for i, ok := range results {
		if !ok {
			t.Fatalf("goroutine %d rejected the correct password", i)
		}
	}
}

func TestConcurrentFirstVerifyLegacyRunsArgon2Once(t *testing.T) {
	setFastCost(t)
	a := mustNew(t, Options{Login: "user", Password: validHashFor(t, "hunter2")})
	singleflightBurst(t, a.Verify)
	a.mu.Lock()
	cached := a.cachedPass
	a.mu.Unlock()
	if cached != "hunter2" {
		t.Fatalf("cached password = %q, want hunter2", cached)
	}
	// The cache answers afterwards without touching the seam.
	verifyHash = func(string, string) (bool, error) {
		return false, nil
	}
	if !a.Verify("user", "hunter2") {
		t.Fatal("cache fast path broken after the burst")
	}
}

func TestConcurrentFirstVerifyMultiRunsArgon2Once(t *testing.T) {
	setFastCost(t)
	m, err := NewMulti(MultiOptions{Accounts: []AccountSpec{{Login: "user", Password: validHashFor(t, "hunter2")}}})
	if err != nil {
		t.Fatal(err)
	}
	singleflightBurst(t, m.Verify)
	state := m.(*multiAuthenticator).accounts["user"]
	state.mu.Lock()
	cached := state.cached
	state.mu.Unlock()
	if cached != "hunter2" {
		t.Fatalf("cached password = %q, want hunter2", cached)
	}
}

// A flight that starts after the credential was cached must not run argon2:
// the seam fails the test if it is reached.
func TestVerifyAfterCachedFlightSkipsArgon2(t *testing.T) {
	setFastCost(t)
	m, err := NewMulti(MultiOptions{Accounts: []AccountSpec{{Login: "user", Password: validHashFor(t, "hunter2")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Verify("user", "hunter2") {
		t.Fatal("first verification failed")
	}
	orig := verifyHash
	verifyHash = func(string, string) (bool, error) {
		t.Error("argon2 ran for a cached credential")
		return false, nil
	}
	t.Cleanup(func() { verifyHash = orig })
	// Enter the flight directly, as a caller that saw an empty cache would.
	multi := m.(*multiAuthenticator)
	state := multi.accounts["user"]
	v, err, _ := multi.sf.Do("user\x00hunter2", func() (any, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.cached == "hunter2", nil
	})
	if err != nil || v != true {
		t.Fatalf("flight result %v, %v", v, err)
	}
	if !m.Verify("user", "hunter2") || m.Verify("user", "wrong") {
		t.Fatal("cached answers wrong")
	}
}

// Concurrent wrong and right passwords use different singleflight keys:
// neither result may leak into the other path.
func TestConcurrentVerifyWrongAndRightPasswordsIsolated(t *testing.T) {
	setFastCost(t)
	a := mustNew(t, Options{Login: "user", Password: validHashFor(t, "hunter2")})
	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wrong := make([]bool, n)
	right := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			wrong[i] = a.Verify("user", "wrong")
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			right[i] = a.Verify("user", "hunter2")
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		if wrong[i] {
			t.Fatalf("wrong password accepted (goroutine %d)", i)
		}
		if !right[i] {
			t.Fatalf("correct password rejected (goroutine %d)", i)
		}
	}
	a.mu.Lock()
	cached := a.cachedPass
	a.mu.Unlock()
	if cached != "hunter2" {
		t.Fatalf("wrong password leaked into the cache: %q", cached)
	}
	if a.Verify("user", "wrong") {
		t.Fatal("wrong password accepted after the burst")
	}
	if !a.Verify("user", "hunter2") {
		t.Fatal("correct password rejected after the burst")
	}
}
