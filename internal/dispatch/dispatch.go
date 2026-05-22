// Package dispatch declares the core public contract for sand's
// `sand.dispatch` MCP tool: the typed Params/Response shape and the package
// level sentinel errors that callers use to distinguish unsupported backends,
// unknown roles, and missing claude-native tiers.
//
// Per SAND-SPEC §3.1, dispatch is sand's transport surface; it carries no
// workflow state and no opinions about which backends are valid in a given
// release — the chain config drives that. This file defines only the shapes
// and sentinels referenced by sibling droplets that implement the actual
// Dispatch entry point, persona/chain wiring, dry-run rendering, Claude CLI
// spawn, JSON envelope parsing, and TOON encoding.
//
// The Dispatch function body lives in a sibling droplet
// (drop_003.drop.droplet_dispatch_persona_chains_dryrun); this file
// intentionally does not declare it so the function signature is owned by
// exactly one droplet.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
	"github.com/evanmschultz/sand/internal/slots"
)

// backendClaudeNative is the chains.Tier.Backend value sand's drop_003
// claude-native dispatch path recognizes. Hoisted to a constant so callers
// can compare without re-typing the literal.
const backendClaudeNative = "claude-native"

// loadChainsConfig resolves the caller's chain config via the v0.2
// hierarchical rules (project → XDG → $HOME/.config → $HOME/.sand — see
// chains.Resolve) and parses the winning file.
//
// When no config exists on any rung, chains.Resolve returns
// ErrChainConfigNotFound; loadChainsConfig surfaces an error that satisfies
// BOTH errors.Is(err, chains.ErrChainConfigNotFound) AND
// errors.Is(err, os.ErrNotExist). The dual-target wrap is intentional: the
// drop_003 tests pin os.ErrNotExist for the "no chains config" case and we
// keep that contract intact while the v0.2 hierarchical resolver lands.
//
// Errors from parse propagate via %w so callers can use errors.Is / errors.As
// against the underlying toml package errors.
func loadChainsConfig(cwd string) (chains.Config, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return chains.Config{}, fmt.Errorf("dispatch: resolve chains config cwd %q: %w", cwd, err)
	}

	path, _, err := chains.Resolve(absCwd)
	if err != nil {
		if errors.Is(err, chains.ErrChainConfigNotFound) {
			// Preserve drop_003-era os.ErrNotExist contract AND the v0.2
			// ErrChainConfigNotFound sentinel via errors.Join. The literal
			// "sand-chains.toml" string is preserved in the error text so
			// the existing TestDispatchSelectionErrors substring assertion
			// remains satisfied.
			return chains.Config{}, fmt.Errorf(
				"dispatch: locate sand-chains.toml: %w",
				errors.Join(err, os.ErrNotExist),
			)
		}
		return chains.Config{}, fmt.Errorf("dispatch: resolve chains config: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return chains.Config{}, fmt.Errorf("dispatch: open chains config %q: %w", path, err)
	}
	defer f.Close()

	cfg, err := chains.Parse(f)
	if err != nil {
		return chains.Config{}, fmt.Errorf("dispatch: parse chains config %q: %w", path, err)
	}
	return cfg, nil
}

// selectClaudeNativeTier walks tiers in order and returns the first entry
// whose Backend is claude-native, along with its 1-indexed position. When no
// claude-native tier exists the returned tier is the zero value, idx is 0,
// and err wraps ErrNoClaudeNativeTier.
func selectClaudeNativeTier(role string, tiers []chains.Tier) (chains.Tier, int, error) {
	for i, t := range tiers {
		if t.Backend == backendClaudeNative {
			return t, i + 1, nil
		}
	}
	return chains.Tier{}, 0, fmt.Errorf("dispatch: role %q: %w", role, ErrNoClaudeNativeTier)
}

// renderDryRunCommand produces the human-readable claude command shape
// returned in Response.Result when Dispatch runs with DryRun=true. The shape
// mirrors SAND-SPEC §7.3 and the runClaudeNative argv construction: one
// argument per line so tests can grep for specific flags without parsing a
// shell-quoted string. The persona body is rendered into
// `--append-system-prompt` verbatim; the persona's tool list becomes
// `--allowedTools <csv>`; `--mcp-config <path>` is included only when
// mcpConfigPath is non-empty.
//
// The output is informational only — it does NOT spawn the CLI. Sibling
// droplets that own the real spawn (claude.go::runClaudeNative) build argv
// directly via os/exec rather than parsing this rendering.
func renderDryRunCommand(prompt string, p persona.Persona, model, mcpConfigPath string) string {
	var b strings.Builder
	b.WriteString("claude -p\n")
	b.WriteString("  --bare\n")
	b.WriteString("  --model " + model + "\n")
	b.WriteString("  --output-format json\n")
	b.WriteString("  --no-session-persistence\n")
	b.WriteString("  --append-system-prompt " + strconvQuote(p.Body) + "\n")
	if mcpConfigPath != "" {
		b.WriteString("  --mcp-config " + mcpConfigPath + "\n")
	}
	if len(p.Tools) > 0 {
		b.WriteString("  --allowedTools " + strings.Join(p.Tools, ",") + "\n")
	}
	b.WriteString("  <<< " + strconvQuote(prompt) + "\n")
	return b.String()
}

// strconvQuote is a tiny indirection so the dry-run rendering can wrap
// free-form values (persona body, prompt) in a double-quoted form without
// pulling strconv directly into the call site. Using strconv.Quote guarantees
// embedded newlines / quotes are escaped so the rendered command stays one
// argument per line.
func strconvQuote(s string) string {
	// Local import-free quoter via strings: we use strconv.Quote semantics
	// (escape \n, \t, embedded quotes). Implemented inline to avoid widening
	// the dispatch package's import surface for one call site.
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// nowFn is the package-level clock seam Dispatch uses to stamp
// Attempt.AttemptedAt. Tests may override it to make per-attempt timestamps
// deterministic; production keeps it pointed at time.Now.
var nowFn = time.Now

// Dispatch is the public entry point for sand's `sand.dispatch` MCP tool.
//
// Per SAND-V02-SPEC §1.4, Dispatch walks the role's fallback chain tier-by-tier:
// for each tier it (a) optionally acquires a cross-project slot via
// slots.AcquireSlot when tier.Slots > 0, (b) calls runTier — which preserves
// the drop_003 ErrUnsupportedBackend guard for non-claude-native backends —
// (c) classifies the outcome via ClassifyExitError, and (d) records an
// Attempt row in Response.FallbackChain regardless of success or failure.
//
// Outcome policy (mirrors SAND-V02-SPEC §1.4 + §3.3):
//
//   - success            : record Attempt + return populated Response.
//   - slot_timeout       : record Attempt + advance to next tier.
//   - unsupported_backend: record Attempt + advance (drop_003 behavior
//     preserved while drops 004/005 wire ollama + codex).
//   - rate_limit / auth_failure / network / timeout: record Attempt + advance.
//   - crash / unknown    : record Attempt + return wrapped error (chain HALTS
//     for unrecoverable spawn failures per §3.3).
//
// When the chain exhausts without success, Dispatch returns ErrChainExhausted
// (wrapped) with FallbackChain populated for every tier attempted.
//
// Dry-run mode (params.DryRun == true) is preserved as a pre-loop branch:
// drop_003's TestDispatchDryRun contract still pins Tier=0 + ServedBy naming
// the first claude-native tier in the chain, with no actual spawn invoked.
//
// Failure modes (each wraps the underlying cause with %w so callers may use
// errors.Is):
//
//   - persona load failure (missing file, malformed frontmatter) propagates
//     from persona.Load.
//   - chains config load / parse failure propagates from loadChainsConfig.
//   - role not in chain config returns a wrapped ErrRoleNotInChains.
//   - role chain contains zero claude-native tiers returns a wrapped
//     ErrNoClaudeNativeTier (preserved guard until drops 004/005 broaden the
//     supported-backend set).
//   - MCP config resolution errors propagate from resolveMCPConfig.
//   - All tiers fail returns wrapped ErrChainExhausted with FallbackChain
//     populated.
func Dispatch(ctx context.Context, params Params) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, fmt.Errorf("dispatch: aborted before start: %w", err)
	}

	p, err := persona.Load(params.CWD, params.Role)
	if err != nil {
		return Response{}, fmt.Errorf("dispatch: load persona for role %q: %w", params.Role, err)
	}

	cfg, err := loadChainsConfig(params.CWD)
	if err != nil {
		return Response{}, err
	}

	tiers, err := cfg.Chain(params.Role)
	if err != nil {
		if errors.Is(err, chains.ErrUnknownRole) {
			return Response{}, fmt.Errorf("dispatch: role %q not in chain config: %w", params.Role, ErrRoleNotInChains)
		}
		return Response{}, fmt.Errorf("dispatch: chain lookup for role %q: %w", params.Role, err)
	}

	// drop_003 compatibility: until drops 004/005 light up ollama/codex, a
	// chain consisting entirely of non-claude-native tiers cannot serve. The
	// pre-loop guard surfaces ErrNoClaudeNativeTier so the existing test
	// contract (TestDispatchSelectionErrors/no claude-native tier) stays
	// green. Once drops 004/005 broaden runTier's supported set, this guard
	// can be removed — chain exhaustion will then surface as
	// ErrChainExhausted.
	cnTier, _, err := selectClaudeNativeTier(params.Role, tiers)
	if err != nil {
		return Response{}, err
	}

	mcpPath, mcpExists, err := resolveMCPConfig(params.CWD)
	if err != nil {
		return Response{}, err
	}
	renderedMCPPath := ""
	if mcpExists {
		renderedMCPPath = mcpPath
	}

	if params.DryRun {
		// Dry-run preserves the drop_003 contract: Tier=0 sentinel +
		// ServedBy naming the first claude-native tier. ModelOverride wins
		// over the tier's configured model. No FallbackChain rows because
		// no real attempt happens.
		dryRunModel := cnTier.Model
		if params.ModelOverride != "" {
			dryRunModel = params.ModelOverride
		}
		return Response{
			Result:   renderDryRunCommand(params.Prompt, p, dryRunModel, renderedMCPPath),
			ServedBy: backendClaudeNative + ":" + dryRunModel,
			Tier:     0,
		}, nil
	}

	// Wet-run path: loop over tiers, recording an Attempt per iteration.
	chain := make([]Attempt, 0, len(tiers))
	for i, tier := range tiers {
		tierIdx := i + 1
		attempt := Attempt{
			Tier:        tierIdx,
			Backend:     tier.Backend,
			Model:       tier.Model,
			AttemptedAt: nowFn().UTC(),
		}

		// Slot acquisition is a per-tier ceiling on concurrent spawns
		// across all sand processes. slots=0 is the explicit "unlimited"
		// sentinel — AcquireSlot returns (nil, nil) without touching the
		// filesystem.
		var slot *slots.Slot
		if tier.Slots > 0 {
			waitMax := time.Duration(tier.WaitMax) * time.Second
			s, slotErr := slots.AcquireSlot(tier.Backend, tier.Model, tier.Slots, waitMax)
			if slotErr != nil {
				if errors.Is(slotErr, slots.ErrSlotTimeout) {
					attempt.Outcome = "slot_timeout"
					attempt.Reason = fmt.Sprintf("all %d slots busy for %s", tier.Slots, waitMax)
					chain = append(chain, attempt)
					continue
				}
				attempt.Outcome = "unknown"
				attempt.Reason = slotErr.Error()
				chain = append(chain, attempt)
				return Response{FallbackChain: chain}, fmt.Errorf("dispatch: acquire slot for tier %d (%s:%s): %w", tierIdx, tier.Backend, tier.Model, slotErr)
			}
			slot = s
		}

		// Model override replaces the served tier's model. Attempt rows
		// for non-served tiers still record the chain's CONFIGURED model
		// (set before this branch) so the FallbackChain audit reflects
		// the actual chain layout, not the override.
		spawnTier := tier
		if params.ModelOverride != "" {
			spawnTier.Model = params.ModelOverride
		}

		result, runErr := runTier(ctx, params, p, spawnTier, renderedMCPPath, nil)

		// Per-tier slot release: must happen BEFORE we continue or return.
		// defer would batch all releases until function return, which is
		// wrong — we need the next tier (or test) to see freed slots
		// immediately.
		if slot != nil {
			slot.Release()
		}

		if runErr != nil {
			if errors.Is(runErr, ErrUnsupportedBackend) {
				attempt.Outcome = "unsupported_backend"
				attempt.Reason = fmt.Sprintf("sand does not yet spawn %q", tier.Backend)
				chain = append(chain, attempt)
				continue
			}
			// Spawn-level failure (e.g. claude binary not on PATH, ctx
			// cancelled). Classify with exit code -1 / empty stderr — both
			// land as Crash/Unknown. We treat ctx.Canceled / DeadlineExceeded
			// distinctly because the caller asked us to stop.
			if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				attempt.Outcome = ErrClassTimeout.String()
				attempt.Reason = runErr.Error()
				chain = append(chain, attempt)
				return Response{FallbackChain: chain}, fmt.Errorf("dispatch: tier %d spawn cancelled: %w", tierIdx, runErr)
			}
			// Unrecoverable spawn plumbing failure — halt chain.
			attempt.Outcome = ErrClassUnknown.String()
			attempt.Reason = runErr.Error()
			chain = append(chain, attempt)
			return Response{FallbackChain: chain}, fmt.Errorf("dispatch: spawn tier %d (%s:%s): %w", tierIdx, tier.Backend, tier.Model, runErr)
		}

		// Classify by exit code + stderr (errors_class.go). Non-zero exit
		// is NOT a Go error at runTier's layer — it lives in
		// claudeResult.ExitCode and stderr.
		class := ClassifyExitError(result.Stderr, result.ExitCode)
		switch class {
		case ErrClassSuccess:
			attempt.Outcome = class.String()
			chain = append(chain, attempt)
			return buildSuccessResponse(params, spawnTier, tierIdx, result, chain)
		case ErrClassRateLimit, ErrClassAuthFailure, ErrClassNetwork, ErrClassTimeout:
			attempt.Outcome = class.String()
			attempt.Reason = summarizeStderr(result.Stderr)
			chain = append(chain, attempt)
			continue
		default:
			// ErrClassCrash / ErrClassUnknown — unrecoverable per
			// SAND-V02-SPEC §3.3. Record + halt with FallbackChain
			// preserved.
			attempt.Outcome = class.String()
			attempt.Reason = summarizeStderr(result.Stderr)
			chain = append(chain, attempt)
			return Response{FallbackChain: chain}, fmt.Errorf("dispatch: tier %d (%s:%s) %s: exit %d", tierIdx, tier.Backend, tier.Model, class.String(), result.ExitCode)
		}
	}

	// Chain exhausted — every tier recorded a non-success Attempt.
	return Response{FallbackChain: chain}, fmt.Errorf("dispatch: role %q: %w", params.Role, ErrChainExhausted)
}

// buildSuccessResponse populates Response from a successful claude-native
// spawn result. Extracted from Dispatch's main body so the success path is
// inspectable in isolation; the dispatch loop only owns control flow + the
// FallbackChain accumulator.
func buildSuccessResponse(params Params, tier chains.Tier, tierIdx int, result claudeResult, chain []Attempt) (Response, error) {
	env, err := ParseEnvelope(result.Stdout)
	if err != nil {
		return Response{FallbackChain: chain}, fmt.Errorf("dispatch: parse envelope for role %q: %w", params.Role, err)
	}

	durationMs := int64(env.DurationMS)
	if durationMs == 0 {
		durationMs = result.DurationMs
	}

	return Response{
		Result:            env.Result,
		ServedBy:          tier.Backend + ":" + tier.Model,
		Tier:              tierIdx,
		Fallback:          tierIdx > 1,
		DurationMs:        durationMs,
		CostUSD:           env.TotalCostUSD,
		Tokens:            tokensFromEnvelope(env.Usage),
		ToolsUsed:         toolUsesFromMap(env.ToolsUsed),
		PermissionDenials: permissionDenialsFromMap(env.PermissionDenials),
		FallbackChain:     chain,
	}, nil
}

// summarizeStderr collapses the captured stderr to a single trimmed line
// suitable for Attempt.Reason. The TOON encoder renders the reason column
// inline, so multi-line stderr would break the table layout; we take the
// first non-empty line (or fall back to the whole trimmed text when stderr
// is single-line).
func summarizeStderr(stderr []byte) string {
	trimmed := strings.TrimSpace(string(stderr))
	if trimmed == "" {
		return ""
	}
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return trimmed
}

// tokensFromEnvelope copies the claude CLI Usage block into the dispatch
// Tokens shape declared in this package. The two structs are intentionally
// not the same type — Envelope.Usage mirrors the on-wire JSON key names while
// Response.Tokens is the sand-facing field set surfaced into TOON.
func tokensFromEnvelope(u Usage) Tokens {
	return Tokens{
		Input:         u.InputTokens,
		Output:        u.OutputTokens,
		CacheRead:     u.CacheReadTokens,
		CacheCreation: u.CacheCreationTokens,
	}
}

// toolUsesFromMap converts the envelope's name->count aggregate into the
// tabular []ToolUse shape Response carries into the TOON encoder. Rows are
// emitted in deterministic name-ascending order so tests can assert exact
// output without sorting downstream and so the TOON serialization is stable
// across runs. A nil or empty input yields a non-nil zero-length slice.
func toolUsesFromMap(m map[string]int) []ToolUse {
	out := make([]ToolUse, 0, len(m))
	for name, count := range m {
		out = append(out, ToolUse{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// permissionDenialsFromMap converts the envelope's tool->count aggregate into
// the tabular []PermissionDenial shape Response carries into the TOON encoder.
// Same ordering / nil-handling contract as toolUsesFromMap.
func permissionDenialsFromMap(m map[string]int) []PermissionDenial {
	out := make([]PermissionDenial, 0, len(m))
	for tool, count := range m {
		out = append(out, PermissionDenial{Tool: tool, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// Params is the typed input to Dispatch. It mirrors the `sand.dispatch` MCP
// tool schema in SAND-SPEC §3.1: role and prompt are required, the rest are
// optional. The MCP tool wiring (cmd/sand/main.go) decodes the MCP request
// into this struct before calling Dispatch.
//
// Field semantics:
//   - Role names the persona / chain entry to dispatch (e.g. "ta-go-builder").
//     It selects both the persona file (under CWD/.claude/agents/<role>.md)
//     and the chain config entry that orders backend tiers.
//   - Prompt is the task prompt forwarded to the spawned agent. SAND-SPEC §1.4
//     calls out that prompt arrives as a string argument by design — sand
//     does NOT accept a prompt-file path (the antipattern the spec retires).
//   - CWD is the working directory for the dispatched agent. When empty,
//     callers (cmd/sand wiring) default to the sand server's project dir.
//     This is also the root sand reads the caller's .mcp.json and persona
//     files from.
//   - ModelOverride, when non-empty, replaces the tier-1 model only for this
//     one dispatch (e.g. "qwen3-coder:30b"). Subsequent fallback tiers are
//     untouched. Empty means "use the chain's configured tier-1 model".
//   - DryRun, when true, instructs Dispatch to render the would-be command +
//     persona summary + mcp-config path as a TOON-encoded Response and skip
//     the actual backend spawn entirely.
type Params struct {
	Role          string
	Prompt        string
	CWD           string
	ModelOverride string
	DryRun        bool
}

// Response is the typed output of Dispatch. The cmd/sand wiring encodes it as
// TOON before returning the MCP response body to the orchestrator (see
// SAND-SPEC §3.1 and §4 for the on-wire shape).
//
// Field semantics:
//   - Result is the agent's final text response (the `.result` field of the
//     Claude Code JSON envelope, or the contiguous body block of the codex
//     stream once that backend lands). Free-form, frequently multi-line.
//   - ServedBy identifies the chain entry that produced Result, in
//     "<backend>:<model>" form (e.g. "claude-native:opus").
//   - Tier is the 1-indexed position of the served-by entry in the role's
//     fallback chain.
//   - Fallback is true when Tier > 1 — the primary tier did not serve.
//   - DurationMs is the wall-clock duration of the served-by spawn in
//     milliseconds.
//   - CostUSD is the dispatched agent's reported cost in USD; zero when the
//     backend does not surface cost data.
//   - Tokens is the per-dispatch token-usage aggregate.
//   - ToolsUsed is a tabular aggregate of tool-name => invocation-count
//     extracted from PARSED EVENTS in the dispatched agent's output, never
//     from the agent's narrative claims. SAND-SPEC §3.1 calls this out
//     explicitly because dispatched agents have been observed to claim tool
//     usage they did not perform.
//   - PermissionDenials is a tabular aggregate of permission-denial events,
//     same sourcing rule as ToolsUsed.
//   - FallbackChain is the per-tier audit trail of the dispatch. Every
//     attempted tier (including the successful one) contributes one row.
//     Empty on dry-run and on selection errors (chain config / persona /
//     role-not-in-chain) where no tier was attempted.
//   - ToolCalls is the ordered per-call breakdown of the dispatched agent's
//     tool invocations. drop_007 wires it from the full envelope stream;
//     drop_008 introduces the field on Response so downstream TOON encoders
//     can shape the row layout now.
//   - LogPath is the absolute path to the per-dispatch log file in
//     /tmp/sand-dispatch/log/<uuid>.json; the file is the source of truth for
//     deep inspection.
type Response struct {
	Result            string
	ServedBy          string
	Tier              int
	Fallback          bool
	DurationMs        int64
	CostUSD           float64
	Tokens            Tokens
	ToolsUsed         []ToolUse
	PermissionDenials []PermissionDenial
	FallbackChain     []Attempt
	ToolCalls         []ToolCall
	LogPath           string
}

// Attempt is one row of Response.FallbackChain. Each row records the
// per-tier outcome — backend, model, attempt timestamp, outcome class, and
// a one-line human reason — so callers (and the TOON encoder) can render the
// full chain walk per SAND-V02-SPEC §4. Outcome values mirror ErrClass.String()
// plus the slot-only "slot_timeout" and "unsupported_backend" values that
// classification does not produce.
type Attempt struct {
	Tier        int
	Backend     string
	Model       string
	AttemptedAt time.Time
	Outcome     string
	Reason      string
}

// ToolCall is one row of Response.ToolCalls. drop_007 populates this from
// the dispatched agent's full event stream; drop_008 introduces the type so
// downstream TOON encoders and Response consumers can compile against the
// final shape now. The Index field is the 1-based call order so callers can
// reconstruct the sequence regardless of slice-mutation downstream.
type ToolCall struct {
	Index      int
	Name       string
	DurationMs int64
	IsError    bool
}

// Tokens is the per-dispatch token-usage aggregate. Field names mirror the
// SAND-SPEC §3.1 TOON layout (input / output / cache_read / cache_creation).
type Tokens struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
}

// ToolUse is one row of the Response.ToolsUsed tabular aggregate. SAND-SPEC
// §3.1 renders rows as bare CSV under a `tools_used[N]{name,count}:` header.
type ToolUse struct {
	Name  string
	Count int
}

// PermissionDenial is one row of the Response.PermissionDenials tabular
// aggregate. SAND-SPEC §3.1 renders rows as bare CSV under a
// `permission_denials[N]{tool,count}:` header.
type PermissionDenial struct {
	Tool  string
	Count int
}

// Package-level sentinel errors. Sibling droplets wrap these with %w when
// signalling specific failure conditions; callers use errors.Is to match
// regardless of wrapping depth.
var (
	// ErrUnsupportedBackend is returned (wrapped) when the chain's first
	// reachable tier names a backend sand does not yet support. drop_003
	// implements the claude-native backend only; tiers naming "ollama-local"
	// or "codex-exec" surface as ErrUnsupportedBackend with the offending
	// backend in the wrapped message. Drops 004 (ollama) and 005 (codex)
	// will narrow this sentinel's scope as those backends land.
	ErrUnsupportedBackend = errors.New("dispatch: unsupported backend")

	// ErrRoleNotInChains is returned (wrapped) when Params.Role does not
	// appear in the loaded chain config. It is distinct from
	// chains.ErrUnknownRole at the dispatch boundary so callers can match
	// the dispatch-level failure mode without importing the chains package.
	ErrRoleNotInChains = errors.New("dispatch: role not present in chain config")

	// ErrNoClaudeNativeTier is returned (wrapped) when the role's chain
	// contains zero claude-native tiers. drop_003 only knows how to spawn
	// claude-native; a chain made entirely of ollama-local + codex-exec
	// tiers has nothing for the current implementation to dispatch to.
	// Distinct from ErrUnsupportedBackend so callers can tell "this role
	// will never serve under drop_003" from "the current tier names a
	// backend we don't support yet".
	ErrNoClaudeNativeTier = errors.New("dispatch: chain contains no claude-native tier")

	// ErrChainExhausted is returned (wrapped) when every tier in a role's
	// fallback chain produced a non-success Attempt. Per SAND-V02-SPEC §1.4
	// it signals "this dispatch had no winner" — distinct from a single
	// unrecoverable spawn failure (which halts the chain mid-walk with the
	// underlying error) because exhaustion means the caller-supplied chain
	// itself was insufficient for this role.
	ErrChainExhausted = errors.New("dispatch: chain exhausted; every tier failed")
)
