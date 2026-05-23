// Package backends — codex-exec concrete Backend implementation.
//
// This file owns codexExecBackend: the Backend impl that spawns the
// codex CLI via os/exec under `codex exec --ephemeral --ignore-rules
// --skip-git-repo-check -C <cwd> -m <model>` per SAND-V02-SPEC §7.1.
//
// Key differences from claudeNativeBackend:
//
//   - DIFFERENT CLI — `codex exec ...` not `claude -p ...`. argv shape is
//     supplied by `BackendConfig.Args` (templated against TemplateData);
//     no `--mcp-config` / `--allowedTools` flag pairs.
//   - DIFFERENT MCP INJECTION — for each MCP server declared in the
//     caller's `.mcp.json`, this backend probes the server via
//     `ProbeMCPServer` (sibling codex_mcp_probe.go) to discover canonical
//     tool names, then appends per-server `-c <inline-TOML>` flags. Probe
//     failures are non-fatal: the server is skipped and dispatch
//     proceeds with the remaining `-c` flags.
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
	"encoding/json"
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
//  4. Appending per-MCP `-c <inline-TOML>` flags when req.McpConfigPath
//     is non-empty: each declared MCP server in the caller's
//     `.mcp.json` is probed via ProbeMCPServer; successful probes
//     contribute `-c "mcp_servers.<name>={...}"` to the argv. Skipped
//     probes log a warning to stderr and continue.
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
// Context handling: ctx is forwarded to exec.CommandContext AND to
// ProbeMCPServer for the per-server probe steps. When ctx fires before
// the child completes, the returned error wraps ctx.Err() so callers
// can use errors.Is(err, context.Canceled) /
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

	// Per-MCP -c flag injection. Probe failures are non-fatal: the failing
	// server's `-c` is omitted and a warning is written to stderr. Loading
	// the .mcp.json file itself fails soft for the same reason — codex
	// dispatch should proceed even when caller MCP config is unreadable.
	if req.McpConfigPath != "" {
		mcpArgs := b.renderMCPInjectionFlags(ctx, req.McpConfigPath)
		args = append(args, mcpArgs...)
	}

	envOut, err := b.renderEnv(td)
	if err != nil {
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
// inline via per-server `-c` flags handled by renderMCPInjectionFlags,
// and codex has no --allowedTools concept (tool allow-listing is per-
// server via the inline TOML's `tools={...}` map).
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

// renderMCPInjectionFlags loads the caller's `.mcp.json`, probes each
// declared MCP server via ProbeMCPServer, and returns a flat argv
// fragment of `-c <inline-TOML>` pairs ready to append to the codex
// command line.
//
// Failure modes are non-fatal at this layer — every problem (mcp.json
// unreadable / unparseable, per-server probe Skipped) writes a warning
// to stderr and contributes zero `-c` flags for the affected server.
// The codex spawn proceeds with whatever subset of servers probed
// cleanly. This mirrors the planner acceptance criterion that probe
// failures MUST NOT halt dispatch.
//
// Server iteration order is deterministic (alphabetical by server name)
// so the rendered argv stays stable across runs — important for the
// per-dispatch JSON log files in drop_007.
func (b *codexExecBackend) renderMCPInjectionFlags(ctx context.Context, mcpConfigPath string) []string {
	servers, err := loadMCPServersFromConfig(mcpConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sand: codex-exec: load %s: %v (skipping MCP injection)\n", mcpConfigPath, err)
		return nil
	}

	// Stable iteration order: sort server names so the rendered argv is
	// deterministic across runs. Go's map range is intentionally
	// randomized; we explicitly sort here.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sortStringsAsc(names)

	var out []string
	for _, name := range names {
		entry := servers[name]
		result, probeErr := ProbeMCPServer(ctx, name, entry)
		if probeErr != nil {
			// Plumbing-level probe error (should never happen per the
			// probe's contract — operational failures surface as
			// Skipped). Log and continue.
			fmt.Fprintf(os.Stderr, "sand: codex-exec: probe %s: %v (skipping)\n", name, probeErr)
			continue
		}
		if result.Skipped {
			fmt.Fprintf(os.Stderr, "sand: codex-exec: %s\n", result.SkipReason)
			continue
		}
		out = append(out, "-c", result.InlineTOML)
	}
	return out
}

// mcpConfigFile is the subset of a Claude Code `.mcp.json` file the
// codex-exec backend needs. The full schema includes per-server `type`
// fields and other keys this backend ignores; encoding/json's default
// behavior of dropping unknown keys is the load-bearing reason this
// struct only declares the fields we consume.
type mcpConfigFile struct {
	MCPServers map[string]MCPServerEntry `json:"mcpServers"`
}

// loadMCPServersFromConfig parses a `.mcp.json` file at the given path
// and returns the declared MCP-server entries as a name→entry map.
//
// Returns a non-nil error only on I/O failure or JSON parse failure.
// An empty / absent `mcpServers` object in an otherwise valid file
// returns an empty map + nil error.
func loadMCPServersFromConfig(path string) (map[string]MCPServerEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var file mcpConfigFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if file.MCPServers == nil {
		return map[string]MCPServerEntry{}, nil
	}
	return file.MCPServers, nil
}

// sortStringsAsc sorts a string slice in ascending lexicographic order
// in place. Inlined as a tiny helper instead of importing sort just
// for the one call site — keeps the import graph minimal. Uses an
// O(n^2) insertion sort which is fine for the small server-count
// expected in a `.mcp.json` (typical caller has < 10 MCP servers).
func sortStringsAsc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
