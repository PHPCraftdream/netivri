package angel

import (
	"runtime"
	"testing"
	"time"
)

// bootSeam replaces the boot time of the computer for one test.
func bootSeam(t *testing.T, boot time.Time, ok bool) {
	t.Helper()
	old := bootTime
	t.Cleanup(func() { bootTime = old })
	bootTime = func() (time.Time, bool) { return boot, ok }
}

func TestRebootedSince(t *testing.T) {
	started := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		boot time.Time
		ok   bool
		want bool
	}{
		{"booted hours after the start", started.Add(3 * time.Hour), true, true},
		{"booted just past the margin", started.Add(bootMargin + time.Second), true, true},
		{"booted within the margin", started.Add(bootMargin), true, false},
		{"booted at the start", started, true, false},
		{"booted before the start", started.Add(-2 * time.Hour), true, false},
		{"boot time unknown", time.Time{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bootSeam(t, tc.boot, tc.ok)
			boot, got := rebootedSince(started)
			if got != tc.want {
				t.Fatalf("rebootedSince = %v, want %v", got, tc.want)
			}
			if got && !boot.Equal(tc.boot) {
				t.Errorf("boot = %v, want %v", boot, tc.boot)
			}
			if !got && !boot.IsZero() {
				t.Errorf("boot = %v without a restart, want zero", boot)
			}
		})
	}
	bootSeam(t, started.Add(time.Hour), true)
	if _, got := rebootedSince(time.Time{}); got {
		t.Error("an unknown start counted as before the boot")
	}
}

func TestCrashStarted(t *testing.T) {
	fallback := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	header := "netivri angel crash dump\nstarted: 2026-11-14T15:44:01+03:00\npid: 9\nversion: 1\n\n"
	if got, want := crashStarted(header, fallback), time.Date(2026, 11, 14, 12, 44, 1, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("started = %v, want %v in UTC", got, want)
	}
	for name, text := range map[string]string{
		"unparsable": "netivri angel crash dump\nstarted: x\npid: 9\n\n",
		"missing":    "netivri angel crash dump\npid: 9\n\n",
		"empty":      "",
	} {
		if got := crashStarted(text, fallback); !got.Equal(fallback) {
			t.Errorf("%s: started = %v, want the fallback %v", name, got, fallback)
		}
	}
}

// The unclean stop of an angel that started before the boot names the boot; one
// that started after it, one with an unreadable start and an unknown boot time
// do not.
func TestReportCrashesNamesTheBootOfAnUncleanStop(t *testing.T) {
	boot := time.Date(2026, 11, 14, 15, 38, 2, 0, time.UTC)
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	write := func(home, name, started string) {
		writeCrashFile(t, home, name, "netivri angel crash dump\nstarted: "+started+"\npid: 1\nversion: 0.1.0\n\n", at)
	}

	home := t.TempDir()
	bootSeam(t, boot, true)
	write(home, "crash-20261113T090000Z-101.pending", "2026-11-13T09:00:00Z") // before the boot
	write(home, "crash-20261114T160000Z-102.pending", "2026-11-14T16:00:00Z") // after the boot
	write(home, "crash-20261114T160100Z-103.pending", "unreadable")           // start falls back to the file time: before the boot
	rep := ReportCrashes(home)
	got := map[int]Crash{}
	for _, c := range rep.Unclean {
		got[c.PID] = c
	}
	if len(got) != 3 {
		t.Fatalf("unclean = %+v, want three", rep.Unclean)
	}
	if c := got[101]; !c.BootedAt.Equal(boot) || !c.Started.Equal(time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("angel before the boot: %+v", c)
	}
	if c := got[102]; !c.BootedAt.IsZero() {
		t.Errorf("angel after the boot got BootedAt %v", c.BootedAt)
	}
	if c := got[103]; !c.BootedAt.Equal(boot) || !c.Started.Equal(at) {
		t.Errorf("unreadable start must fall back to the file time: %+v", c)
	}

	home = t.TempDir()
	bootSeam(t, time.Time{}, false)
	write(home, "crash-20261113T090000Z-101.pending", "2026-11-13T09:00:00Z")
	rep = ReportCrashes(home)
	if len(rep.Unclean) != 1 || !rep.Unclean[0].BootedAt.IsZero() {
		t.Errorf("unknown boot time: %+v", rep.Unclean)
	}
}

// A crash with a traceback never names a boot: it was a crash.
func TestReportCrashesDumpHasNoBoot(t *testing.T) {
	bootSeam(t, time.Date(2026, 11, 14, 15, 38, 2, 0, time.UTC), true)
	home := t.TempDir()
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	writeCrashFile(t, home, "crash-20261113T090000Z-101.pending", "netivri angel crash dump\nstarted: 2026-11-13T09:00:00Z\npid: 101\nversion: 0.1.0\n\npanic: boom\n\ngoroutine 1 [running]:\n", at)
	rep := ReportCrashes(home)
	if len(rep.Dumps) != 1 || !rep.Dumps[0].BootedAt.IsZero() || len(rep.Unclean) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// The real source of the boot time works on the systems that have one: a boot
// in the past, not older than a few years, and agreeing with itself.
func TestOSBootTimeIsSane(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		t.Skip("no boot time source on " + runtime.GOOS)
	}
	boot, ok := osBootTime()
	if !ok {
		t.Fatal("no boot time on " + runtime.GOOS)
	}
	now := time.Now()
	if boot.After(now.Add(time.Minute)) {
		t.Errorf("boot %v is in the future (now %v)", boot, now)
	}
	if boot.Before(now.AddDate(-10, 0, 0)) {
		t.Errorf("boot %v is over ten years ago (now %v)", boot, now)
	}
	again, ok := osBootTime()
	if !ok || again.Sub(boot).Abs() > 5*time.Second {
		t.Errorf("two readings differ: %v and %v", boot, again)
	}
	// A computer that booted before this test started has not rebooted since.
	if _, rebooted := rebootedSince(now); rebooted {
		t.Errorf("a start now counted as before the boot %v", boot)
	}
}
