package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PHPCraftdream/netivri/internal/angel"
	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

// Variables only so that tests can shorten them.
var (
	// refreshStartDelay keeps the first refresh away from the angel's start.
	refreshStartDelay = 30 * time.Second
	// refreshWake is how often the angel checks which subscription is due.
	refreshWake = 30 * time.Second
	// refreshQuickRetry is the wait before the one retry of a fetch that failed
	// on the transport (a timeout, a reset), inside the same attempt.
	refreshQuickRetry = 5 * time.Second
)

const (
	// refreshRetry is the wait after a failed refresh (never longer than the
	// interval itself).
	refreshRetry = 10 * time.Minute
	// refreshWarnStreak is how many failed refreshes of one subscription in a
	// row raise a WARN; fewer are INFO while the stored list is still recent.
	refreshWarnStreak = 3
	// refreshWarnEvery: while the refreshes of a subscription keep failing,
	// one WARN per this long; the other failures are DEBUG lines. A network
	// that is down for hours is one warning an hour, not one per try.
	refreshWarnEvery = time.Hour
)

// refreshSchedule tracks when each subscription is due again.
type refreshSchedule struct {
	notBefore time.Time
	due       map[string]time.Time
}

func newRefreshSchedule(notBefore time.Time) *refreshSchedule {
	return &refreshSchedule{notBefore: notBefore, due: map[string]time.Time{}}
}

// pick returns the subscriptions that are due at now, in the given order, and
// forgets the ones that are no longer configured. A subscription seen for the
// first time is due one interval after its stored fetch time (at once when it
// has none), but never before notBefore.
func (s *refreshSchedule) pick(now time.Time, names []string, interval time.Duration, fetchedAt func(string) (time.Time, bool)) []string {
	keep := make(map[string]bool, len(names))
	var out []string
	for _, name := range names {
		keep[name] = true
		due, known := s.due[name]
		if !known {
			due = now
			if at, ok := fetchedAt(name); ok {
				due = at.Add(interval)
			}
			if due.Before(s.notBefore) {
				due = s.notBefore
			}
		}
		// A shortened interval takes effect at once.
		if latest := now.Add(interval); due.After(latest) {
			due = latest
		}
		s.due[name] = due
		if !due.After(now) {
			out = append(out, name)
		}
	}
	for name := range s.due {
		if !keep[name] {
			delete(s.due, name)
		}
	}
	return out
}

// done schedules the next refresh of a subscription after an attempt.
func (s *refreshSchedule) done(name string, now time.Time, ok bool, interval time.Duration) {
	wait := interval
	if !ok && refreshRetry < wait {
		wait = refreshRetry
	}
	s.due[name] = now.Add(wait)
}

// refreshInfoLocked is the status view of the refresh; l.mu must be held.
func (l *liveAngel) refreshInfoLocked() *angel.RefreshInfo {
	info := &angel.RefreshInfo{OK: l.refreshOK, Failed: l.refreshFail, LastError: l.refreshErr}
	if l.latest != nil {
		if d, on := l.latest.Fetch.Interval(); on {
			info.IntervalSeconds = int64(d / time.Second)
		}
	}
	if !l.lastRefresh.IsZero() {
		info.LastAt = l.lastRefresh.UTC().Format(time.RFC3339)
	}
	return info
}

func (l *liveAngel) noteRefresh(ok bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastRefresh = time.Now()
	if ok {
		l.refreshOK++
		l.refreshErr = ""
		return
	}
	l.refreshFail++
	l.refreshErr = errText(err)
}

// refreshLoop re-fetches every configured subscription once per interval
// (fetch.update_interval, one hour by default). The snapshot is stored for
// the file watcher to apply; a failed fetch keeps the old snapshot.
func (l *liveAngel) refreshLoop(ctx context.Context) {
	sched := newRefreshSchedule(time.Now().Add(refreshStartDelay))
	store := subscription.NewStore(filepath.Join(l.opts.Home, "subscriptions"))
	fetchedAt := func(name string) (time.Time, bool) {
		snap, err := store.Load(name)
		if err != nil {
			return time.Time{}, false
		}
		return snap.FetchedAt, true
	}
	t := time.NewTicker(refreshWake)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		l.mu.Lock()
		cfg := l.latest
		l.mu.Unlock()
		interval, on := cfg.Fetch.Interval()
		if !on {
			continue
		}
		names := make([]string, 0, len(cfg.Subscriptions))
		bySub := make(map[string]config.Subscription, len(cfg.Subscriptions))
		for _, s := range cfg.Subscriptions {
			names = append(names, s.Name)
			bySub[s.Name] = s
		}
		for _, name := range sched.pick(time.Now(), names, interval, fetchedAt) {
			if ctx.Err() != nil {
				return
			}
			ok := l.refreshOne(ctx, store, cfg, bySub[name])
			sched.done(name, time.Now(), ok, interval)
		}
	}
}

// refreshOne fetches one subscription and stores it, unless storing it would
// leave an account without nodes: an unattended refresh must not break a
// working setup, so such a snapshot is rejected and the old one stays.
func (l *liveAngel) refreshOne(ctx context.Context, store *subscription.Store, cfg *config.Config, s config.Subscription) bool {
	started := time.Now()
	snap, err := fetchSnapshot(ctx, subFetch, time.Now, cfg, s)
	retried := false
	if err != nil && transportFailure(err) && sleepCtx(ctx, refreshQuickRetry) {
		retried = true
		snap, err = fetchSnapshot(ctx, subFetch, time.Now, cfg, s)
	}
	took := time.Since(started).Round(100 * time.Millisecond)
	if err != nil {
		l.noteRefresh(false, err)
		l.fetchFailed(store, cfg, s.Name, err, took, retried)
		return false
	}
	override := map[string]*subscription.Snapshot{s.Name: &snap}
	if _, err := accountRoutePoolWith(l.opts.Home, cfg, override); err != nil {
		l.noteRefresh(false, err)
		l.warnf("subscription %s: refreshed list rejected, the old one stays: %s", s.Name, errText(err))
		return false
	}
	if err := store.Save(s.Name, snap); err != nil {
		l.noteRefresh(false, err)
		l.warnf("subscription %s: refreshed list not stored: %v", s.Name, err)
		return false
	}
	l.noteRefresh(true, nil)
	fails, since := l.streakEnd(s.Name)
	second := ""
	if retried {
		second = ", at the second try"
	}
	if fails > 0 {
		second += fmt.Sprintf(", after %s in a row (%s)", count(fails, "failed refresh", "failed refreshes"), ageText(l.refreshTime().Sub(since)))
	}
	l.logf("subscription %s: refreshed, %d nodes in %s%s", s.Name, len(snap.Nodes), took, second)
	return true
}

// transportFailure reports a fetch that failed on the way (a timeout, a
// reset, no route), which a retry may get through; a refused status or a list
// that does not parse is not one.
func transportFailure(err error) bool {
	var ce *cli.Error
	if !errors.As(err, &ce) || ce.Code != "FETCH_FAILED" {
		return false
	}
	return strings.Contains(ce.Message, "request failed") || strings.Contains(ce.Message, "timed out")
}

// sleepCtx waits d and reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// refreshRun is the failed refreshes of one subscription in a row: how many,
// since when, and when the last WARN about them was written.
type refreshRun struct {
	fails         int
	since, warned time.Time
}

func (l *liveAngel) refreshTime() time.Time {
	if l.refreshClock != nil {
		return l.refreshClock()
	}
	return time.Now()
}

// streakUp counts one more failed refresh of a subscription in a row and
// returns the count; streakEnd forgets the run and returns what it was.
func (l *liveAngel) streakUp(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refreshRuns == nil {
		l.refreshRuns = map[string]*refreshRun{}
	}
	run := l.refreshRuns[name]
	if run == nil {
		run = &refreshRun{since: l.refreshTime()}
		l.refreshRuns[name] = run
	}
	run.fails++
	return run.fails
}

func (l *liveAngel) streakEnd(name string) (fails int, since time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	run := l.refreshRuns[name]
	delete(l.refreshRuns, name)
	if run == nil {
		return 0, time.Time{}
	}
	return run.fails, run.since
}

// warnDue reports whether a WARN about the failing refreshes of a
// subscription is due (none yet in this run, or refreshWarnEvery since the
// last) and records it as written.
func (l *liveAngel) warnDue(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	run := l.refreshRuns[name]
	now := l.refreshTime()
	if run == nil || (!run.warned.IsZero() && now.Sub(run.warned) < refreshWarnEvery) {
		return false
	}
	run.warned = now
	return true
}

// fetchFailed logs a refresh whose fetch failed. The stored list stays, so a
// single failure while it is recent is INFO; WARN comes with the third failure
// in a row, a list older than two intervals, or no list at all.
func (l *liveAngel) fetchFailed(store *subscription.Store, cfg *config.Config, name string, err error, took time.Duration, retried bool) {
	streak := l.streakUp(name)
	interval, _ := cfg.Fetch.Interval()
	retry := min(refreshRetry, interval)
	line := fmt.Sprintf("subscription %s: refresh failed after %s", name, took)
	if retried {
		line += " (two tries)"
	}
	line += ": " + errText(err)
	snap, loadErr := store.Load(name)
	if loadErr != nil {
		l.failureLine(name, "%s; no stored list, next try in %s (%s in a row)", line, ageText(retry), count(streak, "failure", "failures"))
		return
	}
	age := time.Since(snap.FetchedAt)
	line += fmt.Sprintf("; the list of %s ago stays, next try in %s", ageText(age), ageText(retry))
	if streak >= refreshWarnStreak || age > 2*interval {
		l.failureLine(name, "%s (%s in a row)", line, count(streak, "failure", "failures"))
		return
	}
	l.logf("%s", line)
}

// failureLine writes a failed refresh that deserves a WARN: at WARN the first
// time and once per refreshWarnEvery after, at DEBUG in between.
func (l *liveAngel) failureLine(name, format string, args ...any) {
	if l.warnDue(name) {
		l.warnf(format+"; the next failures are DEBUG, a WARN again in %s", append(args, ageText(refreshWarnEvery))...)
		return
	}
	l.debugf(format, args...)
}

// ageText renders a duration as minutes below an hour, as hours and minutes
// above.
func ageText(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
