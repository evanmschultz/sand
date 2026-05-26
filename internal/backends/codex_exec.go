// Package backends — codex-exec concrete Backend implementation.
//
// This file owns codexExecBackend: the Backend impl that spawns the
// codex CLI via os/exec under `codex exec --ephemeral --ignore-user-config
// --skip-git-repo-check -C <cwd> -m <model>` per SAND-SPEC §7.1.
//
// Key differences from claudeNativeBackend:
//
//   - DIFFERENT CLI — `codex exec ...` not `claude -p ...`. argv shape is
//     supplied by `BackendConfig.Args` (templated against TemplateData);
//     no `--mcp-config` / `--allowedTools` flag pairs.
//   - DIFFERENT MCP INJECTION — role-conditional static injection via
//     `RenderRoleConditionalMCPFlags(req.Role, req.CWD)` (sibling
//     codex_mcp_probe.go). No `.mcp.json` probe: tool lists are static,
//     and the role determines which servers are injected unconditionally.
//   - SAME anti-recursion suffix — reuses the package-private
//     `antiRecursionSuffix` const from claude_native.go so spawned-agent
//     prompt shape stays identical across backends.
//   - SAME env filter — strips ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN
//     so the codex subprocess never sees the ollama-redirect env
//     (preserves drop_003 contract; harmless for codex which doesn't
//     consume ANTHROPIC vars). OPENAI_API_KEY flows naturally via
//     os.Environ().
//   - SAME non-zero-exit-is-data semantics — child exit code is
//     SpawnResult.ExitCode, never a Go error.
//
// EnvelopeFormat() returns "codex_stream" per drop_005 L3 amendment C2,
// anticipating the Backend interface widen by the sibling envelope
// droplet.
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

// codexExecBackend is the Backend implementation for the `codex-exec`
// provider. It binds one BackendConfig (the resolved
// `[backends.codex-exec]` TOML table) to the Spawn / Preview /
// EnvelopeFormat methods the Backend interface declares (with the
// EnvelopeFormat widening anticipated by drop_005 L3 amendment C2).
//
// The struct is unexported because external callers always go through
// the Resolve factory in backend.go; the Backend interface is the
// public contract.
type codexExecBackend struct {
	cfg BackendConfig
}

// EnvelopeFormat returns "codex_stream" per drop_005 L3 amendment C2.
//
// The sibling envelope-routing droplet widens the Backend interface to
// include EnvelopeFormat() so dispatch.go can route parser selection
// (claude_json → ParseEnvelope; codex_stream → ParseCodexEnvelope)
// without re-reading BackendConfig. This method lets the backend
// declare its envelope dialect at the type level.
func (b *codexExecBackend) EnvelopeFormat() string { return "codex_stream" }

// Spawn runs the codex CLI for one dispatch and captures stdout +
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
//  4. Appending role-conditional `-c <inline-TOML>` flags via
//     RenderRoleConditionalMCPFlags(req.Role, req.CWD). The tool lists
//     are static; no `.mcp.json` probe is performed. McpConfigPath is
//     not consulted by this path (it may be used by other backends).
//
// Env handling:
//
//   - Process environment is filtered to strip ANTHROPIC_BASE_URL and
//     ANTHROPIC_AUTH_TOKEN (preserving the drop_003 contract that
//     codex never sees the ollama-redirect env; harmless because codex
//     does not consume ANTHROPIC vars but the filter keeps the
//     env-shape invariant predictable across backends).
//   - BackendConfig.Env entries are then appended; each `KEY=VALUE` is
//     rendered through the template engine so `{{env "VAR"}}` inside
//     VALUE works for forwarding selected host env vars (e.g.
//     OPENAI_API_KEY flows naturally via os.Environ() without needing a
//     templated forward).
//
// Prompt handling: when BackendConfig.StdinPrompt is true, req.Prompt
// is piped to the child's stdin via a strings.Reader. When false,
// callers rely on a `{{prompt}}` substitution in Args — not yet
// supported by the template engine but the field is preserved for
// future drops.
//
// Context handling: ctx is forwarded to exec.CommandContext. When ctx
// fires before the child completes, the returned error wraps ctx.Err()
// so callers can use errors.Is(err, context.Canceled) /
// context.DeadlineExceeded.
//
// Non-zero exit is NOT an error condition at this layer — the
// dispatcher classifies via stderr + exit code. Spawn returns a Go
// error only when the binary cannot be resolved, when ctx fires, when
// an exec plumbing failure surfaces, or when a template render fails.
func (b *codexExecBackend) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	resolvedCmd, err := exec.LookPath(b.cfg.Command)
	if err != nil {
		return SpawnResult{}, fmt.Errorf("backends: locate %s on PATH: %w", b.cfg.Command, err)
	}

	args, td, err := b.renderArgs(req)
	if err != nil {
		return SpawnResult{}, err
	}

	// Role-conditional MCP injection — static, no .mcp.json probe.
	// RenderRoleConditionalMCPFlags selects servers by role and CWD;
	// the returned slice is ready to append directly to args.
	mcpArgs := RenderRoleConditionalMCPFlags(req.Role, req.CWD)
	args = append(args, mcpArgs...)

	envOut, err := b.renderEnv(td)
	if err != nil {
		return SpawnResult{}, err
	}

	// Hermetic CODEX_HOME lifecycle — mirrors bin/agent-dispatch.sh:521-544.
	// Create a throwaway temp dir that contains only the auth/identity
	// symlinks codex needs plus an execpolicy rules file. defer cleanup()
	// fires regardless of subsequent errors so the dir never leaks.
	// bashDenyPatterns is nil here (Area 5 threads the real gate patterns;
	// this nil is the intentional Area 5 seam — do not fill it here).
	hermeticDir, cleanup, hermeticErr := newHermeticCodexHome(nil)
	if hermeticErr != nil {
		return SpawnResult{}, hermeticErr
	}
	defer cleanup()
	envOut = append(envOut, "CODEX_HOME="+hermeticDir)

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
		return result, fmt.Errorf("backends: codex spawn aborted: %w", ctxErr)
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

// Preview renders the would-be codex command shape without spawning a
// subprocess. The output preserves the committed
// internal/dispatch/renderDryRunCommand byte shape — one argument per
// line, flag+value pairs space-separated (NOT `flag=value`), values
// containing newlines / quotes / tabs escaped via strconv-Quote-style
// rendering so multi-line persona bodies stay on one rendered line,
// and a trailing `  <<< "<prompt>"\n` heredoc marker when StdinPrompt
// is true.
//
// Preview does NOT invoke ProbeMCPServer — probing a live MCP server
// from a dry-run path would be a confusing side effect. Per-MCP `-c`
// flags are omitted from Preview; the dispatcher / preflight tool
// renders MCP injection summary separately when needed.
func (b *codexExecBackend) Preview(req SpawnRequest) (string, error) {
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

	if b.cfg.StdinPrompt {
		out.WriteString("  <<< ")
		out.WriteString(quoteValue(req.Prompt))
		out.WriteByte('\n')
	}

	return out.String(), nil
}

// renderArgs substitutes BackendConfig.Args element-wise per the
// template engine. Returns the rendered argv slice plus the
// TemplateData used (so callers like renderEnv can share the same
// substitution surface without re-deriving the persona-body-plus-suffix
// value).
//
// The persona body has the role-templated anti-recursion suffix
// appended BEFORE template substitution; downstream `{{.PersonaBody}}`
// references see the full text.
//
// Unlike claudeNativeBackend.renderArgs, this method does NOT append
// conditional --mcp-config / --allowedTools pairs — codex consumes MCP
// inline via per-server `-c` flags handled by RenderRoleConditionalMCPFlags
// in Spawn, and codex has no --allowedTools concept (tool allow-listing
// is per-server via the inline TOML's `tools={...}` map).
func (b *codexExecBackend) renderArgs(req SpawnRequest) ([]string, TemplateData, error) {
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

	// Hermetic config overrides — appended unconditionally after the
	// TOML-defined args so they appear in both Spawn and Preview (Preview
	// calls only renderArgs; Spawn-appended flags would be invisible to it).
	// These mirror bin/agent-dispatch.sh:393-428 (4-way consensus 2026-05-25):
	//   approval_policy  — enables "never" approval (workspace-write sandbox
	//                      is INERT in exec without this; `-a` is not a valid
	//                      `codex exec` flag — the knob is this -c form).
	//   web_search       — re-enables live web search per-run (HOME config
	//                      web_search value is ignored under --ignore-user-config).
	//   project_doc_max_bytes=0 — caps instruction-doc budget to 0 bytes,
	//                      preventing AGENTS.md from polluting the persona.
	//   skills.bundled.enabled=false — disables runtime-bundled skills so the
	//                      agent's world is only the persona + injected MCP.
	args = append(
		args,
		"-c", `approval_policy="never"`,
		"-c", `web_search="live"`,
		"-c", `project_doc_max_bytes=0`,
		"-c", `skills.bundled.enabled=false`,
	)

	return args, td, nil
}

// renderEnv builds the final cmd.Env slice. Starts from os.Environ()
// minus the two ANTHROPIC variables that the drop_003 contract pins
// (harmless for codex which doesn't consume them, but keeps the
// env-shape invariant predictable across backends), then appends
// BackendConfig.Env entries with the same templating surface so values
// may pull selected host env vars via `{{env "VAR"}}`.
func (b *codexExecBackend) renderEnv(td TemplateData) ([]string, error) {
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
