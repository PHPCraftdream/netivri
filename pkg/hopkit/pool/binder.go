package pool

import (
	"container/list"
	"sync"
	"time"
)

const (
	defaultBinderTTL        = 24 * time.Hour
	defaultBinderMaxEntries = 65536
)

// BinderOptions configures a pure site-to-hop binding store. Zero values use the defaults: a 24-hour TTL, 65536 entries, and time.Now for Clock.
type BinderOptions struct {
	// TTL is the sliding binding lifetime; nonpositive values use the 24-hour default.
	TTL time.Duration
	// MaxEntries is the capacity; nonpositive values use the 65536-entry default.
	MaxEntries int
	// Clock supplies timestamps. A nil Clock uses time.Now. It is called outside
	// the binder lock, so the callback must be safe for concurrent calls.
	Clock func() time.Time
}

type binderKey struct {
	login string
	site  string
}

type binderEntry struct {
	binding binderKey
	key     string
	expires time.Time
	lru     *list.Element
}

// Binder stores expiring, LRU-bounded site-to-hop bindings.
type Binder struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	clock      func() time.Time
	entries    map[binderKey]*binderEntry
	byHop      map[string]map[binderKey]*binderEntry
	lru        list.List
}

// NewBinder creates a binding store, defaulting nonpositive options.
func NewBinder(o BinderOptions) *Binder {
	if o.TTL <= 0 {
		o.TTL = defaultBinderTTL
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = defaultBinderMaxEntries
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Binder{
		ttl:        o.TTL,
		maxEntries: o.MaxEntries,
		clock:      o.Clock,
		entries:    make(map[binderKey]*binderEntry),
		byHop:      make(map[string]map[binderKey]*binderEntry),
	}
}

// Get returns a live binding and refreshes its sliding TTL and LRU position.
func (b *Binder) Get(login, site string) (key string, ok bool) {
	if site == "" {
		return "", false
	}
	now := b.clock()
	binding := binderKey{login: login, site: site}

	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[binding]
	if entry == nil {
		return "", false
	}
	if !now.Before(entry.expires) {
		b.remove(entry)
		return "", false
	}
	entry.expires = now.Add(b.ttl)
	b.lru.MoveToFront(entry.lru)
	return entry.key, true
}

// Bind creates or replaces a binding, refreshing its TTL and LRU position.
func (b *Binder) Bind(login, site, key string) {
	if site == "" || key == "" {
		return
	}
	now := b.clock()
	binding := binderKey{login: login, site: site}

	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.entries[binding]; entry != nil {
		b.remove(entry)
	}
	b.evictForInsert(now)

	entry := &binderEntry{binding: binding, key: key, expires: now.Add(b.ttl)}
	entry.lru = b.lru.PushFront(entry)
	b.entries[binding] = entry
	if b.byHop[key] == nil {
		b.byHop[key] = make(map[binderKey]*binderEntry)
	}
	b.byHop[key][binding] = entry
}

// Unbind removes the binding for one login and site; repeated calls are harmless.
func (b *Binder) Unbind(login, site string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.entries[binderKey{login: login, site: site}]; entry != nil {
		b.remove(entry)
	}
}

// Forget removes every binding for a hop key and returns the number removed.
func (b *Binder) Forget(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	bindings := b.byHop[key]
	count := len(bindings)
	for _, entry := range bindings {
		b.remove(entry)
	}
	return count
}

// Len returns the number of live bindings, lazily removing expired entries.
func (b *Binder) Len() int {
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeExpired(now)
	return len(b.entries)
}

func (b *Binder) remove(entry *binderEntry) {
	delete(b.entries, entry.binding)
	delete(b.byHop[entry.key], entry.binding)
	if len(b.byHop[entry.key]) == 0 {
		delete(b.byHop, entry.key)
	}
	b.lru.Remove(entry.lru)
}

func (b *Binder) removeExpired(now time.Time) {
	for element := b.lru.Back(); element != nil; {
		previous := element.Prev()
		entry := element.Value.(*binderEntry)
		if !now.Before(entry.expires) {
			b.remove(entry)
		}
		element = previous
	}
}

func (b *Binder) evictForInsert(now time.Time) {
	if len(b.entries) < b.maxEntries {
		return
	}
	b.removeExpired(now)
	if len(b.entries) >= b.maxEntries {
		b.remove(b.lru.Back().Value.(*binderEntry))
	}
}
