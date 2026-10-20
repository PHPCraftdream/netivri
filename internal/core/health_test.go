package core

import (
	"math"
	"testing"
	"time"
)

func TestHealthScoreFormula(t *testing.T) {
	tests := []struct {
		name       string
		delayMS    float64
		failRate   float64
		throughput float64
		want       float64
	}{
		{name: "plain", delayMS: 100, want: 100},
		{name: "failures", delayMS: 100, failRate: 0.25, want: 200},
		{name: "throughput", delayMS: 100, throughput: 10, want: 100 / 1.5},
		{name: "throughput clamped", delayMS: 100, throughput: 40, want: 50},
		{name: "negative throughput clamped", delayMS: 100, throughput: -5, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := healthScore(test.delayMS, test.failRate, test.throughput)
			if math.Abs(got-test.want) > 1e-9 {
				t.Fatalf("healthScore = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNodeHealthEWMAs(t *testing.T) {
	var h nodeHealth
	h.recordProbe(true, 100*time.Millisecond)
	h.recordProbe(true, 200*time.Millisecond)
	// 0.3*200 + 0.7*100 = 130.
	if math.Abs(h.probeEWMS-130) > 1e-9 {
		t.Fatalf("probe EWMA = %v, want 130", h.probeEWMS)
	}
	h.recordDial(true, 100*time.Millisecond)
	h.recordDial(true, 200*time.Millisecond)
	// 0.2*200 + 0.8*100 = 120.
	if math.Abs(h.dialEWMS-120) > 1e-9 {
		t.Fatalf("dial EWMA = %v, want 120", h.dialEWMS)
	}
	delay, known := h.delayMS()
	if !known || math.Abs(delay-125) > 1e-9 {
		t.Fatalf("delay = %v, known = %v, want 125/true", delay, known)
	}
}

func TestNodeHealthDelayUnknownWithoutSamples(t *testing.T) {
	var h nodeHealth
	if _, known := h.delayMS(); known {
		t.Fatal("delay known without samples")
	}
	h.recordProbe(true, time.Second)
	delay, known := h.delayMS()
	if !known || delay != 1000 {
		t.Fatalf("delay = %v, known = %v, want 1000/true", delay, known)
	}
}

func TestNodeHealthFailRateRing(t *testing.T) {
	var h nodeHealth
	for i := 0; i < healthRingSize; i++ {
		h.recordOutcome(i%4 != 0) // 24 successes, 8 failures in 32 outcomes
	}
	if math.Abs(h.failRate()-0.25) > 1e-9 {
		t.Fatalf("fail rate = %v, want 0.25", h.failRate())
	}
	// The ring only keeps the last 32 outcomes: 32 successes clear the rate.
	for i := 0; i < healthRingSize; i++ {
		h.recordOutcome(true)
	}
	if h.failRate() != 0 {
		t.Fatalf("fail rate = %v, want 0", h.failRate())
	}
}

func TestNodeHealthDownRule(t *testing.T) {
	t.Run("two probe failures", func(t *testing.T) {
		var h nodeHealth
		h.recordProbe(false, 0)
		if h.Down() {
			t.Fatal("one probe failure must not be down")
		}
		h.recordProbe(false, 0)
		if !h.Down() {
			t.Fatal("two probe failures must be down")
		}
		h.recordProbe(true, 50*time.Millisecond)
		if h.Down() {
			t.Fatal("success must clear down")
		}
	})
	t.Run("three dial failures", func(t *testing.T) {
		var h nodeHealth
		h.recordDial(false, 0)
		h.recordDial(false, 0)
		if h.Down() {
			t.Fatal("two dial failures must not be down")
		}
		h.recordDial(false, 0)
		if !h.Down() {
			t.Fatal("three dial failures must be down")
		}
		h.recordDial(true, 0)
		if h.Down() {
			t.Fatal("success must clear down")
		}
	})
}

// Any success clears both consecutive-failure counters, so a successful
// dial clears a probe-induced down state and vice versa.
func TestNodeHealthAnySuccessClearsDown(t *testing.T) {
	var h nodeHealth
	h.recordProbe(false, 0)
	h.recordProbe(false, 0)
	if !h.Down() {
		t.Fatal("two probe failures must be down")
	}
	h.recordDial(true, 0)
	if h.Down() {
		t.Fatal("a successful dial must clear a probe-induced down state")
	}
	h.recordDial(false, 0)
	h.recordDial(false, 0)
	h.recordDial(false, 0)
	if !h.Down() {
		t.Fatal("three dial failures must be down")
	}
	h.recordProbe(true, time.Millisecond)
	if h.Down() {
		t.Fatal("a successful probe must clear a dial-induced down state")
	}
}

func TestNodeHealthThroughputEWMA(t *testing.T) {
	var h nodeHealth
	// Below the 64 KiB window: ignored.
	h.recordThroughput(16<<10, time.Second)
	if h.throughput != 0 {
		t.Fatalf("small window fed the EWMA: %v", h.throughput)
	}
	// 64 KiB/s = 0.0625 MiB/s.
	h.recordThroughput(64<<10, time.Second)
	if math.Abs(h.throughput-0.0625) > 1e-9 {
		t.Fatalf("throughput = %v, want 0.0625", h.throughput)
	}
	// 128 KiB in one second: 0.2*0.125 + 0.8*0.0625 = 0.075.
	h.recordThroughput(128<<10, time.Second)
	if math.Abs(h.throughput-0.075) > 1e-9 {
		t.Fatalf("throughput = %v, want 0.075", h.throughput)
	}
}

func TestHealthTable(t *testing.T) {
	table := newHealthTable()
	table.register([]string{"a", "b"})
	table.recordProbe("a", true, 100*time.Millisecond)
	table.recordProbe("zz", true, 50*time.Millisecond) // unknown label: ignored
	table.recordDial("b", false, 0)
	table.recordDial("b", false, 0)
	table.recordDial("b", false, 0)

	if !table.down("b") {
		t.Fatal("b should be down after three dial failures")
	}
	if table.down("a") || table.down("unknown") {
		t.Fatal("a and unknown labels must not be down")
	}
	if len(table.entries) != 2 {
		t.Fatalf("records = %d, want 2 (unknown labels are ignored)", len(table.entries))
	}
	views := table.views([]string{"a", "b"})
	if len(views) != 2 {
		t.Fatalf("views = %v, want two", views)
	}
	if !views[0].Known || views[0].Down {
		t.Fatalf("view a = %+v", views[0])
	}
	if !views[1].Down {
		t.Fatalf("view b = %+v, want down", views[1])
	}
	if math.Abs(views[0].Score-100) > 1e-9 {
		t.Fatalf("score a = %v, want 100", views[0].Score)
	}
	if math.Abs(views[0].DelayMS-100) > 1e-9 {
		t.Fatalf("delay a = %v, want 100", views[0].DelayMS)
	}
}
