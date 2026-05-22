package debugtools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/evanmschultz/sand/internal/chains"
)

// chainsConfigRelPath is the project-relative location of the sand chain
// config, per SAND-SPEC §5. The tool always reads from this path under the
// caller project directory passed at registration time.
const chainsConfigRelPath = ".claude/sand-chains.toml"

// ChainsListTool constructs the mcp-go tool descriptor + handler for
// sand.chains_list (per SAND-SPEC §3.4). The returned tool takes no input
// arguments and emits a nested TOON document enumerating every configured
// role and its ordered fallback chain.
//
// projectDir is the caller project root; the handler resolves
// `<projectDir>/.claude/sand-chains.toml` at every call so that an operator
// editing the config does not need to restart sand to see the update. This
// mirrors the projectDir-binding convention used by PersonaGetTool.
//
// Parsing is delegated to internal/chains.Parse — this package does NOT
// implement a second TOML decoder.
func ChainsListTool(projectDir string) (mcp.Tool, server.ToolHandlerFunc) {
	tool := mcp.NewTool(
		"sand.chains_list",
		mcp.WithDescription(
			"Enumerate all sand roles and their fallback chains from the caller "+
				"project's .claude/sand-chains.toml. Returns a nested TOON document "+
				"(roles[N]: with inner tiers[N]{tier,backend,model,opts,wait_max,slots}:) "+
				"per SAND-SPEC §3.4. Read-only debug tool — does not dispatch.",
		),
	)

	handler := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cfgPath := filepath.Join(projectDir, chainsConfigRelPath)
		body, err := renderChainsList(cfgPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(body), nil
	}

	return tool, handler
}

// renderChainsList opens the chain config at cfgPath, parses it via
// internal/chains.Parse, and serializes the result as the nested TOON document
// declared in SAND-SPEC §3.4.
//
// Errors are descriptive and wrap underlying causes with %w so callers can use
// errors.Is to distinguish missing-file from parse-error cases.
func renderChainsList(cfgPath string) (string, error) {
	f, err := os.Open(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("chains_list: chain config not found at %s: %w", cfgPath, err)
		}
		return "", fmt.Errorf("chains_list: open chain config %s: %w", cfgPath, err)
	}
	defer f.Close()

	cfg, err := chains.Parse(f)
	if err != nil {
		return "", fmt.Errorf("chains_list: parse chain config %s: %w", cfgPath, err)
	}

	return marshalChainsTOON(cfg), nil
}

// marshalChainsTOON renders cfg as the nested TOON document declared in
// SAND-SPEC §3.4. The outer array is YAML-list-of-objects (each row begins
// with `  - role: <name>`); inner tiers use TOON tabular emission with a
// shared `{tier,backend,model,opts,wait_max,slots}` header.
//
// Role iteration order is alphabetical so output is deterministic across
// runs (Go map iteration order is randomized).
func marshalChainsTOON(cfg chains.Config) string {
	roleNames := make([]string, 0, len(cfg.Roles))
	for name := range cfg.Roles {
		roleNames = append(roleNames, name)
	}
	sort.Strings(roleNames)

	var b strings.Builder
	fmt.Fprintf(&b, "roles[%d]:\n", len(roleNames))

	for _, name := range roleNames {
		tiers := cfg.Roles[name]
		fmt.Fprintf(&b, "  - role: %s\n", name)
		fmt.Fprintf(&b, "    tiers[%d]{tier,backend,model,opts,wait_max,slots}:\n", len(tiers))
		for i, t := range tiers {
			fmt.Fprintf(
				&b,
				"      %d,%s,%s,%s,%s,%s\n",
				i+1,
				t.Backend,
				t.Model,
				t.Opts,
				formatIntOrEmpty(t.WaitMax),
				formatIntOrEmpty(t.Slots),
			)
		}
	}

	return b.String()
}

// formatIntOrEmpty renders an int as its decimal representation, or as the
// empty string when n is zero. SAND-SPEC §3.4 shows `wait_max`/`slots` as
// literal empty CSV cells for non-ollama tiers (where the zero value is
// "unset" semantically); we follow that convention so the emitted TOON
// matches the spec byte-for-byte.
func formatIntOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
