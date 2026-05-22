// Package preflight implements the per-tier backend health-check logic for
// sand's `preflight` MCP tool (SAND-SPEC §3.2).
//
// Preflight walks a role's configured fallback chain and, for each tier,
// records whether the tier's backend is currently usable: claude CLI on PATH
// for claude-native, codex CLI on PATH for codex-exec, and (for ollama-local)
// both that the ollama daemon answers `GET /api/version` and that the tier's
// model is present in the local `ollama list` output.
//
// This package owns only the pure walk + result-shaping logic. All external
// I/O (PATH lookups, HTTP, the `ollama list` invocation) is mediated through
// the Probe interface so unit tests can substitute deterministic stubs
// without depending on real binaries, network reachability, or installed
// models. The MCP tool wrapper that adapts the Probe to real stdlib calls
// lives in a sibling builder droplet; this package does not import os/exec
// or net/http directly.
package preflight

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/evanmschultz/sand/internal/chains"
)

// Backend identifiers used throughout this package. They match the strings
// the chains parser produces for the Tier.Backend field per SAND-SPEC §5 and
// must stay in sync with the chain-validator's accepted values.
const (
	BackendOllamaLocal  = "ollama-local"
	BackendCodexExec    = "codex-exec"
	BackendClaudeNative = "claude-native"
)

// ollamaVersionURL is the daemon liveness endpoint per SAND-SPEC §3.2.
// Exported as an unexported package-level constant rather than baked into
// the call site so test stubs can match it without string duplication.
const ollamaVersionURL = "http://localhost:11434/api/version"

// Result is one row of the preflight report. It mirrors the TOON tabular
// shape declared in SAND-SPEC §3.2 (`tiers[N]{tier,backend,model,ok,reason}`):
// when OK is true Reason is the empty string; when OK is false Reason carries
// a short human-readable diagnostic.
type Result struct {
	// Tier is the 1-based tier index within the role's chain, matching the
	// position of the corresponding chains.Tier in the input slice.
	Tier int

	// Backend is the tier's backend identifier (e.g. "ollama-local").
	Backend string

	// Model is the tier's model identifier (e.g. "qwen2.5-coder:7b"), copied
	// verbatim from the chain tier.
	Model string

	// OK reports whether the tier's backend is currently usable.
	OK bool

	// Reason is the diagnostic string explaining why OK is false; empty when
	// OK is true.
	Reason string
}

// Report is the top-level preflight outcome for a single role: the role name
// plus the per-tier Result rows in chain order.
type Report struct {
	// Role is the role identifier the preflight was run for (e.g.
	// "ta-go-builder").
	Role string

	// Tiers is the ordered slice of per-tier Result rows, one per entry in
	// the input chain.
	Tiers []Result
}

// Probe is the dependency-injected I/O surface Preflight uses to interrogate
// the host environment. Tests pass a deterministic stub; production wiring
// (in the MCP tool wrapper droplet) bridges these methods to exec.LookPath,
// an http.Client, and exec.CommandContext("ollama", "list").
//
// All methods accept context.Context where cancellation is meaningful so the
// MCP tool can enforce a per-call deadline against the chain walk.
type Probe interface {
	// LookPath reports whether the named CLI binary is resolvable on the
	// host PATH. It returns the absolute path on success and a non-nil
	// error when the binary cannot be located (mirroring exec.LookPath).
	LookPath(name string) (string, error)

	// HTTPGet issues an HTTP GET against url. Implementations are expected
	// to honour the context for cancellation/timeout. Callers are
	// responsible for closing the returned response body.
	HTTPGet(ctx context.Context, url string) (*http.Response, error)

	// OllamaList returns the raw stdout of `ollama list` (or an equivalent
	// programmatic listing). The returned string is parsed by Preflight to
	// determine whether the tier's model is present locally; an error
	// indicates the listing could not be obtained at all (e.g. ollama
	// binary missing, daemon unreachable).
	OllamaList(ctx context.Context) (string, error)
}

// Preflight walks chain in order and returns one Result per tier. The
// returned Report is always populated: there is no "tool failed" error
// channel because per-tier failures are surfaced as ok=false rows, which is
// the contract the SAND-SPEC §3.2 TOON response shape codifies.
//
// chain may be nil or empty; in that case the returned Report carries the
// role name and a non-nil empty Tiers slice (so downstream TOON encoding
// emits `tiers[0]{...}:` rather than a missing key).
func Preflight(ctx context.Context, p Probe, role string, chain []chains.Tier) Report {
	rows := make([]Result, 0, len(chain))
	for i, tier := range chain {
		rows = append(rows, checkTier(ctx, p, i+1, tier))
	}
	return Report{Role: role, Tiers: rows}
}

// checkTier dispatches a single tier to its backend-specific probe and packs
// the outcome into a Result. Unknown backends are reported as ok=false with
// a diagnostic rather than ignored, so chain-config drift surfaces in the
// preflight report instead of silently producing an empty row.
func checkTier(ctx context.Context, p Probe, idx int, t chains.Tier) Result {
	r := Result{Tier: idx, Backend: t.Backend, Model: t.Model}

	switch t.Backend {
	case BackendClaudeNative:
		r.OK, r.Reason = checkCLI(p, "claude")
	case BackendCodexExec:
		r.OK, r.Reason = checkCLI(p, "codex")
	case BackendOllamaLocal:
		r.OK, r.Reason = checkOllama(ctx, p, t.Model)
	default:
		r.OK = false
		r.Reason = fmt.Sprintf("unknown backend %q", t.Backend)
	}

	return r
}

// checkCLI asks the probe whether the named CLI binary resolves on PATH.
// On success it returns (true, ""); on failure it returns (false, diag)
// where diag names the missing binary and the underlying error message.
func checkCLI(p Probe, name string) (bool, string) {
	if _, err := p.LookPath(name); err != nil {
		return false, fmt.Sprintf("%s CLI not on PATH: %v", name, err)
	}
	return true, ""
}

// checkOllama runs the two-stage ollama-local check: daemon liveness via
// `GET /api/version`, then model presence via `ollama list`. The first
// failure short-circuits with a reason scoped to the failing stage.
func checkOllama(ctx context.Context, p Probe, model string) (bool, string) {
	if ok, reason := checkOllamaDaemon(ctx, p); !ok {
		return false, reason
	}
	return checkOllamaModel(ctx, p, model)
}

// checkOllamaDaemon probes the ollama HTTP `/api/version` endpoint. A
// non-2xx response or transport error counts as unreachable; the body is
// always closed.
func checkOllamaDaemon(ctx context.Context, p Probe) (bool, string) {
	resp, err := p.HTTPGet(ctx, ollamaVersionURL)
	if err != nil {
		return false, fmt.Sprintf("ollama daemon unreachable at localhost:11434: %v", err)
	}
	defer func() {
		// Drain + close so HTTP/1.1 keep-alive connections can be
		// reused by the underlying transport. Errors during drain are
		// ignored because the response status has already been
		// captured.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("ollama daemon returned status %d at /api/version", resp.StatusCode)
	}
	return true, ""
}

// checkOllamaModel asks the probe for the current `ollama list` output and
// scans for the requested model. The empty-model edge case (a malformed
// chain entry) returns ok=false with an explicit diagnostic rather than
// matching every row.
func checkOllamaModel(ctx context.Context, p Probe, model string) (bool, string) {
	if model == "" {
		return false, "ollama-local tier has no model configured"
	}

	out, err := p.OllamaList(ctx)
	if err != nil {
		return false, fmt.Sprintf("ollama list failed: %v", err)
	}

	if hasOllamaModel(out, model) {
		return true, ""
	}
	return false, fmt.Sprintf("model %q not pulled locally", model)
}

// hasOllamaModel scans `ollama list` text output for a row whose first
// whitespace-separated field equals model. `ollama list` tabular output
// places the model name (e.g. `qwen2.5-coder:7b`) in the first column; we
// avoid substring matching so e.g. `qwen2.5-coder:7b-instruct` does not
// shadow a chain entry for `qwen2.5-coder:7b`.
func hasOllamaModel(out, model string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == model {
			return true
		}
	}
	return false
}
