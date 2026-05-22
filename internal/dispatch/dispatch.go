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

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
)

// backendClaudeNative is the chains.Tier.Backend value sand's drop_003
// claude-native dispatch path recognizes. Hoisted to a constant so callers
// can compare without re-typing the literal.
const backendClaudeNative = "claude-native"

// chainsConfigRelPath is the caller-project-relative path to sand's chain
// configuration file. SAND-SPEC §5 fixes the filename at
// `.claude/sand-chains.toml`; sand reads it from the caller project tree on
// every dispatch so multiple projects can carry distinct chains.
var chainsConfigRelPath = filepath.Join(".claude", "sand-chains.toml")

// loadChainsConfig reads and parses the caller project's chain config from
// `<cwd>/.claude/sand-chains.toml`. The returned Config is the parsed result
// of chains.Parse; errors wrap the underlying I/O or parse failure with %w so
// callers can use errors.Is for classification (notably os.ErrNotExist for the
// "no chains file in this project" case).
//
// loadChainsConfig is a small private seam — it exists so Dispatch keeps its
// caller-config resolution alongside the existing resolveMCPConfig helper
// rather than reaching directly into chains.Parse from a deeper call site.
func loadChainsConfig(cwd string) (chains.Config, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return chains.Config{}, fmt.Errorf("dispatch: resolve chains config cwd %q: %w", cwd, err)
	}

	path := filepath.Join(absCwd, chainsConfigRelPath)
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

// Dispatch is the public entry point for sand's `sand.dispatch` MCP tool.
//
// drop_003 implements the dry-run-only path: when params.DryRun is true,
// Dispatch loads the caller persona, loads the caller's chain config, selects
// the first claude-native tier in the role's chain, resolves the caller's
// optional .mcp.json, and returns a Response whose Result carries the
// would-be claude command shape. The real backend spawn (runClaudeNative)
// is intentionally NOT invoked in dry-run mode — the wet-run path lands in a
// later droplet.
//
// Failure modes (each wraps the underlying cause with %w so callers may use
// errors.Is):
//
//   - persona load failure (missing file, malformed frontmatter) propagates
//     from persona.Load.
//   - chains config load / parse failure propagates from loadChainsConfig.
//   - role not in chain config returns a wrapped ErrRoleNotInChains.
//   - role chain contains zero claude-native tiers returns a wrapped
//     ErrNoClaudeNativeTier.
//   - MCP config resolution errors propagate from resolveMCPConfig (missing
//     .mcp.json is NOT an error — it surfaces as exists=false and Dispatch
//     simply omits --mcp-config from the rendered command).
//
// The ctx parameter is accepted for future symmetry with the wet-run path; in
// dry-run mode it is honored only as a cancellation gate before any I/O.
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

	tier, tierIdx, err := selectClaudeNativeTier(params.Role, tiers)
	if err != nil {
		return Response{}, err
	}

	model := tier.Model
	if params.ModelOverride != "" {
		model = params.ModelOverride
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
		return Response{
			Result:   renderDryRunCommand(params.Prompt, p, model, renderedMCPPath),
			ServedBy: backendClaudeNative + ":" + model,
			Tier:     0,
		}, nil
	}

	// Wet-run path: spawn the claude-native backend, parse the JSON envelope
	// it emits, and populate Response strictly from PARSED EVENTS (per memory
	// note feedback_always_verify_tool_calls — agent narrative is never the
	// source of tool-use or permission-denial counts).
	//
	// tier.Model is replaced by params.ModelOverride for the spawn so the
	// rendered argv and ServedBy stay in lockstep.
	spawnTier := tier
	spawnTier.Model = model

	result, err := runClaudeNative(ctx, params, p, spawnTier, renderedMCPPath)
	if err != nil {
		return Response{}, fmt.Errorf("dispatch: spawn claude-native for role %q: %w", params.Role, err)
	}

	env, err := ParseEnvelope(result.Stdout)
	if err != nil {
		return Response{}, fmt.Errorf("dispatch: parse envelope for role %q: %w", params.Role, err)
	}

	durationMs := int64(env.DurationMS)
	if durationMs == 0 {
		durationMs = result.DurationMs
	}

	return Response{
		Result:            env.Result,
		ServedBy:          backendClaudeNative + ":" + model,
		Tier:              tierIdx,
		Fallback:          tierIdx > 1,
		DurationMs:        durationMs,
		CostUSD:           env.TotalCostUSD,
		Tokens:            tokensFromEnvelope(env.Usage),
		ToolsUsed:         toolUsesFromMap(env.ToolsUsed),
		PermissionDenials: permissionDenialsFromMap(env.PermissionDenials),
	}, nil
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
	LogPath           string
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
)
