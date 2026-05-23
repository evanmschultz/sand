// Package backends additionally defines the Backend contract — the
// interface every concrete spawn implementation satisfies — plus the
// BackendConfig TOML shape and the Resolve factory that ties a backend
// name to a Backend instance.
//
// This file is the v0.2 §5.1 contract surface. The CONCRETE Backend
// implementations (claudeNativeBackend, codexExecBackend, etc.) live in
// sibling files in this package; the dispatch package consumes them via
// the Backend interface only, preventing an import cycle between
// backends ↔ dispatch.
package backends

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// ErrUnknownBackend signals a missing config ENTRY (the backends.toml
// file loaded fine, but no `[backends.<name>]` table exists for the
// requested name). It is deliberately distinct from the sibling
// ErrBackendsConfigNotFound from resolve.go, which signals a missing
// config FILE. Callers should match it via errors.Is so wrapping with
// %w stays transparent.
var ErrUnknownBackend = errors.New("backends: unknown backend")

// Backend is the contract every concrete spawn implementation satisfies.
// All three methods are MANDATORY for every Backend.
//
// Spawn runs the backend's subprocess for a given dispatch and returns
// the captured result. The context governs cancellation + deadlines for
// the whole spawn lifecycle (process start through exit).
//
// Preview renders the would-be argv + env + stdin shape as a
// human-readable string WITHOUT spawning a subprocess. It is the
// dry-run surface used by the `preflight` MCP tool and by `--dry-run`
// dispatches per SAND-V02-SPEC §8.3.
//
// EnvelopeFormat declares the backend's stdout dialect ("claude_json",
// "codex_stream", ...) so the dispatch package can pick the right
// envelope parser at the type level without re-reading BackendConfig.
// Added by drop_005 L3 amendment B3 to widen parser routing without a
// Resolve signature change.
type Backend interface {
	Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error)
	Preview(req SpawnRequest) (string, error)
	EnvelopeFormat() string
}

// SpawnRequest is the backend-local input shape every Backend.Spawn /
// Backend.Preview call accepts.
//
// This type is deliberately local to the backends package (rather than
// re-using a dispatch-package result type) so backends can be consumed
// by dispatch without creating an import cycle backends ↔ dispatch.
//
// Field semantics:
//
//   - PersonaBody is the loaded persona's system-prompt body (markdown
//     after the YAML frontmatter), typically injected via
//     `--append-system-prompt`.
//   - PersonaToolsCSV is the persona's Tools slice joined with `,`,
//     typically injected via `--allowedTools`.
//   - Prompt is the dispatch prompt string. Backends with
//     stdin_prompt=true pipe this to the subprocess stdin; others may
//     pass it via a `{{prompt}}` template substitution.
//   - McpConfigPath is the caller project's `.mcp.json` path when it
//     exists, otherwise empty. Backends with mcp_config_arg set append
//     it as a flag; codex-style backends ignore it (they inject MCP
//     inline via -c flags).
//   - Model is the chain tier's model identifier AFTER any per-dispatch
//     override has been applied (the spawn implementation does not
//     re-resolve overrides — that's the dispatcher's job).
//   - CWD is the caller project's absolute path — passed to the spawned
//     subprocess as its working directory and available to templates
//     for backends like codex that take `-C <cwd>` as a flag.
//   - Role is the persona role identifier (e.g. "ta-go-builder").
//     Useful for logging and for backends that tag spawns by role.
//   - Env is a passthrough environment-variable slice in `KEY=VAL`
//     form. The concrete spawn implementation is responsible for
//     applying any backend-level env-filter rules (see drop_011's
//     filterEnv design); the dispatcher passes this raw and the
//     backend decides what to forward.
type SpawnRequest struct {
	PersonaBody     string
	PersonaToolsCSV string
	Prompt          string
	McpConfigPath   string
	Model           string
	CWD             string
	Role            string
	Env             []string
}

// SpawnResult is the backend-local output shape every Backend.Spawn
// call returns on success (errors return the zero value plus a non-nil
// error).
//
// Stdout + Stderr are captured raw — the dispatcher's envelope parser
// (claude_json / codex_stream / etc., keyed by BackendConfig.EnvelopeFormat)
// is responsible for any structural decoding.
//
// ExitCode is the subprocess's exit code; 0 == success for most
// backends but the dispatcher's error classifier (SAND-V02-SPEC §3)
// inspects stderr text in addition to exit code to assign an ErrClass.
//
// Duration is wall-clock time from spawn start to process exit. It
// feeds the TOON response's `duration_ms` field.
type SpawnResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// BackendConfig is the TOML shape for a single `[backends.<name>]`
// table per SAND-V02-SPEC §5.1. All 10 fields are declared verbatim
// per the drop_011 L3 planner amendment so future backends
// (together-ai, openrouter, etc.) can be added without touching Go.
//
// Field semantics:
//
//   - Command is the executable name (e.g. "claude", "codex"). Resolved
//     against PATH at spawn time.
//   - Args is the argv slice; each element is independently rendered
//     through the templating engine (see template.go SubstituteSlice).
//   - Env is a slice of `KEY=VALUE` strings; values may contain
//     `{{env:VAR}}` substitutions resolved at spawn time.
//   - McpConfigArg, if non-empty, is the flag name (e.g. "--mcp-config")
//     appended along with the resolved mcp.json path when the caller
//     project has one. Empty value means this backend does not consume
//     `--mcp-config`-style flags (codex falls into this bucket — it
//     uses McpInjection instead).
//   - AllowedToolsArg is the flag name (e.g. "--allowedTools") used to
//     pass the persona's tool allowlist.
//   - AllowedToolsCSVTemplate is the template string rendered to
//     produce the value paired with AllowedToolsArg. Typically
//     `{{persona_tools_csv}}` for claude-style backends.
//   - SlotsDefault is the default slot count for this backend when a
//     chain tier omits the `slots` field. 0 means unlimited.
//   - EnvelopeFormat selects the dispatcher's output parser:
//     `claude_json` for Claude Code CLI output, `codex_stream` for
//     codex's line-oriented stream. Future formats land here.
//   - StdinPrompt controls how the dispatch prompt is delivered: true
//     pipes it to the subprocess stdin (default); false relies on a
//     `{{prompt}}` template substitution in Args.
//   - McpInjection is reserved for the codex-style "inline TOML"
//     injection mode (special-cased per
//     `~/.claude/codex-mcp-dispatch-tool-conversion.md`). drop_011
//     declares the field for round-trip safety; drop_005 consumes it.
type BackendConfig struct {
	Command                 string   `toml:"command"`
	Args                    []string `toml:"args"`
	Env                     []string `toml:"env"`
	McpConfigArg            string   `toml:"mcp_config_arg"`
	AllowedToolsArg         string   `toml:"allowed_tools_arg"`
	AllowedToolsCSVTemplate string   `toml:"allowed_tools_csv_template"`
	SlotsDefault            int      `toml:"slots_default"`
	EnvelopeFormat          string   `toml:"envelope_format"`
	StdinPrompt             bool     `toml:"stdin_prompt"`
	McpInjection            string   `toml:"mcp_injection"`
}

// backendsFile is the top-level TOML wrapper used solely by the
// BurntSushi/toml decoder. It corresponds to a file whose contents are
// laid out as `[backends.<name>]` tables — the decoder collects them
// into the Backends map.
type backendsFile struct {
	Backends map[string]BackendConfig `toml:"backends"`
}

// Resolve looks up the backend named `name` in the resolved
// backends.toml file for `projectDir` and returns a Backend instance
// ready to Spawn / Preview.
//
// Resolution order for the config file mirrors chains.Resolve
// (project override first, then XDG → home-config → home-dotfile).
// See resolve.go's ResolveBackendsConfig for the canonical hierarchy.
//
// Error contract:
//
//   - If no backends.toml file exists at any rung, the returned error
//     satisfies errors.Is(err, ErrBackendsConfigNotFound) — the
//     sentinel bubbles up from ResolveBackendsConfig unchanged.
//   - If the file exists but cannot be read or decoded, the returned
//     error wraps the underlying I/O or toml error with %w.
//   - If the file decoded but contains no `[backends.<name>]` table
//     for the requested name, the returned error satisfies
//     errors.Is(err, ErrUnknownBackend) and the name is included in
//     the wrapped message for diagnostic context.
//
// On success, Resolve returns the concrete Backend implementation that
// matches `name`. drop_011 ships only the claudeNativeBackend stub;
// drops 004 / 005 will widen the switch to cover ollama-local /
// ollama-cloud / codex-exec.
func Resolve(projectDir, name string) (Backend, error) {
	path, _, err := ResolveBackendsConfig(projectDir)
	if err != nil {
		// Bubble up unchanged so callers can errors.Is against
		// ErrBackendsConfigNotFound without re-wrapping.
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backends: read %s: %w", path, err)
	}

	var file backendsFile
	if _, decodeErr := toml.Decode(string(raw), &file); decodeErr != nil {
		return nil, fmt.Errorf("backends: decode %s: %w", path, decodeErr)
	}

	cfg, ok := file.Backends[name]
	if !ok {
		return nil, fmt.Errorf("backends: %q: %w", name, ErrUnknownBackend)
	}

	// Dispatch by EnvelopeFormat. claudeNativeBackend handles every backend
	// whose CLI emits claude -p --output-format json envelopes (claude
	// itself + ollama-local + ollama-cloud + together-ai + any other
	// provider routed through claude with ANTHROPIC_BASE_URL).
	// codexExecBackend handles backends whose CLI emits codex's
	// line-oriented stream (`mcp: <server>/<tool> (completed)` log lines).
	switch cfg.EnvelopeFormat {
	case "claude_json", "":
		return &claudeNativeBackend{cfg: cfg}, nil
	case "codex_stream":
		return &codexExecBackend{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf(
			"backends: %q: envelope_format=%q not yet supported: %w",
			name, cfg.EnvelopeFormat, ErrUnsupportedEnvelopeFormat,
		)
	}
}

// ErrUnsupportedEnvelopeFormat is returned by Resolve when the configured
// envelope_format has no Backend impl yet. drop_011 shipped claude_json;
// drop_005 added codex_stream. Future formats (e.g. together_ai_stream)
// remain ErrUnsupportedEnvelopeFormat until their Backend impl lands.
// Callers (dispatch loop) treat this the same as ErrUnknownBackend —
// advance to the next tier.
var ErrUnsupportedEnvelopeFormat = errors.New("backends: envelope_format not supported")
