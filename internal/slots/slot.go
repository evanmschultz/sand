// Package slots implements sand's cross-project slot subsystem. A "slot" is
// kernel-managed reservation against a (backend, model) pair, backed by an
// advisory file lock (syscall.Flock with LOCK_EX|LOCK_NB) on a lock file
// under /tmp/sand-slots/<backend>/<model-slug>/slot.<N>.lock. Locks are
// auto-released by the kernel when the holding process dies, so no PID
// liveness polling is needed (SIGKILL-safe). See SAND-V02-SPEC.md §1 for
// the full design rationale.
//
// slots=0 is the explicit "unlimited" sentinel: AcquireSlot returns
// (nil, nil) without touching the filesystem at all, so callers can use the
// same call site regardless of whether the tier enforces a slot ceiling.
package slots

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// slotsRoot is the directory under which per-backend, per-model lock files
// live. /tmp is intentional: it clears on reboot, which means stale locks
// from a previous boot auto-vanish with no explicit cleanup path.
const slotsRoot = "/tmp/sand-slots"

// pollInterval is the gap between attempts when all configured slots for a
// (backend, model) are currently held. Chosen to be small enough that wait
// budgets in the single-digit-second range still feel responsive, while
// large enough to avoid spinning hot.
const pollInterval = 100 * time.Millisecond

// ErrSlotTimeout is returned by AcquireSlot when waitMax elapses with all
// slot files still held. Callers (notably the dispatcher loop in
// internal/dispatch) match it with errors.Is and advance to the next chain
// tier when they see it.
var ErrSlotTimeout = errors.New("slots: all slots busy after wait_max")

// Slot is a held advisory file lock on one slot index for a (backend, model)
// pair. Callers MUST call Release when done; the held fd otherwise stays
// open until process exit, at which point the kernel reclaims the lock.
//
// The exported fields are diagnostic only — the dispatcher records them in
// the per-attempt audit log; correctness only depends on the unexported fd
// being closed via Release.
type Slot struct {
	Backend string
	Model   string
	Index   int

	fd       *os.File
	released sync.Once
}

// Release drops the advisory lock and closes the underlying file
// descriptor. Safe to call multiple times — only the first call actually
// touches the fd. The lock file itself is intentionally NOT removed: another
// process may already be waiting on it, and removing it would let a third
// process recreate a fresh file and acquire its own lock concurrently.
func (s *Slot) Release() {
	if s == nil {
		return
	}
	s.released.Do(func() {
		if s.fd == nil {
			return
		}
		// Best-effort explicit unlock; fd.Close also releases the lock,
		// but an explicit LOCK_UN narrows the window during which the
		// kernel still considers us the holder.
		_ = syscall.Flock(int(s.fd.Fd()), syscall.LOCK_UN)
		_ = s.fd.Close()
		s.fd = nil
	})
}

// AcquireSlot tries to claim one of `slots` advisory lock files for the
// given (backend, model) pair, polling at pollInterval until waitMax
// elapses. On success it returns a *Slot the caller must Release.
//
// slots == 0 is the explicit "unlimited" sentinel: AcquireSlot returns
// (nil, nil) immediately without creating /tmp/sand-slots/<backend>/...,
// so the caller code path is identical regardless of slot policy.
//
// On exhaustion AcquireSlot returns (nil, ErrSlotTimeout). All other
// errors (mkdir failure, file open failure, unexpected flock errno) are
// wrapped with %w and surfaced to the caller — the dispatcher treats those
// as terminal for this tier.
func AcquireSlot(backend, model string, slots int, waitMax time.Duration) (*Slot, error) {
	if slots == 0 {
		// Unlimited — skip the filesystem entirely. No directory is
		// created for this (backend, model) pair.
		return nil, nil
	}
	if slots < 0 {
		return nil, fmt.Errorf("slots: invalid slot count %d (must be >= 0)", slots)
	}

	dir := filepath.Join(slotsRoot, backend, slug(model))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("slots: mkdir %s: %w", dir, err)
	}

	deadline := time.Now().Add(waitMax)
	for {
		for idx := 1; idx <= slots; idx++ {
			s, err := tryAcquire(backend, model, idx, dir)
			if err != nil {
				return nil, err
			}
			if s != nil {
				return s, nil
			}
		}
		// All slots busy this pass.
		if !time.Now().Before(deadline) {
			return nil, ErrSlotTimeout
		}
		// Don't oversleep the deadline; cap the sleep at the remaining
		// budget so the next iteration sees the deadline cleanly.
		remaining := time.Until(deadline)
		if remaining > pollInterval {
			remaining = pollInterval
		}
		time.Sleep(remaining)
	}
}

// tryAcquire opens the lock file for one slot index and attempts a
// non-blocking exclusive flock. Returns (nil, nil) on EWOULDBLOCK (slot
// held by someone else — caller should try the next index). Returns
// (*Slot, nil) on success. Returns (nil, err) on any other failure.
func tryAcquire(backend, model string, idx int, dir string) (*Slot, error) {
	path := filepath.Join(dir, "slot."+strconv.Itoa(idx)+".lock")
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("slots: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = fd.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, fmt.Errorf("slots: flock %s: %w", path, err)
	}
	return &Slot{
		Backend: backend,
		Model:   model,
		Index:   idx,
		fd:      fd,
	}, nil
}

// slug returns a filesystem-safe rendering of a model name. The model
// strings used in chains.toml (e.g. "qwen3-coder:30b", "gpt-5.4+high",
// "anthropic/claude-haiku") contain characters — colons, slashes, plus
// signs — that either break path semantics or behave inconsistently
// across filesystems. slug replaces every byte outside [A-Za-z0-9._-]
// with an underscore. Collisions within a single backend's chain are
// acceptable (and rare in practice — chain tiers are model-distinct).
func slug(model string) string {
	if model == "" {
		return "_"
	}
	b := make([]byte, len(model))
	for i := 0; i < len(model); i++ {
		c := model[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c
		case c >= 'a' && c <= 'z':
			b[i] = c
		case c >= '0' && c <= '9':
			b[i] = c
		case c == '.' || c == '_' || c == '-':
			b[i] = c
		default:
			b[i] = '_'
		}
	}
	return string(b)
}
