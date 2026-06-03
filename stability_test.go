package eonId

import (
	"sync"
	"testing"
	"time"
)

// TestClockBackwardWait verifies that when the clock goes backward and the
// ClockDriftAction is "wait", the generator eventually recovers and produces
// a valid, monotonically-increasing ID (no duplicates).
func TestClockBackwardWait(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = true
	cfg.ClockDriftAction = ClockDriftActionWait
	cfg.EnableClockDriftProtection = false // only test the backward-clock path

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Generate one ID so lastTimestamp is set.
	first, err := g.GenerateID()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate clock going backward by bumping lastTimestamp into the future
	// (from the generator's perspective the "current" time will look like it went back).
	g.mu.Lock()
	g.lastTimestamp = g.lastTimestamp + 200 // 200 ms ahead
	g.mu.Unlock()

	// The generator should wait internally and then produce a valid ID.
	second, err := g.GenerateID()
	if err != nil {
		t.Fatalf("expected recovery from backward clock, got error: %v", err)
	}
	if second <= first {
		t.Errorf("expected second ID (%d) > first ID (%d) after clock recovery", second, first)
	}

	// Clock drift counter should have been incremented.
	stats := g.GetStats()
	if stats.ClockBackwardCount == 0 {
		t.Error("expected ClockBackwardCount > 0 after simulated backward clock")
	}
}

// TestClockBackwardError verifies that with ClockDriftAction = "error",
// a backward clock immediately returns a ClockDriftError.
func TestClockBackwardError(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	cfg.ClockDriftAction = ClockDriftActionError
	cfg.EnableClockDriftProtection = false

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Seed lastTimestamp so the clock looks like it went backward.
	g.mu.Lock()
	g.lastTimestamp = time.Now().UnixMilli() + 500 // 500 ms ahead
	g.mu.Unlock()

	_, err = g.GenerateID()
	if err == nil {
		t.Fatal("expected ClockDriftError, got nil")
	}
	if _, ok := err.(*ClockDriftError); !ok {
		t.Errorf("expected *ClockDriftError, got %T: %v", err, err)
	}
}

// TestClockBackwardIgnore verifies that with ClockDriftAction = "ignore",
// the generator produces monotonically-increasing IDs by advancing lastTimestamp.
func TestClockBackwardIgnore(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	cfg.ClockDriftAction = ClockDriftActionIgnore
	cfg.EnableClockDriftProtection = false

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	first, err := g.GenerateID()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate 50 ms backward clock – within maxIgnoreBackwardDriftMs.
	g.mu.Lock()
	g.lastTimestamp = g.lastTimestamp + 50
	g.mu.Unlock()

	second, err := g.GenerateID()
	if err != nil {
		t.Fatalf("ClockDriftActionIgnore should not error on small backward drift: %v", err)
	}
	if second <= first {
		t.Errorf("expected second ID (%d) > first ID (%d) with ignore mode", second, first)
	}
}

// TestConcurrentUniqueness generates IDs from many goroutines and asserts
// that every ID is unique across all goroutines.
func TestConcurrentUniqueness(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 50
	const perGoroutine = 2000

	ids := make(chan int64, goroutines*perGoroutine)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				id, err := g.GenerateID()
				if err != nil {
					t.Errorf("GenerateID failed: %v", err)
					return
				}
				ids <- id
			}
		}()
	}

	wg.Wait()
	close(ids)

	seen := make(map[int64]struct{}, goroutines*perGoroutine)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID detected: %d", id)
		}
		seen[id] = struct{}{}
	}

	if want := goroutines * perGoroutine; len(seen) != want {
		t.Errorf("expected %d unique IDs, got %d", want, len(seen))
	}
}

// TestWorkerIDConflictError verifies that WorkerIDConflictError implements
// the error interface with meaningful output.
func TestWorkerIDConflictError(t *testing.T) {
	e := &WorkerIDConflictError{
		WorkerID:     7,
		DatacenterID: 2,
		ConflictWith: "10.0.0.1",
	}
	msg := e.Error()
	if msg == "" {
		t.Error("WorkerIDConflictError.Error() must not be empty")
	}
}

// TestGeneratorShutdownRejectsNewIDs ensures GenerateID returns an error
// after Shutdown is called.
func TestGeneratorShutdownRejectsNewIDs(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if err := g.Shutdown(nil); err != nil {
		t.Fatal(err)
	}

	_, err = g.GenerateID()
	if err == nil {
		t.Error("expected error from GenerateID after Shutdown, got nil")
	}
}

// TestMetricsClockDriftRecorded verifies that the in-memory Metrics struct
// increments ClockDriftEvents when the clock goes backward.
func TestMetricsClockDriftRecorded(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = true
	cfg.ClockDriftAction = ClockDriftActionWait
	cfg.EnableClockDriftProtection = false

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Seed lastTimestamp 150 ms ahead so clock appears to go backward.
	g.mu.Lock()
	g.lastTimestamp = time.Now().UnixMilli() + 150
	g.mu.Unlock()

	// This will detect backward clock, wait, then succeed.
	if _, err := g.GenerateID(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := g.GetMetrics().GetSnapshot()
	if snap.ClockDriftEvents == 0 {
		t.Error("expected ClockDriftEvents > 0 after backward clock simulation")
	}
}

// TestMetricsSequenceOverflowRecorded verifies that SequenceOverflows increments
// when the sequence space is exhausted within a millisecond.
func TestMetricsSequenceOverflowRecorded(t *testing.T) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = true
	cfg.EnableSequenceCache = false
	// Use minimum valid bits (7 bits = 128 sequences, maxSequence = 127).
	cfg.SequenceBits = 7
	cfg.DatacenterIDBits = 5
	cfg.WorkerIDBits = 10

	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Force the generator to think we are in the same millisecond and that
	// the sequence is already at maxSequence (127), so the next call wraps to 0.
	now := time.Now().UnixMilli()
	g.mu.Lock()
	g.lastTimestamp = now
	g.sequence = g.maxSequence // = 127
	g.mu.Unlock()

	// The next GenerateID call inside the same millisecond will increment to
	// (127+1)&127 = 0, triggering overflow.  The generator waits and retries
	// in the next millisecond, so no error is expected.
	if _, err := g.GenerateID(); err != nil {
		t.Fatalf("ID generation failed after overflow wait: %v", err)
	}

	snap := g.GetMetrics().GetSnapshot()
	if snap.SequenceOverflows == 0 {
		t.Error("expected SequenceOverflows > 0 after forcing sequence wrap")
	}
}

// BenchmarkGenerateID_NoMetrics benchmarks bare ID generation with metrics disabled.
func BenchmarkGenerateID_NoMetrics(b *testing.B) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	cfg.EnableSequenceCache = false
	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := g.GenerateID(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGenerateID_Parallel benchmarks concurrent ID generation.
func BenchmarkGenerateID_Parallel(b *testing.B) {
	cfg := DefaultGeneratorConfig()
	cfg.EnableMetrics = false
	g, err := NewSnowflakeGeneratorCore(1, 1, cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := g.GenerateID(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
