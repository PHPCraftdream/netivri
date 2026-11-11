package main

import (
	"time"

	"github.com/PHPCraftdream/netivri/internal/core"
	hoppool "github.com/PHPCraftdream/netivri/pkg/hopkit/pool"
)

func (l *liveAngel) siteBinder() *hoppool.Binder {
	l.hopBinderOnce.Do(func() { l.hopBinder = hoppool.NewBinder(hoppool.BinderOptions{TTL: 24 * time.Hour}) })
	return l.hopBinder
}

// hopServices returns the shared binder and the health controller only after
// the latter has been needed by a real core start/reconfigure. Merely building
// options in unit tests therefore does not start its worker goroutine.
func (l *liveAngel) hopServices() (*hoppool.Binder, *hopHealth) {
	l.hopMu.Lock()
	health := l.hopHealth
	l.hopMu.Unlock()
	return l.siteBinder(), health
}

func (l *liveAngel) ensureHopHealth() *hopHealth {
	l.hopHealthOnce.Do(func() {
		info := logFunc(func(format string, args ...any) {
			if l.logf != nil {
				l.logf(format, args...)
			}
		})
		warn := logFunc(func(format string, args ...any) {
			if l.warnf != nil {
				l.warnf(format, args...)
			}
		})
		health := newHopHealth(l.opts.Home, info, warn, nil, nil)
		l.hopMu.Lock()
		l.hopHealth = health
		l.hopMu.Unlock()
	})
	l.hopMu.Lock()
	defer l.hopMu.Unlock()
	return l.hopHealth
}

// configureHopOptions is also used by option-only callers. It shares the
// per-live site binder but leaves health-worker creation to actual lifecycle
// operations.
func (l *liveAngel) configureHopOptions(o *core.Options) {
	if o == nil {
		return
	}
	o.SiteBinder = l.siteBinder()
	l.hopMu.Lock()
	health := l.hopHealth
	l.hopMu.Unlock()
	if health != nil {
		o.OnHopEvent = health.Hook()
	}
}

// prepareHopStart ensures a callback is installed before Start. A retry or a
// rollback needs a fresh hook because its saved Options may belong to a retired
// generation.
func (l *liveAngel) prepareHopStart(o *core.Options, refreshHook bool) *hopHealth {
	if o == nil {
		return nil
	}
	o.SiteBinder = l.siteBinder()
	health := l.ensureHopHealth()
	if refreshHook || o.OnHopEvent == nil {
		o.OnHopEvent = health.Hook()
	}
	return health
}

func (l *liveAngel) attachHopHealth(inst coreInstance) {
	_, health := l.hopServices()
	if health == nil || inst == nil {
		return
	}
	if hopCore, ok := inst.(hopHealthCore); ok {
		health.Attach(hopCore)
	}
}

func (l *liveAngel) detachHopHealth() {
	_, health := l.hopServices()
	if health != nil {
		health.Detach()
	}
}

func (l *liveAngel) activateHopHealth() {
	_, health := l.hopServices()
	if health != nil {
		health.Activate()
		health.Refresh()
	}
}

func (l *liveAngel) rejectHopHealth() {
	_, health := l.hopServices()
	if health != nil {
		health.RejectPending()
	}
}

func (l *liveAngel) closeHopHealth() {
	l.hopMu.Lock()
	health := l.hopHealth
	l.hopMu.Unlock()
	if health != nil {
		health.Close()
	}
}
