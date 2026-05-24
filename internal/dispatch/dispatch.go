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

	"github.com/evanmschultz/sand/internal/backends"
	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
	"github.com/evanmschultz/sand/internal/slots"
)

// backendClaudeNative is the chains.Tier.Backend value sand's drop_003
// claude-native dispatch path recognizes. Hoisted to a constant so callers
// can compare without re-typing the literal.
const backendClaudeNative = "claude-native"

// loadChainsConfig resolves the caller's chain config via the hierarchical
// rules (project → XDG → $HOME/.config → $HOME/.sand — see chains.Resolve)
// and parses the winning file.
//
// When no config exists on any rung, chains.Resolve returns
// ErrChainConfigNotFound; loadChainsConfig surfaces an error that satisfies
// BOTH errors.Is(err, chains.ErrChainConfigNotFound) AND
// errors.Is(err, os.ErrNotExist). The dual-target wrap is intentional: the
// drop_003 tests pin os.ErrNotExist for the "no chains config" case and we
// keep that contract intact alongside the hierarchical resolver.
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
			// Preserve drop_003-era os.ErrNotExist contract AND the
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

// nowFn is the package-level clock seam Dispatch uses to stamp
// Attempt.AttemptedAt. Tests may override it to make per-attempt timestamps
// deterministic; production keeps it pointed at time.Now.
var nowFn = time.Now

// Dispatch is the public entry point for sand's `sand.dispatch` MCP tool.
//
// Per SAND-SPEC §1.4, Dispatch walks the role's fallback chain tier-by-tier:
// for each tier it (a) optionally acquires a cross-project slot via
// slots.AcquireSlot when tier.Slots > 0, (b) resolves the backend via
// backends.Resolve and Spawns it — non-claude-native tiers short-circuit with
// Attempt{Outcome:"unsupported_backend"} until drops 004/005 land their
// implementations — (c) classifies the outcome via ClassifyExitError, and
// (d) records an Attempt row in Response.FallbackChain regardless of success
// or failure.
//
// Outcome policy (mirrors SAND-SPEC §1.4 + §3.3):
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
//   - role chain contains zero claude-native tiers in DRY-RUN mode returns
//     a wrapped ErrNoClaudeNativeTier. Wet-run no longer applies this
//     guard pre-loop (drop_005 L3 amendment B4); codex-only or
//     ollama-only chains are gated per-tier by backends.Resolve and
//     surface as ErrChainExhausted with FallbackChain rows recording
//     "unsupported_backend" outcomes when no tier resolves.
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
		//
		// The selectClaudeNativeTier guard lives HERE (drop_005 L3
		// amendment B4): wet-run no longer pre-rejects codex-only chains
		// — backends.Resolve gates support per-tier in the loop below.
		// Dry-run still picks the first claude-native tier so the Preview
		// rendering stays deterministic and the existing
		// TestDispatchSelectionErrors/no-claude-native-tier contract is
		// preserved.
		//
		// Per drop_011 amendment A5, the rendered command MUST preserve
		// renderDryRunCommand's byte shape; the claudeNativeBackend.Preview
		// implementation matches that contract bit-for-bit. Resolution
		// goes through backends.Resolve so the dry-run path exercises the
		// same factory as wet-run.
		cnTier, _, err := selectClaudeNativeTier(params.Role, tiers)
		if err != nil {
			return Response{}, err
		}
		dryRunModel := cnTier.Model
		if params.ModelOverride != "" {
			dryRunModel = params.ModelOverride
		}
		backend, err := backends.Resolve(params.CWD, backendClaudeNative)
		if err != nil {
			return Response{}, fmt.Errorf("dispatch: resolve %s backend for dry-run: %w", backendClaudeNative, err)
		}
		preview, err := backend.Preview(buildSpawnRequest(params, p, dryRunModel, renderedMCPPath))
		if err != nil {
			return Response{}, fmt.Errorf("dispatch: preview %s backend: %w", backendClaudeNative, err)
		}
		return Response{
			Result:   preview,
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
			Model:       tier.Model, // A6: record CONFIGURED model, not override.
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

		// post-drop_011 short-circuit lift: dispatch defers backend support
		// decisions to backends.Resolve. Any tier whose name resolves to a
		// Backend impl spawns; any resolve error (ErrUnknownBackend for
		// missing config entry, ErrUnsupportedEnvelopeFormat for not-yet-
		// implemented backend types like codex_stream until drop_005)
		// records Attempt{Outcome:"unsupported_backend"} + advances.
		backend, resolveErr := backends.Resolve(params.CWD, tier.Backend)
		if resolveErr != nil {
			// A3: ErrUnknownBackend (config-entry missing) classifies as
			// "unsupported_backend" + advance. Any other resolve error
			// (e.g. backends.toml not found, decode failure) also advances
			// under the same outcome — the chain may have another tier
			// that resolves correctly.
			if slot != nil {
				slot.Release()
			}
			attempt.Outcome = "unsupported_backend"
			attempt.Reason = resolveErr.Error()
			chain = append(chain, attempt)
			continue
		}

		// Model override replaces the served tier's model in the spawn
		// argv ONLY. Attempt.Model already recorded the CONFIGURED tier
		// model above (A6).
		spawnModel := tier.Model
		if params.ModelOverride != "" {
			spawnModel = params.ModelOverride
		}

		req := buildSpawnRequest(params, p, spawnModel, renderedMCPPath)
		spawnResult, runErr := backend.Spawn(ctx, req)

		// Per-tier slot release: must happen BEFORE we continue or return.
		// defer would batch all releases until function return, which is
		// wrong — we need the next tier (or test) to see freed slots
		// immediately.
		if slot != nil {
			slot.Release()
		}

		if runErr != nil {
			// Spawn-level failure (e.g. claude binary not on PATH, ctx
			// cancelled). We treat ctx.Canceled / DeadlineExceeded
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
		// is NOT a Go error at the backend's layer — it lives in
		// SpawnResult.ExitCode and Stderr.
		class := ClassifyExitError(spawnResult.Stderr, spawnResult.ExitCode)
		if class == ErrClassSuccess {
			attempt.Outcome = class.String()
			chain = append(chain, attempt)
			return buildSuccessResponse(params, backend, tier, spawnModel, tierIdx, spawnResult, chain)
		}

		// drop_009 user-configurable retry policy: when the tier opts in
		// via retry_on, the whitelist supersedes the default outcome→
		// action switch below. Membership = advance; non-membership =
		// halt — INCLUDING outcomes the default policy would have
		// advanced (rate_limit, auth_failure, network, timeout). When
		// the tier did not opt in (RetryOn empty), ShouldRetry returns
		// hasOpinion=false and we fall through to the default switch.
		outcomeStr := class.String()
		if advance, hasOpinion := tier.ShouldRetry(outcomeStr); hasOpinion {
			attempt.Outcome = outcomeStr
			attempt.Reason = summarizeStderr(spawnResult.Stderr)
			chain = append(chain, attempt)
			if advance {
				continue
			}
			return Response{FallbackChain: chain}, fmt.Errorf("dispatch: tier %d (%s:%s) %s halted by retry_on policy: exit %d", tierIdx, tier.Backend, tier.Model, outcomeStr, spawnResult.ExitCode)
		}

		switch class {
		case ErrClassRateLimit, ErrClassAuthFailure, ErrClassNetwork, ErrClassTimeout:
			attempt.Outcome = class.String()
			attempt.Reason = summarizeStderr(spawnResult.Stderr)
			chain = append(chain, attempt)
			continue
		default:
			// ErrClassCrash / ErrClassUnknown — unrecoverable per
			// SAND-SPEC §3.3. Record + halt with FallbackChain
			// preserved.
			attempt.Outcome = class.String()
			attempt.Reason = summarizeStderr(spawnResult.Stderr)
			chain = append(chain, attempt)
			return Response{FallbackChain: chain}, fmt.Errorf("dispatch: tier %d (%s:%s) %s: exit %d", tierIdx, tier.Backend, tier.Model, class.String(), spawnResult.ExitCode)
		}
	}

	// Chain exhausted — every tier recorded a non-success Attempt.
	return Response{FallbackChain: chain}, fmt.Errorf("dispatch: role %q: %w", params.Role, ErrChainExhausted)
}

// buildSpawnRequest constructs the per-tier backends.SpawnRequest from the
// dispatch-level inputs. Centralised so dry-run + wet-run share the same
// shape; the only per-call difference is the resolved Model (override-aware
// for both paths).
//
// Persona Tools are joined into the persona_tools_csv field via comma; the
// claude-native backend's AllowedToolsCSVTemplate then renders it into
// `--allowedTools` as a CSV. An empty Tools slice yields an empty CSV,
// which the backend's conditional append elides.
func buildSpawnRequest(params Params, p persona.Persona, model, mcpConfigPath string) backends.SpawnRequest {
	return backends.SpawnRequest{
		PersonaBody:     p.Body,
		PersonaToolsCSV: strings.Join(p.Tools, ","),
		Prompt:          params.Prompt,
		McpConfigPath:   mcpConfigPath,
		Model:           model,
		CWD:             params.CWD,
		Role:            params.Role,
	}
}

// buildSuccessResponse populates Response from a successful backend spawn
// result. Extracted from Dispatch's main body so the success path is
// inspectable in isolation; the dispatch loop only owns control flow + the
// FallbackChain accumulator.
//
// servedModel is the model string surfaced in ServedBy — for the wet-run
// path this is the override-resolved model (so callers see the model the
// backend actually used). tier.Model is preserved unchanged in the
// FallbackChain rows per amendment A6.
//
// Parser routing per drop_005 L3 amendment B3: backend.EnvelopeFormat()
// selects the envelope decoder. "claude_json" (and the empty-string default
// for backward compat) calls ParseEnvelope; "codex_stream" calls
// ParseCodexEnvelope. Any other value is a programming error — the switch
// returns a wrapped ErrUnknownEnvelopeFormat so the dispatch boundary fails
// loudly rather than silently mis-parsing a new backend's stream.
func buildSuccessResponse(params Params, backend backends.Backend, tier chains.Tier, servedModel string, tierIdx int, result backends.SpawnResult, chain []Attempt) (Response, error) {
	var (
		env Envelope
		err error
	)
	switch format := backend.EnvelopeFormat(); format {
	case "claude_json", "":
		env, err = ParseEnvelope(result.Stdout)
	case "codex_stream":
		env, err = ParseCodexEnvelope(result.Stdout)
	default:
		return Response{FallbackChain: chain}, fmt.Errorf(
			"dispatch: role %q: envelope_format=%q: %w",
			params.Role, format, ErrUnknownEnvelopeFormat,
		)
	}
	if err != nil {
		return Response{FallbackChain: chain}, fmt.Errorf("dispatch: parse envelope for role %q: %w", params.Role, err)
	}

	durationMs := int64(env.DurationMS)
	if durationMs == 0 {
		durationMs = result.Duration.Milliseconds()
	}

	return Response{
		Result:            env.Result,
		ServedBy:          tier.Backend + ":" + servedModel,
		Tier:              tierIdx,
		Fallback:          tierIdx > 1,
		DurationMs:        durationMs,
		CostUSD:           env.TotalCostUSD,
		Tokens:            tokensFromEnvelope(env.Usage),
		ToolsUsed:         toolUsesFromMap(env.ToolsUsed),
		PermissionDenials: permissionDenialsFromMap(env.PermissionDenials),
		ToolCalls:         toolCallsFromOrdered(env.ToolCallsOrdered),
		FallbackChain:     chain,
	}, nil
}

// toolCallsFromOrdered copies the parser-level OrderedToolCall slice into the
// Response-level []ToolCall shape. The two types are intentionally distinct:
// OrderedToolCall lives on Envelope and carries only what either parser can
// actually observe today, while ToolCall mirrors the SAND-SPEC §3.1 TOON row
// layout (index / name / duration_ms / is_error).
//
// DurationMs is set to zero: neither the claude envelope's Iteration record
// nor the codex stream's `mcp:` log line surfaces a per-call duration. The
// gap is documented on Envelope.OrderedToolCall and on Response.ToolCall;
// when an upstream emitter starts publishing per-invocation timing this
// helper is the single point of update.
func toolCallsFromOrdered(in []OrderedToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(in))
	for _, c := range in {
		out = append(out, ToolCall{
			Index:      c.Index,
			Name:       c.Name,
			DurationMs: 0,
			IsError:    c.IsError,
		})
	}
	return out
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
// full chain walk per SAND-SPEC §4. Outcome values mirror ErrClass.String()
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
	// fallback chain produced a non-success Attempt. Per SAND-SPEC §1.4
	// it signals "this dispatch had no winner" — distinct from a single
	// unrecoverable spawn failure (which halts the chain mid-walk with the
	// underlying error) because exhaustion means the caller-supplied chain
	// itself was insufficient for this role.
	ErrChainExhausted = errors.New("dispatch: chain exhausted; every tier failed")

	// ErrUnknownEnvelopeFormat is returned (wrapped) by buildSuccessResponse
	// when the served Backend's EnvelopeFormat() value does not match any
	// known parser dispatch case. drop_005 ships claude_json (incl. the
	// empty-string default for backward compat) + codex_stream; any other
	// value reaches the default branch and surfaces this sentinel. The
	// guard is defensive — backends.Resolve already rejects unknown
	// envelope_format values via ErrUnsupportedEnvelopeFormat — but the
	// switch must be total at the dispatch boundary so a future Backend
	// impl that forgets to update buildSuccessResponse fails loudly.
	ErrUnknownEnvelopeFormat = errors.New("dispatch: unknown backend envelope format")
)
