package debugtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestChainsListTool exercises the sand.chains_list MCP tool wrapper
// end-to-end. Cases cover the drop_006.drop.build_chains_list_tool acceptance
// criteria:
//
//   - happy path: a valid chain config under
//     <projectDir>/.claude/sand-chains.toml produces the nested TOON shape
//     from SAND-SPEC §3.4, with roles sorted alphabetically (Go map
//     iteration order is randomized; the wrapper sorts roles deterministically
//     before emit) and zero wait_max/slots rendered as empty CSV cells.
//   - missing file: a project directory without a .claude/sand-chains.toml
//     returns a CallToolResult with IsError == true and a descriptive
//     "chain config not found" message.
//   - parse error: a malformed TOML config returns a CallToolResult with
//     IsError == true that surfaces "parse chain config" plus the underlying
//     decode failure.
//
// Fixtures are materialized into t.TempDir()/.claude/sand-chains.toml at the
// path the handler will resolve at runtime, mirroring the persona_get_test
// pattern; this keeps the repo free of committed test artifacts.
func TestChainsListTool(t *testing.T) {
	t.Parallel()

	t.Run("happy path two roles sorted alphabetically", func(t *testing.T) {
		t.Parallel()

		projectDir := t.TempDir()
		writeChainsFixture(t, projectDir, happyChainsTOML)

		tool, handler := ChainsListTool(projectDir)
		assertToolMeta(t, tool)

		res := callChainsList(t, handler)
		if res.IsError {
			t.Fatalf("expected IsError=false on happy path; got error: %s", textBody(t, res))
		}

		got := textBody(t, res)

		// Roles in the fixture are declared planning-then-builder; output
		// must be builder-then-planning (alphabetical). Inner tier rows
		// mirror SAND-SPEC §3.4 byte-for-byte: zero wait_max/slots render as
		// empty CSV cells, non-zero values render as decimals.
		want := "roles[2]:\n" +
			"  - role: ta-go-builder\n" +
			"    tiers[3]{tier,backend,model,opts,wait_max,slots}:\n" +
			"      1,ollama-local,qwen2.5-coder:7b,,20,4\n" +
			"      2,codex-exec,gpt-5.5,--sandbox workspace-write -c model_reasoning_effort=low,,\n" +
			"      3,claude-native,haiku,,,\n" +
			"  - role: ta-go-planning\n" +
			"    tiers[4]{tier,backend,model,opts,wait_max,slots}:\n" +
			"      1,codex-exec,gpt-5.5,--sandbox read-only -c model_reasoning_effort=low,,\n" +
			"      2,codex-exec,gpt-5.5,--sandbox read-only -c model_reasoning_effort=medium,,\n" +
			"      3,claude-native,sonnet,,,\n" +
			"      4,claude-native,opus,,,\n"

		if got != want {
			t.Fatalf("TOON output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	t.Run("missing chains file returns tool error", func(t *testing.T) {
		t.Parallel()

		projectDir := t.TempDir()
		// Deliberately do NOT write a .claude/sand-chains.toml fixture.

		tool, handler := ChainsListTool(projectDir)
		assertToolMeta(t, tool)

		res := callChainsList(t, handler)
		if !res.IsError {
			t.Fatalf("expected IsError=true for missing chains file; got success: %s", textBody(t, res))
		}

		msg := textBody(t, res)
		wantSubstrings := []string{
			"chains_list",
			"chain config not found",
			filepath.Join(projectDir, ".claude", "sand-chains.toml"),
		}
		for _, want := range wantSubstrings {
			if !strings.Contains(msg, want) {
				t.Errorf("missing-file error missing substring %q\nfull message:\n%s", want, msg)
			}
		}
	})

	t.Run("malformed toml returns tool error", func(t *testing.T) {
		t.Parallel()

		projectDir := t.TempDir()
		writeChainsFixture(t, projectDir, malformedChainsTOML)

		tool, handler := ChainsListTool(projectDir)
		assertToolMeta(t, tool)

		res := callChainsList(t, handler)
		if !res.IsError {
			t.Fatalf("expected IsError=true for malformed TOML; got success: %s", textBody(t, res))
		}

		msg := textBody(t, res)
		wantSubstrings := []string{
			"chains_list",
			"parse chain config",
			filepath.Join(projectDir, ".claude", "sand-chains.toml"),
		}
		for _, want := range wantSubstrings {
			if !strings.Contains(msg, want) {
				t.Errorf("parse-error message missing substring %q\nfull message:\n%s", want, msg)
			}
		}
	})
}

// happyChainsTOML mirrors SAND-SPEC §3.4's worked example. Roles are
// declared in REVERSE alphabetical order so the test exercises the
// wrapper's deterministic role-name sort.
const happyChainsTOML = `[chains]
"ta-go-planning" = [
  { backend = "codex-exec",    model = "gpt-5.5", opts = "--sandbox read-only -c model_reasoning_effort=low",    wait_max = 0, slots = 0 },
  { backend = "codex-exec",    model = "gpt-5.5", opts = "--sandbox read-only -c model_reasoning_effort=medium", wait_max = 0, slots = 0 },
  { backend = "claude-native", model = "sonnet",  opts = "",                                                     wait_max = 0, slots = 0 },
  { backend = "claude-native", model = "opus",    opts = "",                                                     wait_max = 0, slots = 0 },
]

"ta-go-builder" = [
  { backend = "ollama-local",  model = "qwen2.5-coder:7b", opts = "",                                                        wait_max = 20, slots = 4 },
  { backend = "codex-exec",    model = "gpt-5.5",          opts = "--sandbox workspace-write -c model_reasoning_effort=low", wait_max = 0,  slots = 0 },
  { backend = "claude-native", model = "haiku",            opts = "",                                                        wait_max = 0,  slots = 0 },
]
`

// malformedChainsTOML is deliberately broken (unclosed inline-table array)
// so chains.Parse returns a decode error and the wrapper surfaces it as an
// MCP tool error.
const malformedChainsTOML = `[chains]
"ta-go-builder" = [
  { backend = "ollama-local", model = "qwen2.5-coder:7b"
]
`

// writeChainsFixture materializes content at
// <projectDir>/.claude/sand-chains.toml, creating the .claude/ subdir as
// needed. This matches the path the handler resolves at runtime.
func writeChainsFixture(t *testing.T, projectDir, content string) {
	t.Helper()
	dir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "sand-chains.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

// callChainsList invokes the handler with an empty CallToolRequest (the tool
// takes no arguments per SAND-SPEC §3.4). Returning the result lets each
// subtest assert on IsError and content independently.
func callChainsList(t *testing.T, handler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "chains_list"

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned Go error: %v; tool errors must surface via *CallToolResult.IsError, not the error return", err)
	}
	if res == nil {
		t.Fatalf("handler returned nil result with nil error")
	}
	return res
}

// assertToolMeta checks the tool descriptor's static metadata once per
// subtest. The name is pinned to "chains_list" to match the sibling
// PersonaGetTool's "sand.persona_get" naming convention and SAND-SPEC §3.4.
func assertToolMeta(t *testing.T, tool mcp.Tool) {
	t.Helper()
	if tool.Name != "chains_list" {
		t.Fatalf("tool.Name = %q, want %q", tool.Name, "chains_list")
	}
	if strings.TrimSpace(tool.Description) == "" {
		t.Fatalf("tool.Description is empty; want a non-empty description for the MCP tool catalog")
	}
}

// textBody returns the concatenated text content of res. Both
// mcp.NewToolResultText and mcp.NewToolResultError land their payload in
// res.Content as mcp.TextContent entries; this helper centralizes the
// type-assertion so individual cases stay focused on behavior.
func textBody(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
