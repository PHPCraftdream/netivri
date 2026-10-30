package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/netivri/internal/cli"
	"github.com/PHPCraftdream/netivri/internal/config"
	"github.com/PHPCraftdream/netivri/internal/fetch"
	"github.com/PHPCraftdream/netivri/internal/subscription"
)

func timeoutFailure() error {
	return &cli.Error{Code: "FETCH_FAILED", Message: "request to sub.example failed: request failed: timeout"}
}

// refreshRig is a live angel whose subscription fetch is scripted: fetchFn
// gets the number of the call (from 1) and returns the answer.
type refreshRig struct {
	l            *liveAngel
	store        *subscription.Store
	cfg          *config.Config
	sub          config.Subscription
	infos, warns []string
	calls        int
}

func newRefreshRig(t *testing.T, storedAgo time.Duration, fetchFn func(call int) (*fetch.Result, error)) *refreshRig {
	t.Helper()
	home := t.TempDir()
	r := &refreshRig{store: subscription.NewStore(filepath.Join(home, "subscriptions"))}
	if storedAgo > 0 {
		snap := subscription.Snapshot{FetchedAt: time.Now().Add(-storedAgo), Nodes: []subscription.Node{node("fi-1", "FI")}}
		if err := r.store.Save("main", snap); err != nil {
			t.Fatal(err)
		}
	}
	oldFetch, oldRetry := subFetch, refreshQuickRetry
	subFetch = func(_ context.Context, _ string, _ fetch.Options) (*fetch.Result, error) {
		r.calls++
		return fetchFn(r.calls)
	}
	refreshQuickRetry = time.Millisecond
	t.Cleanup(func() { subFetch, refreshQuickRetry = oldFetch, oldRetry })
	r.l = newLiveAngel(upOptions{Home: home}, func(format string, args ...any) { r.infos = append(r.infos, fmt.Sprintf(format, args...)) }, func() {})
	r.l.warnf = func(format string, args ...any) { r.warns = append(r.warns, fmt.Sprintf(format, args...)) }
	r.sub = config.Subscription{Name: "main", URL: "https://main.invalid/sub"}
	r.cfg = config.Default()
	r.cfg.Subscriptions = []config.Subscription{r.sub}
	return r
}

func (r *refreshRig) refresh(t *testing.T) bool {
	t.Helper()
	return r.l.refreshOne(t.Context(), r.store, r.cfg, r.sub)
}

func okResult(int) (*fetch.Result, error) {
	return &fetch.Result{Body: []byte(testBodyFI), Status: 200}, nil
}

// One failed refresh while the stored list is recent is INFO, says what stays
// and when the next try is, and is tried twice.
func TestRefreshFailureIsInfoWhileTheListIsRecent(t *testing.T) {
	r := newRefreshRig(t, 41*time.Minute, func(int) (*fetch.Result, error) { return nil, timeoutFailure() })
	if r.refresh(t) {
		t.Fatal("a failed fetch reported success")
	}
	if r.calls != 2 {
		t.Errorf("%d fetches, want 2 (one quick retry)", r.calls)
	}
	if len(r.warns) != 0 || len(r.infos) != 1 {
		t.Fatalf("warns %q infos %q, want one INFO", r.warns, r.infos)
	}
	for _, want := range []string{"subscription main: refresh failed after ", "(two tries)", "request failed: timeout", "the list of 41m ago stays", "next try in 10m"} {
		if !strings.Contains(r.infos[0], want) {
			t.Errorf("%q misses %q", r.infos[0], want)
		}
	}
	if strings.Contains(r.infos[0], "main.invalid") {
		t.Errorf("the line names the URL: %q", r.infos[0])
	}
}

// The third failure in a row is a WARN; a success starts the count again.
func TestRefreshThirdFailureInARowWarns(t *testing.T) {
	succeed := false
	r := newRefreshRig(t, 41*time.Minute, func(int) (*fetch.Result, error) {
		if succeed {
			return okResult(0)
		}
		return nil, timeoutFailure()
	})
	for i := 1; i <= 3; i++ {
		r.refresh(t)
		if want := map[int]int{1: 0, 2: 0, 3: 1}[i]; len(r.warns) != want {
			t.Fatalf("after failure %d: %d warns, want %d: %q", i, len(r.warns), want, r.warns)
		}
	}
	if !strings.Contains(r.warns[0], "(3 failures in a row)") {
		t.Errorf("warn = %q", r.warns[0])
	}
	succeed = true
	if !r.refresh(t) {
		t.Fatal("a good fetch failed")
	}
	succeed = false
	r.warns = nil
	r.refresh(t)
	if len(r.warns) != 0 {
		t.Errorf("the count was not reset by the success: %q", r.warns)
	}
}

// A list older than two intervals, or none at all, makes the first failure a
// WARN.
func TestRefreshFailureWarnsWhenThereIsNoRecentList(t *testing.T) {
	old := newRefreshRig(t, 3*time.Hour, func(int) (*fetch.Result, error) { return nil, timeoutFailure() })
	old.refresh(t)
	if len(old.warns) != 1 || !strings.Contains(old.warns[0], "the list of 3h00m ago stays") {
		t.Errorf("old list: warns %q infos %q", old.warns, old.infos)
	}
	none := newRefreshRig(t, 0, func(int) (*fetch.Result, error) { return nil, timeoutFailure() })
	none.refresh(t)
	if len(none.warns) != 1 || !strings.Contains(none.warns[0], "no stored list") {
		t.Errorf("no list: warns %q infos %q", none.warns, none.infos)
	}
}

// A refusal is not retried; a cancelled run does not wait for the retry.
func TestRefreshRetriesOnlyTransportFailures(t *testing.T) {
	refused := newRefreshRig(t, 41*time.Minute, func(int) (*fetch.Result, error) {
		return nil, &cli.Error{Code: "FETCH_FAILED", Message: "request to sub.example failed: server answered 403"}
	})
	refused.refresh(t)
	if refused.calls != 1 {
		t.Errorf("a refusal was fetched %d times, want 1", refused.calls)
	}

	cancelled := newRefreshRig(t, 41*time.Minute, func(int) (*fetch.Result, error) { return nil, timeoutFailure() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	refreshQuickRetry = time.Hour
	if cancelled.l.refreshOne(ctx, cancelled.store, cancelled.cfg, cancelled.sub) || cancelled.calls != 1 {
		t.Errorf("cancelled: %d fetches, want 1 and a failure", cancelled.calls)
	}
}

func TestRefreshSuccessAtTheSecondTry(t *testing.T) {
	r := newRefreshRig(t, 41*time.Minute, func(call int) (*fetch.Result, error) {
		if call == 1 {
			return nil, timeoutFailure()
		}
		return okResult(call)
	})
	if !r.refresh(t) {
		t.Fatal("the second try did not succeed")
	}
	if len(r.warns) != 0 || len(r.infos) != 1 || !strings.Contains(r.infos[0], "refreshed, 1 nodes in ") || !strings.HasSuffix(r.infos[0], ", at the second try") {
		t.Errorf("warns %q infos %q", r.warns, r.infos)
	}
	plain := newRefreshRig(t, 41*time.Minute, okResult)
	plain.refresh(t)
	if len(plain.infos) != 1 || !strings.Contains(plain.infos[0], "refreshed, 1 nodes in ") || strings.Contains(plain.infos[0], "second try") {
		t.Errorf("infos %q", plain.infos)
	}
}

func TestTransportFailureAndAgeText(t *testing.T) {
	for err, want := range map[error]bool{
		timeoutFailure(): true,
		&cli.Error{Code: "FETCH_FAILED", Message: "request to h failed: request timed out or canceled"}: true,
		&cli.Error{Code: "FETCH_FAILED", Message: "request to h failed: server answered 403"}:           false,
		&cli.Error{Code: "FETCH_PROXY_FAILED", Message: "could not reach or negotiate"}:                 false,
		errors.New("request failed"): false,
	} {
		if got := transportFailure(err); got != want {
			t.Errorf("transportFailure(%v) = %v, want %v", err, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		0: "0m", 29 * time.Second: "0m", 41 * time.Minute: "41m", 59*time.Minute + 40*time.Second: "1h00m",
		3 * time.Hour: "3h00m", 25*time.Hour + 5*time.Minute: "25h05m",
	} {
		if got := ageText(in); got != want {
			t.Errorf("ageText(%v) = %q, want %q", in, got, want)
		}
	}
}
