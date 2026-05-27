// Package backends — claude-native concrete Backend implementation.
//
// This file owns claudeNativeBackend: the Backend impl that spawns the
// claude CLI via os/exec. It preserves the committed
// internal/dispatch/runClaudeNative behavior bit-for-bit per drop_011
// orchestrator amendment A5 — the anti-recursion suffix, the
// ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN env filtering, the prompt-via-
// stdin contract, the --append-system-prompt persona-body injection,
// optional --mcp-config + --allowedTools conditional appending, and
// non-zero-exit-is-data semantics all carry over unchanged.
//
// [DUAL-PATH AUTHENTICATION MODEL]
//
// Sand supports two distinct authentication paths:
//
//   - Built-in Agent tool (OAuth): Dispatches routed through the native
//     Agent tool inherit the parent session's OAuth context automatically.
//     No explicit ANTHROPIC_API_KEY is required; auth is ambient.
//   - Sand-spawned claude -p --bare (explicit API key): When sand dispatches
//     a role via `claude -p --bare`, the spawned process runs outside the
//     parent's OAuth context. It requires ANTHROPIC_API_KEY to be present
//     in the subprocess environment, drawn from either the host environment
//     passthrough or BackendConfig.Env template entries. See requireAPIKey
//     for the enforcement contract and ErrAPIKeyRequired for the sentinel.
//
// Construction is driven by BackendConfig (loaded by Resolve in
// backend.go); the templating engine in template.go renders Args / Env /
// AllowedToolsCSVTemplate entries against the per-dispatch TemplateData.
// The factory in backend.go binds one BackendConfig to one
// claudeNativeBackend instance.
package backends

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrAPIKeyRequired signals that ANTHROPIC_API_KEY is missing from the
// rendered subprocess environment when a sand-spawned claude -p --bare
// dispatch attempts to execute. This sentinel is scoped to the explicit-key
// path only: OAuth routes (such as dispatch through the built-in Agent tool)
// flow through the parent's OAuth context and do NOT trigger this error.
// When sand invokes claude -p --bare for a dispatch, requireAPIKey scans the
// rendered environment before exec; if the key is absent or whitespace-only,
// this error is wrapped and returned. Callers should match it via errors.Is
// so wrapping with %w stays transparent.
var ErrAPIKeyRequired = errors.New("backends: ANTHROPIC_API_KEY required for non-OAuth claude -p --bare dispatch")

// antiRecursionSuffix is appended to the persona body before it becomes
// the spawned agent's --append-system-prompt argument. Copied verbatim
// from internal/dispatch/claude.go's const so the spawned-agent message
// shape stays identical across both call paths during the dispatch-
// integration droplet that wires backends into dispatch.
//
// The leading `\n---\n` separator keeps the persona body's own markdown
// intact when concatenated; the spawned agent sees its own role's prompt
// followed by a clearly demarcated dispatch-context note.
const antiRecursionSuffix = "\n---\nDISPATCH CONTEXT: You are the %s agent, dispatched via sand. Execute the task below directly using YOUR role-appropriate tools (the orchestrator restricts them per the persona's `tools:` allowlist). Do NOT call sand.dispatch. Do NOT use the Agent tool to spawn other roles. Do NOT route the task elsewhere. You ARE the role. The orchestrator coordinates further dispatches."

// claudeNativeBackend is the Backend implementation for the
// `claude-native` provider. It binds one BackendConfig (the resolved
// `[backends.claude-native]` TOML table) to the Spawn / Preview methods
// the Backend interface declares.
//
// The struct is unexported because external callers always go through
// the Resolve factory in backend.go; the Backend interface is the
// public contract.
type claudeNativeBackend struct {
	cfg BackendConfig
}

// EnvelopeFormat returns "claude_json" per drop_005 L3 amendment B3.
//
// The Backend interface declares EnvelopeFormat() string so dispatch.go
// can route parser selection (claude_json → ParseEnvelope; codex_stream
// → ParseCodexEnvelope) without re-reading BackendConfig. This method
// lets the backend declare its envelope dialect at the type level. The
// returned literal mirrors the value Resolve switches on for routing
// to this backend (`"claude_json"` and the legacy empty-string default
// both land here).
func (b *claudeNativeBackend) EnvelopeFormat() string { return "claude_json" }

// Spawn runs the claude CLI for one dispatch and captures stdout +
// stderr + exit code + wall-clock duration into a SpawnResult.
//
// The argv is built by:
//
//  1. Resolving BackendConfig.Command on PATH (surfaces missing-binary
//     failures via a wrapped error so the caller can distinguish them
//     from a child non-zero exit).
//  2. Appending the anti-recursion suffix to req.PersonaBody.
//  3. Substituting BackendConfig.Args element-wise via the template
//     engine — TemplateData covers Model, CWD, PersonaBody (with
//     suffix), PersonaToolsCSV, McpConfigPath, Role. The `{{env "VAR"}}`
//     helper resolves against the process environment via os.Getenv.
//  4. Appending `--mcp-config <path>` (using BackendConfig.McpConfigArg)
//     iff req.McpConfigPath is non-empty AND BackendConfig.McpConfigArg
//     is non-empty.
//  5. Appending `--allowedTools <csv>` (using BackendConfig.AllowedToolsArg
//     + the substituted BackendConfig.AllowedToolsCSVTemplate) iff
//     req.PersonaToolsCSV is non-empty AND BackendConfig.AllowedToolsArg
//     is non-empty.
//
// Env handling:
//
//   - Process environment is filtered to strip ANTHROPIC_BASE_URL and
//     ANTHROPIC_AUTH_TOKEN (preserving the drop_003 contract that the
//     claude-native backend never sees the ollama-redirect env).
//   - BackendConfig.Env entries are then appended; each `KEY=VALUE` is
//     rendered through the template engine so `{{env "VAR"}}` inside
//     VALUE works for forwarding selected host env vars.
//   - After renderEnv completes, requireAPIKey scans the rendered env slice
//     for ANTHROPIC_API_KEY; if absent or whitespace-only, an error wrapping
//     ErrAPIKeyRequired is returned and exec never occurs. (Note: OAuth routes
//     dispatched via the built-in Agent tool inherit the parent's auth context
//     and do NOT reach this check; this sentinel guards explicit-key paths only.)
//
// Prompt handling: when BackendConfig.StdinPrompt is true, req.Prompt is
// piped to the child's stdin via a strings.Reader. When false, callers
// rely on a `{{prompt}}` substitution in Args — not yet supported by the
// template engine (TemplateData has no Prompt field) but the field is
// preserved for future drops.
//
// Context handling: ctx is forwarded to exec.CommandContext. When ctx
// fires before the child completes, the returned error wraps ctx.Err()
// so callers can use errors.Is(err, context.Canceled) /
// context.DeadlineExceeded.
//
// Non-zero exit is NOT an error condition at this layer — the dispatcher
// classifies via stderr + exit code. Spawn returns a Go error only when
// the binary cannot be resolved, when ctx fires, when an exec plumbing
// failure surfaces, or when a template render fails.
func (b *claudeNativeBackend) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	resolvedCmd, err := exec.LookPath(b.cfg.Command)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("backends: locate %s on PATH: %w", b.cfg.Command, err)
	}

	args, td, err := b.renderArgs(req)
	if err != nil {
		return SpawnResult{}, err
	}

	envOut, err := b.renderEnv(td)
	if err != nil {
		return SpawnResult{}, err
	}

	if err := requireAPIKey(envOut); err != nil {
		return SpawnResult{}, err
	}

	cmd := exec.CommandContext(ctx, resolvedCmd, args...)
	if b.cfg.StdinPrompt {
		cmd.Stdin = strings.NewReader(req.Prompt)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = envOut

	if req.CWD != "" {
		absCwd, absErr := filepath.Abs(req.CWD)
		if absErr != nil {
			return SpawnResult{}, fmt.Errorf("backends: resolve cwd %q: %w", req.CWD, absErr)
		}
		cmd.Dir = absCwd
	}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	result := SpawnResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCodeFromErr(runErr),
		Duration: elapsed,
	}

	// Context cancellation: surface ctx.Err() so callers using
	// errors.Is(err, context.Canceled) / context.DeadlineExceeded detect
	// the case regardless of how cmd.Run() chose to report it.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, fmt.Errorf("backends: claude spawn aborted: %w", ctxErr)
	}

	// Spawn-level plumbing failure (e.g. could not start the process) is
	// distinct from non-zero exit. exec.Error indicates the former; we
	// surface it so the caller doesn't try to parse an empty envelope.
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return result, fmt.Errorf("backends: start %s: %w", b.cfg.Command, runErr)
	}

	return result, nil
}

// Preview renders the would-be claude command shape without spawning a
// subprocess. The output preserves the committed
// internal/dispatch/renderDryRunCommand byte shape per drop_011
// amendment A5 — one argument per line, `--model <value>`
// space-separated (NOT `--model=value`), persona body via strconv-Quote-
// style escaping so embedded newlines stay on one rendered line, and a
// trailing `  <<< "<prompt>"\n` heredoc marker indicating stdin delivery.
//
// Conditional flags follow the same elision rules as Spawn: --mcp-config
// only when req.McpConfigPath + BackendConfig.McpConfigArg are both
// non-empty; --allowedTools only when req.PersonaToolsCSV +
// BackendConfig.AllowedToolsArg are both non-empty.
//
// Preview never executes the child — it is the dry-run surface used by
// the `preflight` MCP tool and by Dispatch(DryRun=true).
func (b *claudeNativeBackend) Preview(req SpawnRequest) (string, error) {
	args, _, err := b.renderArgs(req)
	if err != nil {
		return "", err
	}

	var out strings.Builder

	// First line: `<command> <first-arg>\n` (no leading indent). When
	// Args is empty (unusual but legal) we emit just the command name.
	out.WriteString(b.cfg.Command)
	idx := 0
	if len(args) > 0 {
		out.WriteByte(' ')
		out.WriteString(args[0])
		idx = 1
	}
	out.WriteByte('\n')

	// Remaining args: pair flag+value on one line where adjacent; emit
	// solo flags on their own line. A value containing newlines (the
	// persona body via --append-system-prompt is the load-bearing case)
	// is rendered via strconv-Quote-style escaping so the line stays
	// single-line.
	for idx < len(args) {
		token := args[idx]
		if isFlag(token) && idx+1 < len(args) && !isFlag(args[idx+1]) {
			value := args[idx+1]
			out.WriteString("  ")
			out.WriteString(token)
			out.WriteByte(' ')
			if strings.ContainsAny(value, "\n\r\t\"\\") {
				out.WriteString(quoteValue(value))
			} else {
				out.WriteString(value)
			}
			out.WriteByte('\n')
			idx += 2
			continue
		}
		out.WriteString("  ")
		out.WriteString(token)
		out.WriteByte('\n')
		idx++
	}

	// renderArgs already appended conditional --mcp-config + --allowedTools
	// pairs at the end of args, so the loop above emitted them. No further
	// conditional-flag handling needed here.

	out.WriteString("  <<< ")
	out.WriteString(quoteValue(req.Prompt))
	out.WriteByte('\n')

	return out.String(), nil
}

// renderArgs substitutes BackendConfig.Args element-wise and appends the
// conditional --mcp-config + --allowedTools pairs per Spawn's contract.
// Returns the rendered argv slice plus the TemplateData used (so callers
// like renderEnv can share the same substitution surface without
// re-deriving the persona-body-plus-suffix value).
//
// The persona body has the role-templated anti-recursion suffix appended
// BEFORE template substitution; downstream `{{.PersonaBody}}` references
// see the full text.
func (b *claudeNativeBackend) renderArgs(req SpawnRequest) ([]string, TemplateData, error) {
	personaBody := req.PersonaBody + fmt.Sprintf(antiRecursionSuffix, req.Role)

	td := TemplateData{
		Model:           req.Model,
		CWD:             req.CWD,
		PersonaBody:     personaBody,
		PersonaToolsCSV: req.PersonaToolsCSV,
		McpConfigPath:   req.McpConfigPath,
		Role:            req.Role,
	}

	args, err := SubstituteSlice(b.cfg.Args, td, os.Getenv)
	if err != nil {
		return nil, td, fmt.Errorf("backends: render args: %w", err)
	}

	// --mcp-config conditional append. The flag name comes from
	// BackendConfig.McpConfigArg; the value is the resolved path on
	// req. Both must be non-empty — backends with no McpConfigArg
	// configured (codex) ignore req.McpConfigPath; backends with no
	// req.McpConfigPath (caller has no .mcp.json) skip the flag.
	if req.McpConfigPath != "" && b.cfg.McpConfigArg != "" {
		args = append(args, b.cfg.McpConfigArg, req.McpConfigPath)
	}

	// Gate translation: when req.Gate is non-nil, translate the Allowlist into
	// claude -p tool flags per AGENT_SANDBOX_SPEC.md §3 + the bin oracle
	// (bin/agent-dispatch.sh dispatch_ollama).
	//
	// When a Gate is present, the --allowedTools value is built by prepending the
	// persona's base tools (rendered from PersonaToolsCSV) and appending scoped
	// Edit/Write/MultiEdit entries for each file in Gate.Edit. This ensures gated
	// builders retain their base read/test/MCP tool grants while having edit scope
	// confined to the declared files.
	//
	// When Gate.EditPresent is true, scoped entries are appended: a comma-separated
	// list of Edit(//abs), Write(//abs), MultiEdit(//abs) entries for each file
	// in Gate.Edit. The double-slash absolute form is required: single-slash
	// denies every edit. The oracle derives this as "//" + path-without-leading-slash
	// (${ef#/} in bash), so "/abs/foo.go" becomes "Edit(//abs/foo.go)".
	//
	// Bare "Bash" (without parens) is never added by the gate translation itself —
	// it would let the agent bypass the per-file edit gate via shell writes. However,
	// persona-declared Bash patterns like Bash(mage *) are preserved in the prepended
	// base tools.
	//
	// When Gate.BashDeny is non-empty, emit one --disallowedTools flag with a
	// comma-separated list of Bash(<deny>:*) entries; --disallow wins over
	// --allowedTools so this blocks even persona-granted Bash patterns.
	//
	// Nil Gate falls through to the PersonaToolsCSV path (ungated requests keep
	// their current behavior unchanged).
	if req.Gate != nil && b.cfg.AllowedToolsArg != "" {
		var parts []string
		if req.PersonaToolsCSV != "" {
			csv, csvErr := b.renderAllowedToolsCSV(req)
			if csvErr != nil {
				return nil, td, csvErr
			}
			if csv != "" {
				parts = append(parts, csv)
			}
		}
		if req.Gate.EditPresent {
			if scoped := gateAllowedToolsCSV(req.Gate.Edit); scoped != "" {
				parts = append(parts, scoped)
			}
		}
		if len(parts) > 0 {
			args = append(args, b.cfg.AllowedToolsArg, strings.Join(parts, ","))
		}
		if len(req.Gate.BashDeny) > 0 {
			args = append(args, "--disallowedTools", gateBashDisallowedCSV(req.Gate.BashDeny))
		}
	} else if req.PersonaToolsCSV != "" && b.cfg.AllowedToolsArg != "" {
		// --allowedTools conditional append. The flag name comes from
		// BackendConfig.AllowedToolsArg; the value is rendered from
		// BackendConfig.AllowedToolsCSVTemplate against td (typically just
		// `{{.PersonaToolsCSV}}` for claude-style backends but a backend
		// could template in a default-list or env-lookup if it wants).
		csv, csvErr := b.renderAllowedToolsCSV(req)
		if csvErr != nil {
			return nil, td, csvErr
		}
		args = append(args, b.cfg.AllowedToolsArg, csv)
	}

	return args, td, nil
}

// gateAllowedToolsCSV builds the comma-separated --allowedTools value for a
// gated claude -p dispatch. Each file in edit receives three scoped tool entries:
// Edit(//abs), Write(//abs), MultiEdit(//abs). The double-slash absolute form is
// the contract the claude CLI enforces (single-slash denies all edits). An empty
// edit list produces an empty string (read-only edit-scoped role — no file writes
// are permitted, so no allowedTools value is generated for edit scope).
//
// Bare "Bash" is never added here; including it would let an agent bypass the
// per-file gate via shell writes.
func gateAllowedToolsCSV(edit []string) string {
	if len(edit) == 0 {
		return ""
	}
	entries := make([]string, 0, len(edit)*3)
	for _, f := range edit {
		if f == "" {
			continue
		}
		// Strip the leading "/" so the "//" prefix produces the canonical form:
		// "/abs/foo.go" → "//abs/foo.go" → "Edit(//abs/foo.go)".
		rel := strings.TrimPrefix(f, "/")
		dbl := "//" + rel
		entries = append(entries, "Edit("+dbl+")", "Write("+dbl+")", "MultiEdit("+dbl+")")
	}
	return strings.Join(entries, ",")
}

// gateBashDisallowedCSV builds the space-separated --disallowedTools value for
// gate.BashDeny patterns. Each deny pattern (e.g. "git commit") becomes
// "Bash(git commit:*)" — the claude -p form that matches any bash call whose
// command begins with the deny pattern.
func gateBashDisallowedCSV(bashDeny []string) string {
	entries := make([]string, 0, len(bashDeny))
	for _, pat := range bashDeny {
		if pat == "" {
			continue
		}
		entries = append(entries, "Bash("+pat+":*)")
	}
	return strings.Join(entries, ",")
}

// renderAllowedToolsCSV substitutes BackendConfig.AllowedToolsCSVTemplate
// against the per-dispatch TemplateData. When the template is empty (the
// user wired AllowedToolsArg without a CSV template) we fall back to the
// raw req.PersonaToolsCSV value so the persona's tool allowlist still
// reaches the spawned child.
func (b *claudeNativeBackend) renderAllowedToolsCSV(req SpawnRequest) (string, error) {
	if b.cfg.AllowedToolsCSVTemplate == "" {
		return req.PersonaToolsCSV, nil
	}
	td := TemplateData{
		Model:           req.Model,
		CWD:             req.CWD,
		PersonaToolsCSV: req.PersonaToolsCSV,
		McpConfigPath:   req.McpConfigPath,
		Role:            req.Role,
	}
	csv, err := Substitute(b.cfg.AllowedToolsCSVTemplate, td, os.Getenv)
	if err != nil {
		return "", fmt.Errorf("backends: render allowed-tools-csv: %w", err)
	}
	return csv, nil
}

// renderEnv builds the final cmd.Env slice. Starts from os.Environ()
// minus the two ANTHROPIC variables that the drop_003 contract pins,
// then appends BackendConfig.Env entries with the same templating
// surface so values may pull selected host env vars via `{{env "VAR"}}`.
func (b *claudeNativeBackend) renderEnv(td TemplateData) ([]string, error) {
	envOut := filterEnv(os.Environ(), "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN")
	if len(b.cfg.Env) == 0 {
		return envOut, nil
	}
	rendered, err := SubstituteSlice(b.cfg.Env, td, os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("backends: render env: %w", err)
	}
	return append(envOut, rendered...), nil
}

// requireAPIKey scans the env slice for an ANTHROPIC_API_KEY entry and
// returns ErrAPIKeyRequired (wrapped via fmt.Errorf) if the key is absent
// or its value is whitespace-only. The helper enforces the explicit-key
// contract for sand-spawned claude -p --bare processes: env is checked for
// "ANTHROPIC_API_KEY=" prefix; if found with a non-empty value, requireAPIKey
// returns nil. If the key is missing or whitespace-only, it returns an error.
// The key's value typically comes from the host environment passthrough or
// from BackendConfig.Env template entries (which may forward selected host
// vars via {{env "VAR"}} expressions).
func requireAPIKey(env []string) error {
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == "ANTHROPIC_API_KEY" {
			if strings.TrimSpace(parts[1]) != "" {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: check that ANTHROPIC_API_KEY is set in the host environment or in BackendConfig.Env", ErrAPIKeyRequired)
}

// isFlag reports whether a token looks like a CLI flag (starts with
// `-`). Used by Preview to decide whether to pair it with the next
// token on one line.
func isFlag(token string) bool {
	return strings.HasPrefix(token, "-")
}

// quoteValue is a tiny strconv.Quote-equivalent inline implementation.
// Used by Preview so a multi-line --append-system-prompt value renders
// as a single line with `\n` escape sequences instead of breaking the
// one-arg-per-line layout. Matches the committed
// internal/dispatch/strconvQuote byte shape so cross-package tests can
// compare outputs char-for-char if needed.
func quoteValue(s string) string {
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

// filterEnv returns a copy of env with any entry whose key (the
// substring before the first `=`) appears in drop removed. Order is
// preserved. Mirrors the committed internal/dispatch/filterEnv contract
// — the helper is intentionally duplicated rather than imported because
// crossing the package boundary backend → dispatch would reintroduce
// the import cycle drop_011 retired.
func filterEnv(env []string, drop ...string) []string {
	skip := make(map[string]struct{}, len(drop))
	for _, k := range drop {
		skip[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		key := kv
		if eq >= 0 {
			key = kv[:eq]
		}
		if _, ok := skip[key]; ok {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// exitCodeFromErr extracts the child's exit code from a cmd.Run()
// error. nil error => 0. *exec.ExitError carries the real code via
// ExitCode(). Other error shapes (start failure, ctx cancellation
// before exec) return -1 so callers can distinguish "child ran and
// failed" from "child never produced an exit code". Mirrors the
// committed internal/dispatch/exitCodeFromErr.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
