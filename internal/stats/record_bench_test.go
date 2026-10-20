//go:build bench

package stats

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const benchNodes = 50

// benchLabels are prebuilt so the timed loops do not format strings.
var benchLabels = func() [benchNodes]string {
	var labels [benchNodes]string
	for i := range labels {
		labels[i] = fmt.Sprintf("node%d", i)
	}
	return labels
}()

// benchStore opens a writable store in a benchmark temp directory.
func benchStore(b *testing.B, queueSize int) *Store {
	b.Helper()
	s, err := Open(Options{
		Path:      filepath.Join(b.TempDir(), "stats.db"),
		QueueSize: queueSize,
	})
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

// benchSample builds one dial sample for the node index; at is taken once by
// the caller so the timed loops measure Record, not the clock.
func benchSample(i int, at time.Time) Sample {
	return Sample{
		At:    at,
		Label: benchLabels[i%benchNodes],
		Kind:  KindDial,
		OK:    true,
		Delay: time.Duration(i%100) * time.Millisecond,
	}
}

// reportDropped publishes how many samples the bounded queue rejected, so a
// result that mixes enqueues and rejections is visible instead of hidden.
func reportDropped(b *testing.B, s *Store) {
	b.ReportMetric(float64(s.Dropped()), "dropped")
}

func BenchmarkRecord(b *testing.B) {
	s := benchStore(b, 1<<16)
	at := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Record(benchSample(i, at))
	}
	b.StopTimer()
	reportDropped(b, s)
}

func BenchmarkRecordParallel(b *testing.B) {
	s := benchStore(b, 1<<16)
	at := time.Now()
	var workers atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker walks its own sequence: no shared counter in the loop.
		i := int(workers.Add(1)) * 7919
		for pb.Next() {
			i++
			s.Record(benchSample(i, at))
		}
	})
	b.StopTimer()
	reportDropped(b, s)
}

func BenchmarkNodesQuery(b *testing.B) {
	s := benchStore(b, 1<<17)
	const samples = 100000
	at := time.Now()
	for i := 0; i < samples; i++ {
		s.Record(benchSample(i, at))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	if err := s.Flush(ctx); err != nil {
		cancel()
		b.Fatalf("Flush: %v", err)
	}
	cancel()
	if s.Dropped() != 0 {
		b.Fatalf("setup dropped %d samples", s.Dropped())
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.Nodes(Query{})
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != benchNodes {
			b.Fatalf("Nodes returned %d nodes, want %d", len(got), benchNodes)
		}
	}
}

func BenchmarkFlushBatch(b *testing.B) {
	s := benchStore(b, 1<<16)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	at := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 256; j++ {
			s.Record(benchSample(i*256+j, at))
		}
		if err := s.Flush(ctx); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportDropped(b, s)
}
