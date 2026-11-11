package pool

import (
	"sync"
	"testing"
	"time"
)

func TestBinderRoundTripAndMiss(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{Clock: clock.Now})
	if _, ok := b.Get("unknown", "site"); ok {
		t.Fatal("unknown binding unexpectedly found")
	}
	b.Bind("", "site", "anonymous-hop")
	if got, ok := b.Get("", "site"); !ok || got != "anonymous-hop" {
		t.Fatalf("anonymous binding = (%q, %v), want (%q, true)", got, ok, "anonymous-hop")
	}
	if _, ok := b.Get("login", "site"); ok {
		t.Fatal("login unexpectedly matched anonymous binding")
	}
}

func TestBinderSlidingTTLAndExactExpiry(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{TTL: 10 * time.Second, Clock: clock.Now})
	b.Bind("login", "sliding", "hop")

	clock.Set(testEpoch.Add(9 * time.Second))
	if got, ok := b.Get("login", "sliding"); !ok || got != "hop" {
		t.Fatalf("hit just before expiry = (%q, %v), want (%q, true)", got, ok, "hop")
	}
	clock.Set(testEpoch.Add(18 * time.Second))
	if got, ok := b.Get("login", "sliding"); !ok || got != "hop" {
		t.Fatalf("sliding hit = (%q, %v), want (%q, true)", got, ok, "hop")
	}
	clock.Set(testEpoch.Add(28 * time.Second))
	if _, ok := b.Get("login", "sliding"); ok {
		t.Fatal("binding was live at its refreshed expiry boundary")
	}

	b.Bind("login", "unread", "hop")
	clock.Set(testEpoch.Add(38 * time.Second))
	if _, ok := b.Get("login", "unread"); ok {
		t.Fatal("binding without a hit was live at its expiry boundary")
	}
}

func TestBinderReplaceRefreshesTTLAndReverseIndex(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{TTL: 10 * time.Second, Clock: clock.Now})
	b.Bind("login", "replace", "old")
	b.Bind("login", "other", "old")
	clock.Set(testEpoch.Add(9 * time.Second))
	b.Bind("login", "replace", "new")
	clock.Set(testEpoch.Add(18 * time.Second))
	if got, ok := b.Get("login", "replace"); !ok || got != "new" {
		t.Fatalf("replacement binding = (%q, %v), want (%q, true)", got, ok, "new")
	}
	if got := b.Forget("old"); got != 1 {
		t.Fatalf("Forget(old) = %d, want remaining binding count 1", got)
	}
	if _, ok := b.Get("login", "other"); ok {
		t.Fatal("Forget(old) left the other old-hop binding")
	}
	if got, ok := b.Get("login", "replace"); !ok || got != "new" {
		t.Fatalf("replacement after Forget(old) = (%q, %v), want (%q, true)", got, ok, "new")
	}
	clock.Set(testEpoch.Add(28 * time.Second))
	if _, ok := b.Get("login", "replace"); ok {
		t.Fatal("replacement was live at its refreshed expiry boundary")
	}
	if got := b.Forget("old"); got != 0 {
		t.Fatalf("Forget(old) after cleanup = %d, want 0", got)
	}
}

func TestBinderUnbindIsIdempotent(t *testing.T) {
	b := NewBinder(BinderOptions{Clock: newTestClock().Now})
	b.Bind("login", "site", "hop")
	b.Unbind("login", "site")
	b.Unbind("login", "site")
	if _, ok := b.Get("login", "site"); ok {
		t.Fatal("Unbind left a binding")
	}
	if got := b.Forget("hop"); got != 0 {
		t.Fatalf("Forget after Unbind = %d, want 0", got)
	}
}

func TestBinderCompositeKeysAndEmptyInputs(t *testing.T) {
	b := NewBinder(BinderOptions{Clock: newTestClock().Now})
	b.Bind("a", "bc", "first")
	b.Bind("ab", "c", "second")
	if got, ok := b.Get("a", "bc"); !ok || got != "first" {
		t.Fatalf("(a,bc) = (%q, %v), want (%q, true)", got, ok, "first")
	}
	if got, ok := b.Get("ab", "c"); !ok || got != "second" {
		t.Fatalf("(ab,c) = (%q, %v), want (%q, true)", got, ok, "second")
	}
	b.Bind("a", "bc", "")
	b.Bind("a", "", "ignored")
	if _, ok := b.Get("a", ""); ok {
		t.Fatal("empty site unexpectedly found a binding")
	}
	if got, ok := b.Get("a", "bc"); !ok || got != "first" {
		t.Fatalf("empty-key Bind replaced existing binding: (%q, %v)", got, ok)
	}
	if got := b.Len(); got != 2 {
		t.Fatalf("Len after empty inputs = %d, want 2", got)
	}
}

func TestBinderForgetRemovesOnlyMatchingHop(t *testing.T) {
	b := NewBinder(BinderOptions{Clock: newTestClock().Now})
	b.Bind("one", "site", "shared")
	b.Bind("two", "site", "shared")
	b.Bind("three", "site", "other")
	if got := b.Forget("shared"); got != 2 {
		t.Fatalf("Forget(shared) = %d, want 2", got)
	}
	if got := b.Len(); got != 1 {
		t.Fatalf("Len after Forget = %d, want 1", got)
	}
	if got, ok := b.Get("three", "site"); !ok || got != "other" {
		t.Fatalf("unmatched binding = (%q, %v), want (%q, true)", got, ok, "other")
	}
}

func TestBinderCapacityLRUAndNewEntrySurvival(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{MaxEntries: 2, Clock: clock.Now})
	b.Bind("", "a", "a")
	b.Bind("", "b", "b") // Equal timestamps: the most recently inserted is MRU.
	if got, ok := b.Get("", "a"); !ok || got != "a" {
		t.Fatalf("Get(a) = (%q, %v), want (%q, true)", got, ok, "a")
	}
	b.Bind("", "c", "c")
	if _, ok := b.Get("", "b"); ok {
		t.Fatal("capacity eviction did not remove the least-recently-used entry")
	}
	if _, ok := b.Get("", "a"); !ok {
		t.Fatal("Get did not protect the entry from LRU eviction")
	}
	if got, ok := b.Get("", "c"); !ok || got != "c" {
		t.Fatalf("new entry = (%q, %v), want (%q, true)", got, ok, "c")
	}

	tied := NewBinder(BinderOptions{MaxEntries: 2, Clock: clock.Now})
	tied.Bind("", "first", "first")
	tied.Bind("", "second", "second")
	tied.Bind("", "third", "third")
	if _, ok := tied.Get("", "first"); ok {
		t.Fatal("equal-clock LRU tie did not evict the older insertion")
	}
	if _, ok := tied.Get("", "third"); !ok {
		t.Fatal("new entry was evicted on an equal-clock tie")
	}

	replaced := NewBinder(BinderOptions{MaxEntries: 2, Clock: clock.Now})
	replaced.Bind("", "a", "old")
	replaced.Bind("", "b", "b")
	replaced.Bind("", "a", "new")
	replaced.Bind("", "c", "c")
	if _, ok := replaced.Get("", "b"); ok {
		t.Fatal("replacement did not refresh its LRU position")
	}
	if got, ok := replaced.Get("", "a"); !ok || got != "new" {
		t.Fatalf("replacement after LRU eviction = (%q, %v), want (%q, true)", got, ok, "new")
	}

	one := NewBinder(BinderOptions{MaxEntries: 1, Clock: clock.Now})
	one.Bind("", "old", "old")
	one.Bind("", "new", "new")
	if _, ok := one.Get("", "old"); ok {
		t.Fatal("cap-one binder retained the old entry")
	}
	if got, ok := one.Get("", "new"); !ok || got != "new" {
		t.Fatalf("cap-one new entry = (%q, %v), want (%q, true)", got, ok, "new")
	}
}

func TestBinderLenAndExpiredEvictionCleanReverseIndex(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{TTL: 10 * time.Second, MaxEntries: 2, Clock: clock.Now})
	b.Bind("", "expired", "shared")
	clock.Set(testEpoch.Add(9 * time.Second))
	b.Bind("", "live", "shared")
	clock.Set(testEpoch.Add(10 * time.Second))
	if got := b.Len(); got != 1 {
		t.Fatalf("Len at one expiry boundary = %d, want 1", got)
	}
	b.Bind("", "new", "new-hop")
	if got := b.Forget("shared"); got != 1 {
		t.Fatalf("Forget(shared) after expired eviction = %d, want 1", got)
	}
	if _, ok := b.Get("", "live"); ok {
		t.Fatal("Forget(shared) left the live indexed binding")
	}
	if got, ok := b.Get("", "new"); !ok || got != "new-hop" {
		t.Fatalf("entry inserted after expiry = (%q, %v), want (%q, true)", got, ok, "new-hop")
	}

	expiring := NewBinder(BinderOptions{TTL: 10 * time.Second, Clock: clock.Now})
	expiring.Bind("", "only", "hop")
	clock.Set(testEpoch.Add(20 * time.Second))
	if got := expiring.Len(); got != 0 {
		t.Fatalf("Len with expired entry = %d, want 0", got)
	}
	if got := expiring.Forget("hop"); got != 0 {
		t.Fatalf("Forget after Len cleanup = %d, want 0", got)
	}

	evicting := NewBinder(BinderOptions{TTL: 10 * time.Second, MaxEntries: 2, Clock: clock.Now})
	clock.Set(testEpoch)
	evicting.Bind("", "stale", "shared")
	clock.Set(testEpoch.Add(9 * time.Second))
	evicting.Bind("", "live", "shared")
	clock.Set(testEpoch.Add(10 * time.Second))
	evicting.Bind("", "new", "new-hop")
	if got := evicting.Len(); got != 2 {
		t.Fatalf("Len after expired-entry eviction = %d, want 2", got)
	}
	if got := evicting.Forget("shared"); got != 1 {
		t.Fatalf("Forget(shared) after capacity cleanup = %d, want 1", got)
	}
	if _, ok := evicting.Get("", "live"); ok {
		t.Fatal("Forget(shared) left the live reverse-index entry")
	}
	if got, ok := evicting.Get("", "new"); !ok || got != "new-hop" {
		t.Fatalf("new entry after expired eviction = (%q, %v), want (%q, true)", got, ok, "new-hop")
	}
}

func TestBinderClockOutsideLock(t *testing.T) {
	var b *Binder
	clockWasUnlocked := false
	b = NewBinder(BinderOptions{Clock: func() time.Time {
		clockWasUnlocked = b.mu.TryLock()
		if clockWasUnlocked {
			b.mu.Unlock()
		}
		return testEpoch
	}})

	b.Bind("", "site", "hop")
	if !clockWasUnlocked {
		t.Fatal("Clock callback for Bind ran while the binder mutex was held")
	}

	clockWasUnlocked = false
	if got, ok := b.Get("", "site"); !ok || got != "hop" {
		t.Fatalf("Get = (%q, %v), want (%q, true)", got, ok, "hop")
	}
	if !clockWasUnlocked {
		t.Fatal("Clock callback for Get ran while the binder mutex was held")
	}

	clockWasUnlocked = false
	if got := b.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	if !clockWasUnlocked {
		t.Fatal("Clock callback for Len ran while the binder mutex was held")
	}
}

func TestBinderDefaults(t *testing.T) {
	b := NewBinder(BinderOptions{})
	if b.ttl != 24*time.Hour || b.maxEntries != 65536 || b.clock == nil {
		t.Fatalf("defaults = (TTL %v, MaxEntries %d, Clock nil %v)", b.ttl, b.maxEntries, b.clock == nil)
	}
	b.Bind("", "site", "hop")
	if got, ok := b.Get("", "site"); !ok || got != "hop" {
		t.Fatalf("default-clock round trip = (%q, %v), want (%q, true)", got, ok, "hop")
	}
}

func TestBinderConcurrentOperations(t *testing.T) {
	clock := newTestClock()
	b := NewBinder(BinderOptions{TTL: time.Hour, MaxEntries: 8, Clock: clock.Now})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 300; i++ {
				login := string(rune('a' + worker/2))
				site := string(rune('A' + (i/4)%12))
				key := "hop-0"
				if (worker+i/4)%2 != 0 {
					key = "hop-1"
				}
				switch (worker + i) % 4 {
				case 0:
					b.Bind(login, site, key)
				case 1:
					if got, ok := b.Get(login, site); ok && got != "hop-0" && got != "hop-1" {
						t.Errorf("Get returned unexpected key %q", got)
						return
					}
				case 2:
					b.Unbind(login, site)
				case 3:
					b.Forget(key)
				}
				if got := b.Len(); got > 8 {
					t.Errorf("Len during concurrent operations = %d, exceeds capacity 8", got)
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	if got := b.Len(); got > 8 {
		t.Fatalf("Len after concurrent operations = %d, exceeds capacity 8", got)
	}
}
