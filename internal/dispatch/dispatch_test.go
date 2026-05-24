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
	"time"

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

// defaultClaudeNativeBackendsTOML is the canonical fixture that mirrors the
// committed runClaudeNative argv contract: `-p`, `--bare`, `--model
// {{.Model}}`, `--output-format json`, `--no-session-persistence`,
// `--append-system-prompt {{.PersonaBody}}`, conditional `--mcp-config` and
// `--allowedTools`, prompt piped via stdin. Mirrors the
// fullArgsClaudeNativeTOML fixture in internal/backends/claude_native_test.go
// so the dispatch + backends tests exercise the same template surface.
const defaultClaudeNativeBackendsTOML = `
[backends.claude-native]
command = "claude"
args = [
  "-p",
  "--bare",
  "--model", "{{.Model}}",
  "--output-format", "json",
  "--no-session-persistence",
  "--append-system-prompt", "{{.PersonaBody}}",
]
env = []
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true
mcp_injection = ""
`

// writeBackendsConfig writes <cwd>/.claude/sand-backends.toml with the given
// TOML body so backends.Resolve (called from dispatch.Dispatch) finds the
// project-rung config FIRST and never falls back to HOME / XDG. The project
// rung wins per resolve.go's resolution order, which makes this safe for
// t.Parallel() tests — no env mutation needed because the resolver
// short-circuits before it touches XDG_CONFIG_HOME or HOME.
func writeBackendsConfig(t *testing.T, cwd, body string) {
	t.Helper()
	dir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup backends dir: %v", err)
	}
	path := filepath.Join(dir, "sand-backends.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write backends config %s: %v", path, err)
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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)
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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)
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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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
//
// The parent test is intentionally NOT t.Parallel: one subtest
// (missing_chains_config_errors) calls t.Setenv to isolate from the
// developer's real ~/.config/sand/chains.toml, and Go forbids t.Setenv in
// any subtest whose ancestor is parallel. Siblings that want parallelism
// can still call t.Parallel() themselves and will run concurrently with
// each other.
func TestDispatchSelectionErrors(t *testing.T) {
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
		// Cannot t.Parallel: t.Setenv mutates process-global env vars to
		// isolate the test from the developer's real ~/.config/sand/chains.toml
		// (drop_011's hierarchical resolver walks past CWD into HOME).
		// Without this isolation, the seeded baseline chains.toml landed by
		// `mage install` makes Resolve succeed and breaks the "no chains
		// config" contract this test exists to pin.

		// NOTE: dispatch.loadChainsConfig migrated to chains.Resolve in
		// drop_008. The drop_003 contract (os.ErrNotExist substring
		// matched) is preserved by wrapping chains.ErrChainConfigNotFound
		// with errors.Join(os.ErrNotExist) — see loadChainsConfig.
		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		// No sand-chains.toml.

		// Pin HOME + XDG to empty tempdirs so the hierarchical resolver
		// finds nothing at any rung and surfaces ErrChainConfigNotFound.
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")

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
		// drop_005 L3 amendment B4: the pre-loop ErrNoClaudeNativeTier guard
		// now lives inside the DryRun branch — wet-run lifted it so codex-only
		// and ollama-only chains can resolve per-tier. Dry-run still applies
		// the guard so the Preview picks a claude-native model deterministically.
		if !errors.Is(err, ErrNoClaudeNativeTier) {
			t.Errorf("error must satisfy errors.Is(err, ErrNoClaudeNativeTier); got %v", err)
		}
	})

	t.Run("wet-run no claude-native tier exhausts chain", func(t *testing.T) {
		t.Parallel()

		// Sibling of the dry-run case above: drop_005 L3 amendment B4 lifted
		// the pre-loop ErrNoClaudeNativeTier guard from the wet-run path.
		// A chain made entirely of tiers sand can't resolve (here ollama-local
		// has no [backends.ollama-local] entry in the project backends.toml)
		// now records per-tier Attempt{Outcome:"unsupported_backend"} until
		// the loop exhausts the chain and returns ErrChainExhausted.
		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		const ollamaOnly = `
[chains]
"ta-go-builder" = [
  { backend = "ollama-local", model = "qwen2.5-coder:7b", opts = "", wait_max = 0, slots = 0 },
]
`
		writeChainsConfig(t, cwd, ollamaOnly)
		// backends.toml has ONLY claude-native — Resolve("ollama-local") will
		// fail with backends.ErrUnknownBackend, which the dispatch loop
		// classifies as "unsupported_backend" + advances.
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		ctx := context.Background()
		resp, err := Dispatch(ctx, Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err == nil {
			t.Fatalf("expected ErrChainExhausted on wet-run with no resolvable tier, got nil; resp=%#v", resp)
		}
		if !errors.Is(err, ErrChainExhausted) {
			t.Errorf("err must satisfy errors.Is(err, ErrChainExhausted); got %v", err)
		}
		if errors.Is(err, ErrNoClaudeNativeTier) {
			t.Errorf("err must NOT satisfy ErrNoClaudeNativeTier in wet-run path (guard was lifted); got %v", err)
		}
		if len(resp.FallbackChain) != 1 {
			t.Fatalf("FallbackChain len = %d, want 1 (single ollama-local tier recorded); chain=%#v",
				len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "unsupported_backend" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q",
				resp.FallbackChain[0].Outcome, "unsupported_backend")
		}
		if resp.FallbackChain[0].Backend != "ollama-local" {
			t.Errorf("FallbackChain[0].Backend = %q, want %q",
				resp.FallbackChain[0].Backend, "ollama-local")
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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)
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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

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

// installFakeClaudeSequence installs the fake-claude-sequence script on PATH
// and primes FAKE_CLAUDE_FIXTURE_SEQUENCE + FAKE_CLAUDE_INVOCATION_FILE so a
// single test can drive multi-tier behavior across consecutive runTier spawns.
// The sequence is a comma-separated list of fixture names (rate-limit,
// auth-fail, timeout, network, crash, success); each invocation of the fake
// CLI consumes the next entry.
//
// t.Setenv prohibits parallel tests so failover sub-tests run serially.
func installFakeClaudeSequence(t *testing.T, sequence string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI seam uses /bin/sh and is not portable to windows")
	}

	srcDir := filepath.Join("testdata", "fake-claude-sequence")
	content, err := os.ReadFile(filepath.Join(srcDir, "claude.sh"))
	if err != nil {
		t.Fatalf("read fake-claude-sequence/claude.sh: %v", err)
	}

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}

	// Fresh per-test invocation counter file so sub-tests do not see each
	// other's accumulated invocations.
	invocationFile := filepath.Join(t.TempDir(), "invocations")
	if err := os.WriteFile(invocationFile, nil, 0o644); err != nil {
		t.Fatalf("seed invocation counter file: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("FAKE_CLAUDE_FIXTURE_SEQUENCE", sequence)
	t.Setenv("FAKE_CLAUDE_INVOCATION_FILE", invocationFile)
}

// failoverChainsTOML is a two-tier claude-native chain used by the failover
// tests. Both tiers spawn the same fake CLI; the test drives the per-tier
// behavior by sequencing fixture names. Slots are 0 (unlimited) so neither
// tier touches /tmp/sand-slots — slot semantics are covered by the slots
// package tests, not here.
const failoverChainsTOML = `
[chains]
"ta-go-builder" = [
  { backend = "claude-native", model = "haiku",  opts = "", wait_max = 0, slots = 0 },
  { backend = "claude-native", model = "sonnet", opts = "", wait_max = 0, slots = 0 },
]
`

// TestDispatchFailoverChain exercises the new SAND-V02-SPEC §1.4 fallback
// loop: tier-1 fails with a classifiable error, tier-2 succeeds, and the
// Response carries a FallbackChain with both attempts recorded.
//
// Each sub-test serializes the per-tier behavior through
// installFakeClaudeSequence so the fake CLI returns different outcomes on
// consecutive invocations within a single Dispatch call.
func TestDispatchFailoverChain(t *testing.T) {
	t.Run("rate_limit_advances_to_next_tier", func(t *testing.T) {
		installFakeClaudeSequence(t, "rate-limit,success")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, failoverChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err != nil {
			t.Fatalf("Dispatch with rate-limit fallback: %v", err)
		}
		if resp.Result != "failover succeeded" {
			t.Errorf("Result = %q, want %q", resp.Result, "failover succeeded")
		}
		if resp.ServedBy != "claude-native:sonnet" {
			t.Errorf("ServedBy = %q, want %q (tier 2 served after tier 1 rate-limit)", resp.ServedBy, "claude-native:sonnet")
		}
		if resp.Tier != 2 {
			t.Errorf("Tier = %d, want 2", resp.Tier)
		}
		if !resp.Fallback {
			t.Errorf("Fallback = false, want true")
		}
		if len(resp.FallbackChain) != 2 {
			t.Fatalf("FallbackChain len = %d, want 2; chain=%#v", len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "rate_limit" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "rate_limit")
		}
		if resp.FallbackChain[0].Tier != 1 || resp.FallbackChain[0].Backend != "claude-native" || resp.FallbackChain[0].Model != "haiku" {
			t.Errorf("FallbackChain[0] tier/backend/model = %d/%q/%q; want 1/claude-native/haiku",
				resp.FallbackChain[0].Tier, resp.FallbackChain[0].Backend, resp.FallbackChain[0].Model)
		}
		if resp.FallbackChain[0].Reason == "" {
			t.Errorf("FallbackChain[0].Reason is empty; want stderr summary")
		}
		if resp.FallbackChain[0].AttemptedAt.IsZero() {
			t.Errorf("FallbackChain[0].AttemptedAt is zero time; want non-zero stamp")
		}
		if resp.FallbackChain[1].Outcome != "success" {
			t.Errorf("FallbackChain[1].Outcome = %q, want %q", resp.FallbackChain[1].Outcome, "success")
		}
		if resp.FallbackChain[1].Tier != 2 || resp.FallbackChain[1].Backend != "claude-native" || resp.FallbackChain[1].Model != "sonnet" {
			t.Errorf("FallbackChain[1] tier/backend/model = %d/%q/%q; want 2/claude-native/sonnet",
				resp.FallbackChain[1].Tier, resp.FallbackChain[1].Backend, resp.FallbackChain[1].Model)
		}
	})

	t.Run("auth_failure_advances_to_next_tier", func(t *testing.T) {
		installFakeClaudeSequence(t, "auth-fail,success")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, failoverChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err != nil {
			t.Fatalf("Dispatch with auth-fail fallback: %v", err)
		}
		if resp.Tier != 2 {
			t.Errorf("Tier = %d, want 2", resp.Tier)
		}
		if len(resp.FallbackChain) != 2 {
			t.Fatalf("FallbackChain len = %d, want 2", len(resp.FallbackChain))
		}
		if resp.FallbackChain[0].Outcome != "auth_failure" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "auth_failure")
		}
		if resp.FallbackChain[1].Outcome != "success" {
			t.Errorf("FallbackChain[1].Outcome = %q, want %q", resp.FallbackChain[1].Outcome, "success")
		}
	})

	t.Run("all_tiers_fail_returns_ErrChainExhausted", func(t *testing.T) {
		installFakeClaudeSequence(t, "rate-limit,timeout")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, failoverChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err == nil {
			t.Fatalf("expected ErrChainExhausted, got nil err with resp=%#v", resp)
		}
		if !errors.Is(err, ErrChainExhausted) {
			t.Errorf("err must satisfy errors.Is(err, ErrChainExhausted); got %v", err)
		}
		if len(resp.FallbackChain) != 2 {
			t.Fatalf("FallbackChain len = %d, want 2 (both attempts recorded on exhaustion); chain=%#v",
				len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "rate_limit" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "rate_limit")
		}
		if resp.FallbackChain[1].Outcome != "timeout" {
			t.Errorf("FallbackChain[1].Outcome = %q, want %q", resp.FallbackChain[1].Outcome, "timeout")
		}
		// Success-path fields must be zero on exhaustion — the dispatch
		// never produced a winning Response.
		if resp.Result != "" {
			t.Errorf("Result = %q, want empty on exhaustion", resp.Result)
		}
		if resp.ServedBy != "" {
			t.Errorf("ServedBy = %q, want empty on exhaustion", resp.ServedBy)
		}
		if resp.Tier != 0 {
			t.Errorf("Tier = %d, want 0 on exhaustion", resp.Tier)
		}
	})

	t.Run("crash_halts_chain_with_partial_FallbackChain", func(t *testing.T) {
		// First tier crashes (exit 137, empty stderr -> ErrClassCrash).
		// Per SAND-V02-SPEC §3.3, Crash is unrecoverable: the loop must
		// halt mid-walk and surface a wrapped error WITHOUT exhausting
		// the remaining tiers. Sequence only needs the first entry; the
		// second is never consumed.
		installFakeClaudeSequence(t, "crash,success")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, failoverChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err == nil {
			t.Fatalf("expected unrecoverable error on crash, got nil; resp=%#v", resp)
		}
		if errors.Is(err, ErrChainExhausted) {
			t.Errorf("crash should HALT (not exhaust) the chain; got ErrChainExhausted: %v", err)
		}
		if len(resp.FallbackChain) != 1 {
			t.Fatalf("FallbackChain len = %d, want 1 (chain halted after first tier crashed); chain=%#v",
				len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "crash" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "crash")
		}
	})

	t.Run("unsupported_backend_tier_records_attempt_and_advances", func(t *testing.T) {
		// Chain: ollama-local first (unsupported in drop_003/008 — runTier
		// rejects with ErrUnsupportedBackend) then claude-native. The
		// fake CLI is only consumed once (for the claude-native tier).
		installFakeClaudeSequence(t, "success")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, builderChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err != nil {
			t.Fatalf("Dispatch with unsupported-backend advance: %v", err)
		}
		if resp.Tier != 2 {
			t.Errorf("Tier = %d, want 2", resp.Tier)
		}
		if len(resp.FallbackChain) != 2 {
			t.Fatalf("FallbackChain len = %d, want 2; chain=%#v", len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "unsupported_backend" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "unsupported_backend")
		}
		if resp.FallbackChain[0].Backend != "ollama-local" {
			t.Errorf("FallbackChain[0].Backend = %q, want %q", resp.FallbackChain[0].Backend, "ollama-local")
		}
		if resp.FallbackChain[1].Outcome != "success" {
			t.Errorf("FallbackChain[1].Outcome = %q, want %q", resp.FallbackChain[1].Outcome, "success")
		}
	})
}

// TestDispatchFailoverChainRetryOn exercises the drop_009 user-configurable
// per-tier retry_on override of the default outcome→action policy.
//
// Two sub-tests pin the contract:
//
//   - retry_on_overrides_default_advance: a tier sets retry_on=["rate_limit"]
//     but the spawn produces a network-class outcome. Under the DEFAULT
//     policy network would advance the chain; under the retry_on whitelist
//     network is NOT a member and the chain must HALT mid-walk with a
//     wrapped error and a partial FallbackChain of length 1.
//   - retry_on_advances_on_whitelisted: a tier sets retry_on=["rate_limit"]
//     and the spawn produces a rate_limit outcome. The chain MUST advance
//     to the next tier and the second tier succeeds. The recorded
//     FallbackChain pins both attempts.
func TestDispatchFailoverChainRetryOn(t *testing.T) {
	// retryOnAdvanceChainsTOML opts tier 1 into retry_on=["rate_limit"];
	// tier 2 is the default-policy fallthrough so the override only fires
	// on the first tier. Both tiers use claude-native + slots=0 (unlimited)
	// so the fake-CLI seam drives all per-tier outcomes deterministically.
	const retryOnAdvanceChainsTOML = `
[chains]
"ta-go-builder" = [
  { backend = "claude-native", model = "haiku",  opts = "", wait_max = 0, slots = 0, retry_on = ["rate_limit"] },
  { backend = "claude-native", model = "sonnet", opts = "", wait_max = 0, slots = 0 },
]
`

	t.Run("retry_on_overrides_default_advance", func(t *testing.T) {
		// Sequence has only one entry — under the retry_on whitelist the
		// chain MUST halt after tier 1's network outcome. The fake CLI
		// is never invoked a second time; if it were the missing second
		// fixture would exit 2 and surface as a different error.
		installFakeClaudeSequence(t, "network")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, retryOnAdvanceChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err == nil {
			t.Fatalf("expected halt-by-retry_on error, got nil; resp=%#v", resp)
		}
		if errors.Is(err, ErrChainExhausted) {
			t.Errorf("retry_on halt should NOT exhaust the chain; got ErrChainExhausted: %v", err)
		}
		if !strings.Contains(err.Error(), "retry_on policy") {
			t.Errorf("error message should mention retry_on policy; got %q", err.Error())
		}
		if len(resp.FallbackChain) != 1 {
			t.Fatalf("FallbackChain len = %d, want 1 (chain halted under retry_on whitelist); chain=%#v",
				len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "network" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "network")
		}
		if resp.FallbackChain[0].Reason == "" {
			t.Errorf("FallbackChain[0].Reason is empty; want stderr summary")
		}
		if resp.Tier != 0 {
			t.Errorf("Tier = %d, want 0 on retry_on halt", resp.Tier)
		}
	})

	t.Run("retry_on_advances_on_whitelisted", func(t *testing.T) {
		installFakeClaudeSequence(t, "rate-limit,success")

		cwd := t.TempDir()
		writePersona(t, cwd, "ta-go-builder", "BODY", []string{"Read"}, "haiku")
		writeChainsConfig(t, cwd, retryOnAdvanceChainsTOML)
		writeBackendsConfig(t, cwd, defaultClaudeNativeBackendsTOML)

		resp, err := Dispatch(context.Background(), Params{
			Role:   "ta-go-builder",
			Prompt: "x",
			CWD:    cwd,
		})
		if err != nil {
			t.Fatalf("Dispatch with retry_on advance: %v", err)
		}
		if resp.Tier != 2 {
			t.Errorf("Tier = %d, want 2 (tier 2 served after retry_on-whitelisted rate_limit on tier 1)", resp.Tier)
		}
		if resp.ServedBy != "claude-native:sonnet" {
			t.Errorf("ServedBy = %q, want %q", resp.ServedBy, "claude-native:sonnet")
		}
		if len(resp.FallbackChain) != 2 {
			t.Fatalf("FallbackChain len = %d, want 2; chain=%#v", len(resp.FallbackChain), resp.FallbackChain)
		}
		if resp.FallbackChain[0].Outcome != "rate_limit" {
			t.Errorf("FallbackChain[0].Outcome = %q, want %q", resp.FallbackChain[0].Outcome, "rate_limit")
		}
		if resp.FallbackChain[1].Outcome != "success" {
			t.Errorf("FallbackChain[1].Outcome = %q, want %q", resp.FallbackChain[1].Outcome, "success")
		}
	})
}

// TestDispatchSentinelErrChainExhausted pins ErrChainExhausted's surface area
// alongside the other dispatch sentinels (TestDispatchSentinels covers the
// pre-existing trio; this sub-test adds the v0.2 sentinel without re-running
// the full distinctness matrix).
func TestDispatchSentinelErrChainExhausted(t *testing.T) {
	t.Parallel()

	if ErrChainExhausted == nil {
		t.Fatal("ErrChainExhausted is nil; expected initialised package-level error")
	}
	if ErrChainExhausted.Error() == "" {
		t.Errorf("ErrChainExhausted.Error() is empty; want diagnostic message")
	}

	wrapped := fmt.Errorf("dispatch test: %w", ErrChainExhausted)
	if !errors.Is(wrapped, ErrChainExhausted) {
		t.Errorf("errors.Is(wrapped, ErrChainExhausted) = false; want true via %%w")
	}

	// Cross-match: ErrChainExhausted must not collapse onto the existing
	// sentinels.
	for _, other := range []error{ErrUnsupportedBackend, ErrRoleNotInChains, ErrNoClaudeNativeTier} {
		if errors.Is(ErrChainExhausted, other) {
			t.Errorf("ErrChainExhausted should not errors.Is-match %v", other)
		}
		if errors.Is(other, ErrChainExhausted) {
			t.Errorf("%v should not errors.Is-match ErrChainExhausted", other)
		}
	}
}

// TestResponseV02Fields exercises the v0.2 fields added to Response:
// FallbackChain []Attempt and ToolCalls []ToolCall. Verifies the field names,
// element-type field names, and zero-value behavior so the sibling
// toon_extension droplet has a stable contract to compile against.
func TestResponseV02Fields(t *testing.T) {
	t.Parallel()

	var r Response
	if r.FallbackChain != nil {
		t.Errorf("Response.FallbackChain zero value = %#v, want nil slice", r.FallbackChain)
	}
	if r.ToolCalls != nil {
		t.Errorf("Response.ToolCalls zero value = %#v, want nil slice", r.ToolCalls)
	}

	now := time.Date(2026, 5, 22, 1, 30, 0, 0, time.UTC)
	r = Response{
		FallbackChain: []Attempt{
			{Tier: 1, Backend: "ollama-cloud", Model: "qwen3-coder-cloud-235b", AttemptedAt: now, Outcome: "slot_timeout", Reason: "all 3 slots busy for 10s"},
			{Tier: 2, Backend: "claude-native", Model: "opus", AttemptedAt: now.Add(15 * time.Second), Outcome: "success"},
		},
		ToolCalls: []ToolCall{
			{Index: 1, Name: "Read", DurationMs: 12, IsError: false},
			{Index: 2, Name: "Bash", DurationMs: 234, IsError: true},
		},
	}

	if len(r.FallbackChain) != 2 {
		t.Fatalf("FallbackChain len = %d, want 2", len(r.FallbackChain))
	}
	if r.FallbackChain[0].Outcome != "slot_timeout" {
		t.Errorf("FallbackChain[0].Outcome = %q, want %q", r.FallbackChain[0].Outcome, "slot_timeout")
	}
	if r.FallbackChain[0].Reason != "all 3 slots busy for 10s" {
		t.Errorf("FallbackChain[0].Reason = %q, want %q", r.FallbackChain[0].Reason, "all 3 slots busy for 10s")
	}
	if !r.FallbackChain[0].AttemptedAt.Equal(now) {
		t.Errorf("FallbackChain[0].AttemptedAt = %v, want %v", r.FallbackChain[0].AttemptedAt, now)
	}
	if r.FallbackChain[1].Outcome != "success" {
		t.Errorf("FallbackChain[1].Outcome = %q, want %q", r.FallbackChain[1].Outcome, "success")
	}

	if len(r.ToolCalls) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(r.ToolCalls))
	}
	if r.ToolCalls[0] != (ToolCall{Index: 1, Name: "Read", DurationMs: 12, IsError: false}) {
		t.Errorf("ToolCalls[0] = %#v", r.ToolCalls[0])
	}
	if r.ToolCalls[1] != (ToolCall{Index: 2, Name: "Bash", DurationMs: 234, IsError: true}) {
		t.Errorf("ToolCalls[1] = %#v", r.ToolCalls[1])
	}
}
