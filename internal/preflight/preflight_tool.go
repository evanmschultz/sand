package preflight

// MCP-facing wrapper for `sand.preflight` per SAND-SPEC §3.2.
//
// This file constructs the mcp-go tool descriptor + handler closure and wires
// a real-stdlib Probe (os/exec.LookPath, net/http for the ollama daemon
// liveness check, exec.CommandContext for `ollama list`) into the pure walk
// logic in preflight.go.
//
// The handler:
//   - validates the required `role` argument and emits an MCP tool error
//     BEFORE constructing any probe (per the build_preflight_tool acceptance
//     criteria: "missing role returns an MCP/tool error before probing");
//   - loads the caller project's `.claude/sand-chains.toml` via
//     internal/chains.Parse, mirroring the convention established by
//     debugtools.ChainsListTool;
//   - looks up the role's fallback chain (errors.Is ErrUnknownRole when the
//     role is absent from the config);
//   - delegates to Preflight() with the real-stdlib probe;
//   - serializes the resulting Report as TOON via internal/toon, matching the
//     §3.2 shape `role: <string>` + `tiers[N]{tier,backend,model,ok,reason}:`
//     byte-for-byte.
//
// Tests inject a deterministic Probe via the unexported NewToolWithProbe
// constructor so unit tests do not depend on real `claude`/`codex`/`ollama`
// binaries or a reachable daemon.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/toon"
)

// chainsConfigRelPath is the project-relative location of the sand chain
// config per SAND-SPEC §5; the handler resolves it under the caller project
// directory at every call, matching the convention used by
// debugtools.ChainsListTool so an operator editing the chain config does not
// have to restart sand.
const chainsConfigRelPath = ".claude/sand-chains.toml"

// defaultHTTPTimeout caps the daemon-liveness probe at a few seconds so a
// hung ollama process does not hold the entire preflight call open.
const defaultHTTPTimeout = 3 * time.Second

// PreflightTool constructs the mcp-go tool descriptor + handler for
// sand.preflight per SAND-SPEC §3.2 and wires a real-stdlib Probe. It is the
// constructor cmd/sand/main.go registers; the test-injectable constructor
// is NewToolWithProbe below.
//
// projectDir is the caller project root; the handler resolves
// `<projectDir>/.claude/sand-chains.toml` at every call. This mirrors the
// projectDir-binding pattern used by PersonaGetTool / ChainsListTool.
func PreflightTool(projectDir string) (mcp.Tool, server.ToolHandlerFunc) {
	return NewToolWithProbe(projectDir, defaultProbe())
}

// NewToolWithProbe is the dependency-injected constructor used by tests. It
// accepts an arbitrary Probe implementation so the handler can be exercised
// end-to-end without spawning real binaries or hitting localhost:11434.
//
// Production callers should use PreflightTool, which delegates here with the
// real-stdlib defaultProbe.
func NewToolWithProbe(projectDir string, probe Probe) (mcp.Tool, server.ToolHandlerFunc) {
	tool := mcp.NewTool(
		"preflight",
		mcp.WithDescription(
			"Probe a role's fallback chain and report per-tier readiness "+
				"(SAND-SPEC §3.2). Checks claude/codex CLI presence on PATH and, "+
				"for ollama-local tiers, the ollama daemon at "+
				"http://localhost:11434/api/version plus model presence via "+
				"`ollama list`. Returns TOON with a top-level role scalar and a "+
				"tiers[N]{tier,backend,model,ok,reason} tabular array.",
		),
		mcp.WithString(
			"role",
			mcp.Required(),
			mcp.Description("Role name whose chain to probe (e.g. \"ta-go-builder\"); matches the chain entry in <projectDir>/.claude/sand-chains.toml."),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		role, err := req.RequireString("role")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if strings.TrimSpace(role) == "" {
			return mcp.NewToolResultError("role must not be empty"), nil
		}

		cfgPath, _, resolveErr := chains.Resolve(projectDir)
		if resolveErr != nil {
			if errors.Is(resolveErr, chains.ErrChainConfigNotFound) {
				return mcp.NewToolResultError(
					fmt.Sprintf("preflight: chain config not found: %v", resolveErr),
				), nil
			}
			return mcp.NewToolResultError(
				fmt.Sprintf("preflight: resolve chain config: %v", resolveErr),
			), nil
		}
		chain, loadErr := loadChain(cfgPath, role)
		if loadErr != nil {
			return mcp.NewToolResultError(loadErr.Error()), nil
		}

		rep := Preflight(ctx, probe, role, chain)

		body, err := marshalReportTOON(rep)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("preflight: encode TOON: %v", err)), nil
		}
		return mcp.NewToolResultText(body), nil
	}

	return tool, handler
}

// loadChain opens cfgPath, parses it via internal/chains.Parse, and returns
// the tier slice for role. Errors are descriptive so the MCP tool result
// surfaces a useful message rather than a bare wrapped error chain.
func loadChain(cfgPath, role string) ([]chains.Tier, error) {
	f, err := os.Open(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("preflight: chain config not found at %s: %w", cfgPath, err)
		}
		return nil, fmt.Errorf("preflight: open chain config %s: %w", cfgPath, err)
	}
	defer f.Close()

	cfg, err := chains.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("preflight: parse chain config %s: %w", cfgPath, err)
	}

	chain, err := cfg.Chain(role)
	if err != nil {
		return nil, fmt.Errorf("preflight: %w", err)
	}
	return chain, nil
}

// marshalReportTOON encodes a Report as the SAND-SPEC §3.2 TOON shape:
//
//	role: <string>
//	tiers[N]{tier,backend,model,ok,reason}:
//	  1,ollama-local,qwen2.5-coder:7b,true,
//	  2,codex-exec,gpt-5.5,false,model not pulled locally
//
// Empty `reason` cells (the OK=true rows) are emitted as bare empty CSV
// fields, NOT `""`, by passing nil to the toon encoder for those positions —
// the encoder's scalarString(nil) path renders the empty cell verbatim.
func marshalReportTOON(rep Report) (string, error) {
	rows := make([][]any, 0, len(rep.Tiers))
	for _, t := range rep.Tiers {
		var reason any
		if t.Reason != "" {
			reason = t.Reason
		}
		rows = append(rows, []any{t.Tier, t.Backend, t.Model, t.OK, reason})
	}

	obj := toon.Object{
		{Key: "role", Value: rep.Role},
		{Key: "tiers", Value: toon.Tabular{
			Fields: []string{"tier", "backend", "model", "ok", "reason"},
			Rows:   rows,
		}},
	}

	out, err := toon.Encode(obj)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// defaultProbe returns the real-stdlib Probe implementation: os/exec.LookPath
// for CLI presence, a context-aware net/http.Client with a small timeout for
// the ollama daemon-liveness probe, and exec.CommandContext("ollama", "list")
// for model-presence parsing. Tests do NOT use this — they construct their
// own deterministic stub.
func defaultProbe() Probe {
	return &realProbe{
		client: &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// realProbe bridges the abstract Probe surface to real stdlib calls. It is
// intentionally tiny: each method is one or two stdlib invocations with
// context propagation. The interesting logic (chain walk, row-shaping,
// /api/version status interpretation) lives in preflight.go.
type realProbe struct {
	client *http.Client
}

// LookPath defers to exec.LookPath; the returned path is the absolute
// location on success and the wrapped error message names the binary when it
// is missing.
func (r *realProbe) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// HTTPGet issues a GET against url honouring ctx for cancellation/timeout.
// The caller (checkOllamaDaemon in preflight.go) is responsible for draining
// and closing the body.
func (r *realProbe) HTTPGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("preflight: build GET %s: %w", url, err)
	}
	return r.client.Do(req)
}

// OllamaList runs `ollama list` with the supplied context so the MCP-level
// deadline can cancel a hung process. The combined stdout+stderr is returned
// on success so any warnings ollama prints alongside the model table are
// still visible to the parser. A non-zero exit (e.g. ollama binary missing,
// daemon unreachable) is wrapped so the caller's reason string is
// informative.
func (r *realProbe) OllamaList(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "ollama", "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("preflight: ollama list: %w", err)
	}
	return string(out), nil
}
