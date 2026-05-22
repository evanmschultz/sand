// Package dispatch — claude-native spawn.
//
// This file owns runClaudeNative: the os/exec.CommandContext that spawns the
// claude CLI for one claude-native tier. SAND-SPEC §7.3 specifies the exact
// command shape; the L4 droplet
// drop_003.drop.build_claude_spawn_l4 fixes the flags this implementation
// must assemble.
//
// The spawn is intentionally narrow: it builds argv from inputs, pipes
// Params.Prompt to stdin, runs the command under the caller's context, and
// returns the captured stdout/stderr plus exit metadata. JSON envelope
// parsing, tool aggregation, and TOON encoding all live in sibling droplets
// (envelope.go and friends) so that file's tests can stay independent of any
// real claude CLI.
package dispatch

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

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
)

// claudeBin is the executable name resolved on PATH for the claude-native
// backend. It is a package-level var rather than a const so test code can
// substitute a fake CLI on a custom PATH while keeping the resolved name
// stable; production callers do not mutate it.
var claudeBin = "claude"

// antiRecursionSuffix is appended to the persona body before it becomes
// the spawned agent's --append-system-prompt argument. SAND-SPEC §7.4 defines
// the exact text; the goal is to prevent the spawned agent from calling
// sand.dispatch on itself and creating an unbounded recursion of dispatches.
//
// The leading `\n---\n` separator keeps the persona body's own markdown
// intact when concatenated; the spawned agent sees its own role's prompt
// followed by a clearly demarcated dispatch-context note.
const antiRecursionSuffix = "\n---\nDISPATCH CONTEXT: You are the %s agent, dispatched via sand. Execute the task below directly using YOUR role-appropriate tools (the orchestrator restricts them per the persona's `tools:` allowlist). Do NOT call sand.dispatch. Do NOT use the Agent tool to spawn other roles. Do NOT route the task elsewhere. You ARE the role. The orchestrator coordinates further dispatches."

// claudeResult is the raw output of one claude-native spawn. Envelope
// parsing (the typed JSON shape) is a sibling droplet's concern; this
// struct exposes only the artefacts a spawn produces.
type claudeResult struct {
	// Stdout is the captured stdout bytes from the claude CLI. For
	// `--output-format json` this is the full JSON envelope; the envelope
	// parser droplet consumes it.
	Stdout []byte

	// Stderr is the captured stderr bytes. Useful for diagnostics when the
	// spawn fails; the caller persists it to the per-dispatch log.
	Stderr []byte

	// ExitCode is the claude CLI's exit status. Zero on success.
	ExitCode int

	// DurationMs is the wall-clock duration of the spawn in milliseconds.
	DurationMs int64
}

// runClaudeNative spawns the claude CLI for one claude-native tier and
// returns the captured stdout/stderr plus exit metadata.
//
// The command is assembled per SAND-SPEC §7.3 and drop_003's L4 acceptance
// criteria:
//
//	claude -p --bare --model <model> --output-format json
//	  --no-session-persistence
//	  --append-system-prompt <persona.Body + antiRecursionSuffix>
//	  --mcp-config <cwd>/.mcp.json   (only when mcpConfigPath is non-empty)
//	  --allowedTools <persona.Tools CSV>
//
// params.Prompt is piped to the command's stdin. ModelOverride wins over
// tier.Model when non-empty.
//
// The spawn runs under ctx — context cancellation/timeout propagates to the
// child process via exec.CommandContext.
//
// The caller-pinned working directory params.CWD is honored when non-empty
// (relative paths normalized via filepath.Abs). The child inherits the
// parent environment minus ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN
// (per SAND-SPEC §7.3 — claude-native must not see the ollama-redirect env).
//
// Non-zero exit is NOT an error condition at this layer: callers inspect
// claudeResult.ExitCode and Stderr to decide whether to advance the chain.
// runClaudeNative returns an error only when the claude binary cannot be
// resolved on PATH, when ctx fires before the spawn completes, or when
// stdin/stdout/stderr plumbing fails before exec.
func runClaudeNative(
	ctx context.Context,
	params Params,
	p persona.Persona,
	tier chains.Tier,
	mcpConfigPath string,
) (claudeResult, error) {
	model := tier.Model
	if params.ModelOverride != "" {
		model = params.ModelOverride
	}

	if _, lookErr := exec.LookPath(claudeBin); lookErr != nil {
		return claudeResult{}, fmt.Errorf("dispatch: locate %s on PATH: %w", claudeBin, lookErr)
	}

	systemPrompt := p.Body + fmt.Sprintf(antiRecursionSuffix, params.Role)
	allowed := strings.Join(p.Tools, ",")

	args := []string{
		"-p",
		"--bare",
		"--model", model,
		"--output-format", "json",
		"--no-session-persistence",
		"--append-system-prompt", systemPrompt,
	}
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	if allowed != "" {
		args = append(args, "--allowedTools", allowed)
	}

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Stdin = strings.NewReader(params.Prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.Env = filterEnv(os.Environ(), "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN")

	if params.CWD != "" {
		absCwd, err := filepath.Abs(params.CWD)
		if err != nil {
			return claudeResult{}, fmt.Errorf("dispatch: resolve cwd %q: %w", params.CWD, err)
		}
		cmd.Dir = absCwd
	}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	// Context cancellation manifests as runErr != nil + ctx.Err() != nil;
	// surface ctx.Err() so callers using errors.Is(err, context.Canceled)
	// or context.DeadlineExceeded can detect the case cleanly.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return claudeResult{
			Stdout:     stdout.Bytes(),
			Stderr:     stderr.Bytes(),
			ExitCode:   exitCodeFromErr(runErr),
			DurationMs: elapsed.Milliseconds(),
		}, fmt.Errorf("dispatch: claude spawn aborted: %w", ctxErr)
	}

	result := claudeResult{
		Stdout:     stdout.Bytes(),
		Stderr:     stderr.Bytes(),
		ExitCode:   exitCodeFromErr(runErr),
		DurationMs: elapsed.Milliseconds(),
	}

	// Spawn-level plumbing failure (e.g. could not start the process) is
	// distinct from non-zero exit. exec.Error indicates the former; we
	// surface it so the caller doesn't try to parse an empty envelope.
	var execErr *exec.Error
	if errors.As(runErr, &execErr) {
		return result, fmt.Errorf("dispatch: start claude: %w", runErr)
	}

	return result, nil
}

// filterEnv returns a copy of env with any entry whose key (the substring
// before the first `=`) appears in drop removed. Order is preserved.
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

// exitCodeFromErr extracts the child's exit code from a cmd.Run() error.
// nil error => 0. *exec.ExitError carries the real code via ExitCode().
// Other error shapes (start failure, ctx cancellation before exec) return
// -1 so callers can distinguish "child ran and failed" from "child never
// produced an exit code".
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
