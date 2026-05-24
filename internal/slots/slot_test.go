package slots

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// uniqBackend yields a /tmp-collision-proof backend name so independent
// test runs (and a `go test ./... -count=N` cycle on the same machine)
// cannot collide on lock files at /tmp/sand-slots/<backend>/...
//
// /tmp/sand-slots/ is the production lock root by SAND-SPEC §1 — it is
// intentionally NOT under t.TempDir(). Tests therefore namespace via a
// unique backend name and clean up after themselves.
func uniqBackend(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("sand-test-%s-%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(slotsRoot, name))
	})
	return name
}

// TestAcquireSlot_SingleSlotExclusivity races two goroutines against a
// slots=1 budget. Exactly one must acquire; the other must time out with
// ErrSlotTimeout. This is the core mutex-of-1 acceptance test.
func TestAcquireSlot_SingleSlotExclusivity(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "exclusive-model"

	type result struct {
		slot *Slot
		err  error
	}
	results := make(chan result, 2)

	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < 2; i++ {
		go func() {
			start.Wait()
			s, err := AcquireSlot(backend, model, 1, 300*time.Millisecond)
			results <- result{slot: s, err: err}
		}()
	}
	start.Done()

	var winners, timeouts int
	var heldSlot *Slot
	for i := 0; i < 2; i++ {
		r := <-results
		switch {
		case r.err == nil && r.slot != nil:
			winners++
			heldSlot = r.slot
		case errors.Is(r.err, ErrSlotTimeout):
			timeouts++
		default:
			t.Fatalf("unexpected result: slot=%v err=%v", r.slot, r.err)
		}
	}
	if winners != 1 || timeouts != 1 {
		t.Fatalf("want exactly 1 winner + 1 timeout, got winners=%d timeouts=%d", winners, timeouts)
	}
	if heldSlot != nil {
		heldSlot.Release()
	}
}

// TestAcquireSlot_MultiSlotConcurrency races 6 goroutines against a
// slots=3 budget. Exactly 3 must hold simultaneously; the other 3 must
// time out. The 3 winners must each report a distinct Index in [1..3].
func TestAcquireSlot_MultiSlotConcurrency(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "multi-model"

	type result struct {
		slot *Slot
		err  error
	}
	const goroutines = 6
	const budget = 3
	results := make(chan result, goroutines)

	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < goroutines; i++ {
		go func() {
			start.Wait()
			s, err := AcquireSlot(backend, model, budget, 300*time.Millisecond)
			results <- result{slot: s, err: err}
		}()
	}
	start.Done()

	var winners, timeouts int
	heldSlots := make([]*Slot, 0, budget)
	seenIdx := map[int]int{}
	for i := 0; i < goroutines; i++ {
		r := <-results
		switch {
		case r.err == nil && r.slot != nil:
			winners++
			heldSlots = append(heldSlots, r.slot)
			seenIdx[r.slot.Index]++
		case errors.Is(r.err, ErrSlotTimeout):
			timeouts++
		default:
			t.Fatalf("unexpected result: slot=%v err=%v", r.slot, r.err)
		}
	}
	if winners != budget || timeouts != goroutines-budget {
		t.Fatalf("want exactly %d winners + %d timeouts, got winners=%d timeouts=%d",
			budget, goroutines-budget, winners, timeouts)
	}
	for idx, count := range seenIdx {
		if count != 1 {
			t.Fatalf("slot index %d held by %d goroutines (want 1) — non-exclusive!", idx, count)
		}
		if idx < 1 || idx > budget {
			t.Fatalf("slot index %d outside expected [1..%d]", idx, budget)
		}
	}
	for _, s := range heldSlots {
		s.Release()
	}
}

// TestAcquireSlot_ReleaseAndReuse verifies a slot is reusable after the
// holder calls Release. Without this guarantee the slot mechanism would
// leak budget across dispatches.
func TestAcquireSlot_ReleaseAndReuse(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "reuse-model"

	s1, err := AcquireSlot(backend, model, 1, 100*time.Millisecond)
	if err != nil || s1 == nil {
		t.Fatalf("first acquire: slot=%v err=%v", s1, err)
	}
	s1.Release()

	s2, err := AcquireSlot(backend, model, 1, 100*time.Millisecond)
	if err != nil || s2 == nil {
		t.Fatalf("second acquire after Release: slot=%v err=%v", s2, err)
	}
	// Double-Release MUST be a no-op (sync.Once).
	s2.Release()
	s2.Release()
}

// TestAcquireSlot_Timeout verifies ErrSlotTimeout is returned (and that
// waitMax is actually respected — elapsed time must be at least waitMax
// modulo a small scheduling slack).
func TestAcquireSlot_Timeout(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "timeout-model"

	holder, err := AcquireSlot(backend, model, 1, 100*time.Millisecond)
	if err != nil || holder == nil {
		t.Fatalf("holder acquire: slot=%v err=%v", holder, err)
	}
	defer holder.Release()

	waitMax := 250 * time.Millisecond
	start := time.Now()
	s, err := AcquireSlot(backend, model, 1, waitMax)
	elapsed := time.Since(start)

	if s != nil {
		t.Fatalf("expected nil slot on timeout, got %+v", s)
	}
	if !errors.Is(err, ErrSlotTimeout) {
		t.Fatalf("want ErrSlotTimeout, got %v", err)
	}
	// Allow a generous lower bound — kernel scheduling can finish the
	// last poll iteration slightly before the formal deadline.
	if elapsed < waitMax-50*time.Millisecond {
		t.Fatalf("timeout returned too early: elapsed=%v want >= %v", elapsed, waitMax)
	}
}

// TestAcquireSlot_UnlimitedZero verifies the slots=0 unlimited sentinel:
// returns (nil, nil) and does NOT create any /tmp/sand-slots/<backend>
// directory tree. This is the load-bearing "skip the filesystem entirely"
// promise from SAND-SPEC §1.4.
func TestAcquireSlot_UnlimitedZero(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "unlimited-model"

	s, err := AcquireSlot(backend, model, 0, time.Second)
	if err != nil {
		t.Fatalf("unlimited acquire: err=%v", err)
	}
	if s != nil {
		t.Fatalf("unlimited acquire: want nil Slot, got %+v", s)
	}

	// The backend's directory under /tmp/sand-slots/ must NOT have been
	// created — the implementation must skip the filesystem entirely.
	backendDir := filepath.Join(slotsRoot, backend)
	if _, statErr := os.Stat(backendDir); !os.IsNotExist(statErr) {
		t.Fatalf("unlimited path touched filesystem: stat(%s) err=%v (want IsNotExist)",
			backendDir, statErr)
	}

	// Release on a nil *Slot must be a safe no-op.
	s.Release()
}

// TestSlotSlug verifies the model-name slug helper produces filesystem-
// safe strings and preserves characters that are already safe.
func TestSlotSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"qwen3-coder:30b", "qwen3-coder_30b"},
		{"gpt-5.4+high", "gpt-5.4_high"},
		{"anthropic/claude-haiku", "anthropic_claude-haiku"},
		{"haiku", "haiku"},
		{"opus-4.5", "opus-4.5"},
		{"", "_"},
		{"weird name with spaces", "weird_name_with_spaces"},
	}
	for _, c := range cases {
		got := slug(c.in)
		if got != c.want {
			t.Errorf("slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSlotConcurrent_RaceFreeIndexAssignment is a stress check that hammers
// the slot subsystem with many short acquire/release cycles. It catches
// any state that the simpler exclusivity tests might miss — e.g. a slot
// being handed to two goroutines simultaneously under load. Use -race to
// surface any internal data race in the Slot/Release path.
func TestSlotConcurrent_RaceFreeIndexAssignment(t *testing.T) {
	t.Parallel()

	backend := uniqBackend(t)
	model := "stress-model"
	const budget = 3
	const goroutines = 12
	const acquires = 20

	var heldNow atomic.Int32
	var maxHeld atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < acquires; i++ {
				s, err := AcquireSlot(backend, model, budget, time.Second)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				cur := heldNow.Add(1)
				for {
					prev := maxHeld.Load()
					if cur <= prev || maxHeld.CompareAndSwap(prev, cur) {
						break
					}
				}
				// Hold briefly so concurrent goroutines collide.
				time.Sleep(2 * time.Millisecond)
				heldNow.Add(-1)
				s.Release()
			}
		}()
	}
	wg.Wait()

	if got := maxHeld.Load(); got > int32(budget) {
		t.Fatalf("max concurrent holders = %d, budget = %d — slot exclusivity violated", got, budget)
	}
	// Sanity check that the test actually exercised concurrency (else we
	// can't trust the upper-bound assertion above).
	if maxHeld.Load() < 2 {
		t.Logf("warning: max concurrent holders = %d, test may not have raced", maxHeld.Load())
	}
}

// Compile-time sanity: ensure Slot fields are exported as documented in
// SAND-SPEC §1.2.
var _ = &Slot{Backend: "claude-native", Model: "haiku", Index: 1}
