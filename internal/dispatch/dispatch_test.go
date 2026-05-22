// Tests for the dispatch package core contract: the typed Params/Response
// shape and the exported sentinel errors. Both are public-API surface for
// sibling droplets (persona/chains wiring, Claude CLI spawn, envelope parser,
// TOON encoder, MCP tool registration), so this file pins the contract via
// table-driven tests against zero-value defaults and errors.Is matching.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/evanmschultz/sand/internal/chains"
)

// TestCoreContract exercises the zero-value shape of Params and Response and
// the assignability of every documented field. Failures here surface as
// either a field-name change or an incompatible type change, both of which
// must be deliberate — sibling droplets compile against these names.
func TestCoreContract(t *testing.T) {
	t.Parallel()

	t.Run("params zero value", func(t *testing.T) {
		t.Parallel()

		var p Params
		// Every Params field must be readable at zero value without panic
		// and must compare equal to its type's zero literal. Catches
		// accidental promotion of a field to a non-zero default or a
		// pointer-typed field that would zero-value to nil instead of "".
		cases := []struct {
			name string
			got  any
			want any
		}{
			{"Role", p.Role, ""},
			{"Prompt", p.Prompt, ""},
			{"CWD", p.CWD, ""},
			{"ModelOverride", p.ModelOverride, ""},
			{"DryRun", p.DryRun, false},
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Errorf("Params.%s zero value = %v, want %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("params field assignment", func(t *testing.T) {
		t.Parallel()

		// Exercise every documented field with a value distinguishable from
		// the zero literal. Round-tripping each field via direct assignment
		// is a compile-time + runtime check that the public field set
		// matches the droplet's acceptance criteria exactly.
		p := Params{
			Role:          "ta-go-builder",
			Prompt:        "do the thing",
			CWD:           "/abs/path",
			ModelOverride: "haiku",
			DryRun:        true,
		}
		cases := []struct {
			name string
			got  any
			want any
		}{
			{"Role", p.Role, "ta-go-builder"},
			{"Prompt", p.Prompt, "do the thing"},
			{"CWD", p.CWD, "/abs/path"},
			{"ModelOverride", p.ModelOverride, "haiku"},
			{"DryRun", p.DryRun, true},
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Errorf("Params.%s assigned = %v, want %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("response zero value", func(t *testing.T) {
		t.Parallel()

		var r Response
		// Scalar fields zero out as expected; aggregate fields (Tokens,
		// ToolsUsed, PermissionDenials) zero out to their own zero values
		// so the TOON encoder can render empty tabular sections without
		// special-casing nil.
		scalarCases := []struct {
			name string
			got  any
			want any
		}{
			{"Result", r.Result, ""},
			{"ServedBy", r.ServedBy, ""},
			{"Tier", r.Tier, 0},
			{"Fallback", r.Fallback, false},
			{"DurationMs", r.DurationMs, int64(0)},
			{"CostUSD", r.CostUSD, float64(0)},
			{"LogPath", r.LogPath, ""},
		}
		for _, c := range scalarCases {
			if c.got != c.want {
				t.Errorf("Response.%s zero value = %v, want %v", c.name, c.got, c.want)
			}
		}

		if r.Tokens != (Tokens{}) {
			t.Errorf("Response.Tokens zero value = %#v, want zero Tokens", r.Tokens)
		}
		if r.ToolsUsed != nil {
			t.Errorf("Response.ToolsUsed zero value = %#v, want nil slice", r.ToolsUsed)
		}
		if r.PermissionDenials != nil {
			t.Errorf("Response.PermissionDenials zero value = %#v, want nil slice", r.PermissionDenials)
		}
	})

	t.Run("response field assignment", func(t *testing.T) {
		t.Parallel()

		// Build a fully populated Response that exercises every documented
		// field including the three aggregate sub-types. A round-trip via
		// direct assignment confirms the field set, type set, and aggregate
		// element types match the droplet acceptance criteria.
		r := Response{
			Result:     "ok",
			ServedBy:   "claude-native:opus",
			Tier:       3,
			Fallback:   true,
			DurationMs: 168793,
			CostUSD:    0.626,
			Tokens: Tokens{
				Input:         10,
				Output:        13741,
				CacheRead:     120482,
				CacheCreation: 35481,
			},
			ToolsUsed: []ToolUse{
				{Name: "mcp__ta__get", Count: 4},
				{Name: "Read", Count: 8},
			},
			PermissionDenials: []PermissionDenial{
				{Tool: "Bash", Count: 0},
			},
			LogPath: "/tmp/sand-dispatch/log/abc123.json",
		}

		if r.Result != "ok" {
			t.Errorf("Response.Result = %q, want %q", r.Result, "ok")
		}
		if r.ServedBy != "claude-native:opus" {
			t.Errorf("Response.ServedBy = %q, want %q", r.ServedBy, "claude-native:opus")
		}
		if r.Tier != 3 {
			t.Errorf("Response.Tier = %d, want 3", r.Tier)
		}
		if !r.Fallback {
			t.Errorf("Response.Fallback = false, want true")
		}
		if r.DurationMs != 168793 {
			t.Errorf("Response.DurationMs = %d, want 168793", r.DurationMs)
		}
		if r.CostUSD != 0.626 {
			t.Errorf("Response.CostUSD = %g, want 0.626", r.CostUSD)
		}
		if r.LogPath != "/tmp/sand-dispatch/log/abc123.json" {
			t.Errorf("Response.LogPath = %q, want abc123 path", r.LogPath)
		}

		wantTokens := Tokens{Input: 10, Output: 13741, CacheRead: 120482, CacheCreation: 35481}
		if r.Tokens != wantTokens {
			t.Errorf("Response.Tokens = %#v, want %#v", r.Tokens, wantTokens)
		}

		if len(r.ToolsUsed) != 2 {
			t.Fatalf("Response.ToolsUsed len = %d, want 2", len(r.ToolsUsed))
		}
		if r.ToolsUsed[0] != (ToolUse{Name: "mcp__ta__get", Count: 4}) {
			t.Errorf("ToolsUsed[0] = %#v", r.ToolsUsed[0])
		}
		if r.ToolsUsed[1] != (ToolUse{Name: "Read", Count: 8}) {
			t.Errorf("ToolsUsed[1] = %#v", r.ToolsUsed[1])
		}

		if len(r.PermissionDenials) != 1 {
			t.Fatalf("Response.PermissionDenials len = %d, want 1", len(r.PermissionDenials))
		}
		if r.PermissionDenials[0] != (PermissionDenial{Tool: "Bash", Count: 0}) {
			t.Errorf("PermissionDenials[0] = %#v", r.PermissionDenials[0])
		}
	})
}

// TestDispatchSentinels exercises every exported sentinel: each must be
// distinct, non-nil, errors.Is-matchable when used directly, and
// errors.Is-matchable when wrapped via %w. Sibling droplets rely on this
// behavior to surface specific failure modes through wrapping.
func TestDispatchSentinels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		sentinel error
	}{
		{"ErrUnsupportedBackend", ErrUnsupportedBackend},
		{"ErrRoleNotInChains", ErrRoleNotInChains},
		{"ErrNoClaudeNativeTier", ErrNoClaudeNativeTier},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name+"/non nil", func(t *testing.T) {
			t.Parallel()
			if c.sentinel == nil {
				t.Fatalf("%s is nil; expected an initialised package-level error", c.name)
			}
			if c.sentinel.Error() == "" {
				t.Errorf("%s.Error() is empty; expected a diagnostic message", c.name)
			}
		})

		t.Run(c.name+"/direct match", func(t *testing.T) {
			t.Parallel()
			if !errors.Is(c.sentinel, c.sentinel) {
				t.Errorf("errors.Is(%s, %s) = false; want true", c.name, c.name)
			}
		})

		t.Run(c.name+"/wrapped match", func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("dispatch test: context: %w", c.sentinel)
			if !errors.Is(wrapped, c.sentinel) {
				t.Errorf("errors.Is(wrapped, %s) = false; want true via %%w", c.name)
			}
		})
	}

	t.Run("sentinels are distinct", func(t *testing.T) {
		t.Parallel()
		// Cross-match: each sentinel must NOT errors.Is-match the other two.
		// Catches a refactor that aliased two sentinels to the same value.
		pairs := []struct {
			a, b  error
			aName string
			bName string
		}{
			{ErrUnsupportedBackend, ErrRoleNotInChains, "ErrUnsupportedBackend", "ErrRoleNotInChains"},
			{ErrUnsupportedBackend, ErrNoClaudeNativeTier, "ErrUnsupportedBackend", "ErrNoClaudeNativeTier"},
			{ErrRoleNotInChains, ErrNoClaudeNativeTier, "ErrRoleNotInChains", "ErrNoClaudeNativeTier"},
		}
		for _, p := range pairs {
			if errors.Is(p.a, p.b) {
				t.Errorf("errors.Is(%s, %s) = true; sentinels must be distinct", p.aName, p.bName)
			}
			if errors.Is(p.b, p.a) {
				t.Errorf("errors.Is(%s, %s) = true; sentinels must be distinct", p.bName, p.aName)
			}
		}
	})
}

// writePersona writes a fixture persona markdown file at
// <cwd>/.claude/agents/<role>.md with the given frontmatter + body. The
// frontmatter is rendered as YAML-ish `key: value` lines per persona.Load's
// permissive parser (see internal/persona/persona.go).
func writePersona(t *testing.T, cwd, role, body string, tools []string, model string) {
	t.Helper()
	dir := filepath.Join(cwd, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup persona dir: %v", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + role + "\n")
	sb.WriteString("description: fixture persona for " + role + "\n")
	if model != "" {
		sb.WriteString("model: " + model + "\n")
	}
	if len(tools) > 0 {
		sb.WriteString("tools: " + strings.Join(tools, ",") + "\n")
	}
	sb.WriteString("---\n")
	sb.WriteString(body)
	path := filepath.Join(dir, role+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write persona %s: %v", path, err)
	}
}

// writeChainsConfig writes <cwd>/.claude/sand-chains.toml with the given
// TOML body.
func writeChainsConfig(t *testing.T, cwd, toml string) {
	t.Helper()
	dir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup chains dir: %v", err)
	}
	path := filepath.Join(dir, "sand-chains.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write chains config %s: %v", path, err)
	}
}

// writeMCPConfig writes <cwd>/.mcp.json with a minimal JSON object so
// resolveMCPConfig reports exists=true.
func writeMCPConfig(t *testing.T, cwd string) {
	t.Helper()
	path := filepath.Join(cwd, ".mcp.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write .mcp.json %s: %v", path, err)
	}
}

const builderChainsTOML = `
[chains]
"ta-go-builder" = [
  { backend = "ollama-local",  model = "qwen2.5-coder:7b", opts = "", wait_max = 20, slots = 4 },
  { backend = "claude-native", model = "haiku",            opts = "", wait_max = 0,  slots = 0 },
]
`

// TestDispatchDryRun exercises the dry-run path's command-shape rendering and
// the supporting field population on Response. Each sub-case stands up an
// isolated caller-project tree (persona file, sand-chains.toml, optional
// .mcp.json) so Dispatch's filesystem reads stay hermetic.
func TestDispatchDryRun(t *testing.T) {
	t.Parallel()

	t.Run("happy path with mcp config present", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(
			t, cwd, "ta-go-builder",
			"PERSONA BODY LINE 1\nPERSONA BODY LINE 2\n",
			[]string{"Read", "Edit", "Bash(mage testFunc *)"},
			"haiku",
		)
		writeChainsConfig(t, cwd, builderChainsTOML)
		writeMCPConfig(t, cwd)

		ctx := context.Background()
		params := Params{
			Role:   "ta-go-builder",
			Prompt: "build droplet X",
			CWD:    cwd,
			DryRun: true,
		}

		resp, err := Dispatch(ctx, params)
		if err != nil {
			t.Fatalf("Dispatch dry-run: unexpected error: %v", err)
		}

		// ServedBy + Tier per the orchestrator's prescribed dry-run shape:
		// Tier=0 signals "not actually served — the dry-run did not consume
		// a real fallback slot", ServedBy names the tier that WOULD serve.
		if got, want := resp.ServedBy, "claude-native:haiku"; got != want {
			t.Errorf("ServedBy = %q, want %q", got, want)
		}
		if got, want := resp.Tier, 0; got != want {
			t.Errorf("Tier = %d, want %d (dry-run sentinel)", got, want)
		}

		// Required command pieces — each is a separate assertion so a
		// failure flags the missing piece directly.
		required := []string{
			"claude -p",
			"--bare",
			"--model haiku",
			"--output-format json",
			"--no-session-persistence",
			"--append-system-prompt",
			"PERSONA BODY LINE 1",
			"--allowedTools Read,Edit,Bash(mage testFunc *)",
			"build droplet X",
		}
		for _, piece := range required {
			if !strings.Contains(resp.Result, piece) {
				t.Errorf("Result missing piece %q\nresult=%q", piece, resp.Result)
			}
		}

		// --mcp-config must reference the caller's absolute .mcp.json path.
		wantMCP := filepath.Join(cwd, ".mcp.json")
		if !strings.Contains(resp.Result, "--mcp-config "+wantMCP) {
			t.Errorf("Result missing --mcp-config %s\nresult=%q", wantMCP, resp.Result)
		}
	})

	t.Run("missing mcp config omits flag", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)
		// Intentionally NO writeMCPConfig — resolveMCPConfig must report
		// exists=false and Dispatch must drop --mcp-config from the
		// rendered command.

		ctx := context.Background()
		resp, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err != nil {
			t.Fatalf("Dispatch dry-run: unexpected error: %v", err)
		}
		if strings.Contains(resp.Result, "--mcp-config") {
			t.Errorf("Result must omit --mcp-config when .mcp.json absent\nresult=%q", resp.Result)
		}
	})

	t.Run("model override wins", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		ctx := context.Background()
		resp, err := Dispatch(ctx, Params{
			Role:          "ta-go-builder",
			Prompt:        "x",
			CWD:           cwd,
			ModelOverride: "opus",
			DryRun:        true,
		})
		if err != nil {
			t.Fatalf("Dispatch dry-run: unexpected error: %v", err)
		}
		if !strings.Contains(resp.Result, "--model opus") {
			t.Errorf("Result missing --model opus; override should win\nresult=%q", resp.Result)
		}
		if got, want := resp.ServedBy, "claude-native:opus"; got != want {
			t.Errorf("ServedBy = %q, want %q (override should be reflected)", got, want)
		}
	})

	t.Run("skips non-claude-native tiers before claude-native", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		// Chain has ollama first then claude-native; selection must
		// produce the claude-native model "haiku", not skip past it.
		writeChainsConfig(t, cwd, builderChainsTOML)

		ctx := context.Background()
		resp, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err != nil {
			t.Fatalf("Dispatch dry-run: unexpected error: %v", err)
		}
		if !strings.Contains(resp.Result, "--model haiku") {
			t.Errorf("Result must select claude-native model haiku from chain; got %q", resp.Result)
		}
	})
}

// TestDispatchSelectionErrors covers the four failure modes the droplet
// acceptance criteria call out: persona load failure, missing chains config,
// unknown role, and no-claude-native-tier.
func TestDispatchSelectionErrors(t *testing.T) {
	t.Parallel()

	t.Run("persona load error propagates", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writeChainsConfig(t, cwd, builderChainsTOML)
		// No persona file written for ta-go-builder.

		ctx := context.Background()
		_, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err == nil {
			t.Fatal("expected error when persona file is missing, got nil")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("persona load error should wrap os.ErrNotExist; got %v", err)
		}
		if !strings.Contains(err.Error(), "persona") {
			t.Errorf("error message should mention persona; got %q", err.Error())
		}
	})

	t.Run("missing chains config errors", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		// No sand-chains.toml.

		ctx := context.Background()
		_, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err == nil {
			t.Fatal("expected error when sand-chains.toml is missing, got nil")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("chains-config-missing error should wrap os.ErrNotExist; got %v", err)
		}
		if !strings.Contains(err.Error(), "sand-chains.toml") {
			t.Errorf("error should mention sand-chains.toml; got %q", err.Error())
		}
	})

	t.Run("unknown role wraps ErrRoleNotInChains", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		// Persona exists for ta-go-builder but the role we ask about is
		// not in the chain config.
		writePersona(t, cwd, "ta-go-mystery", "BODY", []string{"Read"}, "haiku")

		ctx := context.Background()
		_, err := Dispatch(ctx, Params{
			Role:   "ta-go-mystery",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err == nil {
			t.Fatal("expected error for unknown role, got nil")
		}
		if !errors.Is(err, ErrRoleNotInChains) {
			t.Errorf("error must satisfy errors.Is(err, ErrRoleNotInChains); got %v", err)
		}
		// chains.ErrUnknownRole is the underlying chains-package signal;
		// we explicitly wrap to ErrRoleNotInChains at the dispatch boundary
		// so dispatch-tier callers don't need to import chains. Verify the
		// wrap by checking the dispatch-level sentinel surfaces.
		if errors.Is(err, chains.ErrUnknownRole) {
			t.Errorf("dispatch should re-key chains.ErrUnknownRole to ErrRoleNotInChains; got %v", err)
		}
	})

	t.Run("no claude-native tier wraps ErrNoClaudeNativeTier", func(t *testing.T) {
		t.Parallel()

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		// Chain config with only ollama-local tier — no claude-native.
		const ollamaOnly = `
[chains]
"ta-go-builder" = [
  { backend = "ollama-local", model = "qwen2.5-coder:7b", opts = "", wait_max = 20, slots = 4 },
]
`
		writeChainsConfig(t, cwd, ollamaOnly)

		ctx := context.Background()
		_, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: true,
		})
		if err == nil {
			t.Fatal("expected error when chain has no claude-native tier, got nil")
		}
		if !errors.Is(err, ErrNoClaudeNativeTier) {
			t.Errorf("error must satisfy errors.Is(err, ErrNoClaudeNativeTier); got %v", err)
		}
	})
}

// installFakeClaudeEnvelope installs the fake-claude-envelope script on PATH
// and points its FAKE_CLAUDE_ENVELOPE_FILE at the named testdata JSON file
// so the wet-run Dispatch path receives an envelope that exercises a specific
// shape (happy, malformed, multi-tool, empty). t.Setenv prohibits parallel
// tests; happy-path tests in this family run serially.
func installFakeClaudeEnvelope(t *testing.T, envelopeFixture string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI seam uses /bin/sh and is not portable to windows")
	}

	srcDir := filepath.Join("testdata", "fake-claude-envelope")
	content, err := os.ReadFile(filepath.Join(srcDir, "claude.sh"))
	if err != nil {
		t.Fatalf("read fake-claude-envelope/claude.sh: %v", err)
	}

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}

	envelopePath, err := filepath.Abs(filepath.Join("testdata", envelopeFixture))
	if err != nil {
		t.Fatalf("resolve envelope fixture %s: %v", envelopeFixture, err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("FAKE_CLAUDE_ENVELOPE_FILE", envelopePath)
}

// TestDispatchHappyPath exercises the non-dry-run wet-run path: Dispatch
// spawns the fake claude CLI, ParseEnvelope decodes its stdout, and the
// returned Response carries fields populated strictly from PARSED EVENTS.
// Sub-tests cover the four canonical envelope shapes called out by the
// droplet's acceptance criteria: happy (multi-tool aggregation), malformed
// (decode error surfaces), empty (zero-length aggregates), and a custom
// multi-tool fixture (different aggregation pattern from the canonical
// happy fixture).
func TestDispatchHappyPath(t *testing.T) {
	t.Run("happy multi tool aggregation", func(t *testing.T) {
		installFakeClaudeEnvelope(t, "claude-envelope-happy.json")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)
		writeMCPConfig(t, cwd)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "wet-run test",
			CWD:    cwd,
			DryRun: false,
		})
		if err != nil {
			t.Fatalf("Dispatch wet-run: %v", err)
		}

		// Result text comes verbatim from envelope.result — narrative is
		// preserved but never used to derive tool counts.
		if !strings.Contains(resp.Result, "planner record has been amended") {
			t.Errorf("Result = %q, expected envelope result text", resp.Result)
		}

		// ServedBy reports the served tier as <backend>:<model>. The chain's
		// claude-native tier names model "haiku".
		if got, want := resp.ServedBy, "claude-native:haiku"; got != want {
			t.Errorf("ServedBy = %q, want %q", got, want)
		}

		// Tier is the 1-indexed chain position of the claude-native tier.
		// builderChainsTOML places claude-native at index 2 (ollama-local
		// is tier 1) so Fallback must be true.
		if resp.Tier != 2 {
			t.Errorf("Tier = %d, want 2 (claude-native is index 2 in builder chain)", resp.Tier)
		}
		if !resp.Fallback {
			t.Errorf("Fallback = false, want true (claude-native is not the primary tier)")
		}

		// CostUSD + DurationMs + Tokens propagate from the envelope.
		if resp.CostUSD != 0.626 {
			t.Errorf("CostUSD = %v, want 0.626", resp.CostUSD)
		}
		if resp.DurationMs != 168793 {
			t.Errorf("DurationMs = %d, want 168793", resp.DurationMs)
		}
		wantTokens := Tokens{Input: 10, Output: 13741, CacheRead: 120482, CacheCreation: 35481}
		if resp.Tokens != wantTokens {
			t.Errorf("Tokens = %#v, want %#v", resp.Tokens, wantTokens)
		}

		// ToolsUsed must be exactly the structured aggregate, in
		// name-ascending order. The happy fixture has:
		//   mcp__ta__get: 4, mcp__hylla__hylla_search: 1, mcp__ta__update: 1
		wantTools := []ToolUse{
			{Name: "mcp__hylla__hylla_search", Count: 1},
			{Name: "mcp__ta__get", Count: 4},
			{Name: "mcp__ta__update", Count: 1},
		}
		if !reflect.DeepEqual(resp.ToolsUsed, wantTools) {
			t.Errorf("ToolsUsed = %#v\nwant %#v", resp.ToolsUsed, wantTools)
		}

		// PermissionDenials in the happy envelope are empty; the slice
		// must be non-nil and zero-length so the TOON encoder renders
		// permission_denials[0]{tool,count}: without nil checks.
		if resp.PermissionDenials == nil {
			t.Errorf("PermissionDenials = nil, want empty non-nil slice")
		}
		if len(resp.PermissionDenials) != 0 {
			t.Errorf("PermissionDenials = %#v, want zero-length slice", resp.PermissionDenials)
		}
	})

	t.Run("malformed envelope returns error", func(t *testing.T) {
		installFakeClaudeEnvelope(t, "claude-envelope-malformed.json")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		_, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: false,
		})
		if err == nil {
			t.Fatal("expected error when envelope is malformed, got nil")
		}
		if !strings.Contains(err.Error(), "parse envelope") {
			t.Errorf("error must mention envelope parse failure; got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "decode envelope") {
			t.Errorf("error must wrap ParseEnvelope's decode error; got %q", err.Error())
		}
	})

	t.Run("empty tool uses returns zero length slice", func(t *testing.T) {
		installFakeClaudeEnvelope(t, "claude-envelope-empty.json")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: false,
		})
		if err != nil {
			t.Fatalf("Dispatch wet-run with empty envelope: %v", err)
		}

		if resp.ToolsUsed == nil {
			t.Errorf("ToolsUsed = nil, want empty non-nil slice for tools_used[0] TOON emission")
		}
		if len(resp.ToolsUsed) != 0 {
			t.Errorf("ToolsUsed = %#v, want zero length", resp.ToolsUsed)
		}
		if resp.PermissionDenials == nil {
			t.Errorf("PermissionDenials = nil, want empty non-nil slice")
		}
		if len(resp.PermissionDenials) != 0 {
			t.Errorf("PermissionDenials = %#v, want zero length", resp.PermissionDenials)
		}
	})

	t.Run("permission denials aggregated by tool", func(t *testing.T) {
		installFakeClaudeEnvelope(t, "claude-envelope-denials.json")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
			DryRun: false,
		})
		if err != nil {
			t.Fatalf("Dispatch wet-run with denials envelope: %v", err)
		}

		// Aggregates derive from structured event records: Read tool_use
		// = 1, permission_denial Bash = 2 (both spellings counted),
		// Edit = 1. Rows emitted in tool-ascending order.
		wantTools := []ToolUse{{Name: "Read", Count: 1}}
		if !reflect.DeepEqual(resp.ToolsUsed, wantTools) {
			t.Errorf("ToolsUsed = %#v, want %#v", resp.ToolsUsed, wantTools)
		}
		wantDenials := []PermissionDenial{
			{Tool: "Bash", Count: 2},
			{Tool: "Edit", Count: 1},
		}
		if !reflect.DeepEqual(resp.PermissionDenials, wantDenials) {
			t.Errorf("PermissionDenials = %#v\nwant %#v", resp.PermissionDenials, wantDenials)
		}
	})

	t.Run("model override flows into ServedBy", func(t *testing.T) {
		installFakeClaudeEnvelope(t, "claude-envelope-happy.json")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:          "ta-go-builder",
			Prompt:        "x",
			CWD:           cwd,
			ModelOverride: "opus",
			DryRun:        false,
		})
		if err != nil {
			t.Fatalf("Dispatch wet-run with override: %v", err)
		}
		if resp.ServedBy != "claude-native:opus" {
			t.Errorf("ServedBy = %q, want %q (override should win)", resp.ServedBy, "claude-native:opus")
		}
	})
}
