package preflight

// Tests for the sand.preflight MCP tool wrapper (preflight_tool.go).
//
// Coverage matches drop_006.drop.build_preflight_tool acceptance criteria:
//   - role argument is required; missing or whitespace-only role returns an
//     MCP/tool error WITHOUT touching the Probe (asserted via a panicProbe);
//   - the handler reads `<projectDir>/.claude/sand-chains.toml` and surfaces
//     missing-file errors as IsError=true results;
//   - happy path: a populated chain produces the SAND-SPEC §3.2 TOON shape
//     byte-for-byte, with empty `reason` cells emitted as bare empty CSV
//     fields (not "") and the `role` top-level scalar echoing the input;
//   - ollama daemon-down + ollama model-not-pulled + claude CLI missing all
//     surface as ok=false rows with reason populated;
//   - tests rely only on the in-package stubProbe defined in preflight_test.go
//     so no real claude/codex/ollama binaries or daemons are required.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// happyChainsTOML is the chain-config fixture used by the happy-path test.
// It declares a builder-shaped chain plus a two-tier "tier-failures" chain
// used by the ollama-daemon-down / model-missing cases. Layout matches the
// inline-table-array form established in
// internal/debugtools/chains_list_test.go so hyphenated role keys decode
// correctly under chains.Parse's strict mode.
const happyChainsTOML = `[chains]
"ta-go-builder" = [
  { backend = "ollama-local",  model = "qwen2.5-coder:7b", opts = "",                                                        wait_max = 20, slots = 4 },
  { backend = "codex-exec",    model = "gpt-5.5",          opts = "--sandbox workspace-write -c model_reasoning_effort=low", wait_max = 0,  slots = 0 },
  { backend = "claude-native", model = "haiku",            opts = "",                                                        wait_max = 0,  slots = 0 },
]

"tier-failures" = [
  { backend = "ollama-local",  model = "qwen2.5-coder:7b", opts = "", wait_max = 0, slots = 0 },
  { backend = "claude-native", model = "opus",             opts = "", wait_max = 0, slots = 0 },
]
`

// writeChainsFixture writes content to
// <projectDir>/.claude/sand-chains.toml so the handler under test can
// resolve it via filepath.Join. Uses t.TempDir() pattern from
// debugtools/persona_get_test.go.
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

// callPreflight invokes the handler with a CallToolRequest carrying the
// supplied arguments and returns the resulting CallToolResult. Returning
// the result lets each test assert on IsError and content independently.
func callPreflight(t *testing.T, handler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "sand.preflight"
	req.Params.Arguments = args

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if res == nil {
		t.Fatalf("handler returned nil result with nil error")
	}
	return res
}

// resultText returns the concatenated text content of res. Mirrors textOf in
// debugtools/persona_get_test.go so the assertion style is consistent across
// the two test packages.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// panicProbe is a Probe stand-in that fails the test if any of its methods
// is invoked. It is the canonical way to assert "the handler must NOT probe
// in this case" — for example when the role argument is missing and the
// handler should short-circuit with an MCP error.
type panicProbe struct {
	t *testing.T
}

func (p *panicProbe) LookPath(name string) (string, error) {
	p.t.Fatalf("panicProbe.LookPath called with %q; probe should not have been invoked", name)
	return "", nil
}

func (p *panicProbe) HTTPGet(ctx context.Context, url string) (*http.Response, error) {
	p.t.Fatalf("panicProbe.HTTPGet called with %q; probe should not have been invoked", url)
	return nil, nil
}

func (p *panicProbe) OllamaList(ctx context.Context) (string, error) {
	p.t.Fatalf("panicProbe.OllamaList called; probe should not have been invoked")
	return "", nil
}

// TestPreflightToolMissingRole verifies that a CallToolRequest without a
// role argument returns IsError=true and never touches the Probe. The
// mcp.WithString(..., mcp.Required()) declaration makes req.RequireString
// fail before the handler dereferences the probe.
func TestPreflightToolMissingRole(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	_, handler := NewToolWithProbe(projectDir, &panicProbe{t: t})

	// No "role" key in arguments at all.
	res := callPreflight(t, handler, map[string]any{})
	if !res.IsError {
		t.Fatalf("expected IsError=true when role missing; got success: %s", resultText(t, res))
	}
}

// TestPreflightToolEmptyRole verifies the explicit empty-string and
// whitespace-only role guards: the handler must reject these BEFORE
// constructing a chain or invoking the probe. Without this guard a
// whitespace-only role would slip past mcp.Required (which only checks for
// presence) and trigger a chain-config lookup with key " ".
func TestPreflightToolEmptyRole(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	_, handler := NewToolWithProbe(projectDir, &panicProbe{t: t})

	tests := []struct {
		name string
		role string
	}{
		{name: "empty string", role: ""},
		{name: "whitespace only", role: "   "},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := callPreflight(t, handler, map[string]any{"role": tc.role})
			if !res.IsError {
				t.Fatalf("expected IsError=true for role=%q; got success: %s", tc.role, resultText(t, res))
			}
		})
	}
}

// TestPreflightToolChainConfigMissing verifies the chain-config-not-found
// branch: the handler must surface IsError=true with a message naming the
// missing path. The probe must not be invoked because chain lookup fails
// before any tier is walked.
func TestPreflightToolChainConfigMissing(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	// Deliberately do NOT write a fixture.

	_, handler := NewToolWithProbe(projectDir, &panicProbe{t: t})
	res := callPreflight(t, handler, map[string]any{"role": "ta-go-builder"})
	if !res.IsError {
		t.Fatalf("expected IsError=true when chain config missing; got success: %s", resultText(t, res))
	}
	msg := resultText(t, res)
	wantSubstrings := []string{
		"chain config not found",
		filepath.Join(projectDir, ".claude", "sand-chains.toml"),
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(msg, w) {
			t.Errorf("error message missing substring %q\nfull message:\n%s", w, msg)
		}
	}
}

// TestPreflightToolUnknownRole verifies that a role absent from the parsed
// chain config produces IsError=true with the role name in the message. The
// probe must not be invoked.
func TestPreflightToolUnknownRole(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	_, handler := NewToolWithProbe(projectDir, &panicProbe{t: t})
	res := callPreflight(t, handler, map[string]any{"role": "missing-role"})
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown role; got success: %s", resultText(t, res))
	}
	if msg := resultText(t, res); !strings.Contains(msg, "missing-role") {
		t.Errorf("error message missing role name; got %q", msg)
	}
}

// TestPreflightToolHappyTOON exercises the e2e happy path: a fully passing
// builder chain produces the SAND-SPEC §3.2 TOON shape byte-for-byte. The
// expected output pins:
//   - top-level `role: <string>` scalar echoing the input role;
//   - tiers[N]{tier,backend,model,ok,reason}: header with the EXACT field
//     order from SAND-SPEC §3.2 lines 131-137;
//   - bare empty CSV cell for the reason column when OK=true (NOT "");
//   - tier index 1-based, in chain order.
func TestPreflightToolHappyTOON(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: stubHTTPGet200(`{"version":"0.1.34"}`),
		ollamaList: func(ctx context.Context) (string, error) {
			return "NAME                  ID      SIZE\nqwen2.5-coder:7b      abc     4.7 GB\n", nil
		},
	}

	_, handler := NewToolWithProbe(projectDir, probe)
	res := callPreflight(t, handler, map[string]any{"role": "ta-go-builder"})
	if res.IsError {
		t.Fatalf("expected IsError=false on happy path; got error: %s", resultText(t, res))
	}

	got := resultText(t, res)
	want := "role: ta-go-builder\n" +
		"tiers[3]{tier,backend,model,ok,reason}:\n" +
		"  1,ollama-local,qwen2.5-coder:7b,true,\n" +
		"  2,codex-exec,gpt-5.5,true,\n" +
		"  3,claude-native,haiku,true,\n"
	if got != want {
		t.Fatalf("TOON output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestPreflightToolMixedFailures exercises the canonical mixed-outcome case
// from SAND-SPEC §3.2's example: tier 1 OK, tier 2 failing, tier 3 OK. The
// failing row carries a populated reason field (no quoting because the
// reason has no embedded comma/quote/newline), confirming the TOON shape
// handles both bare-empty and bare-populated reason cells in the same call.
func TestPreflightToolMixedFailures(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			// claude present, codex missing.
			if name == "codex" {
				return "", errors.New("executable file not found in $PATH")
			}
			return "/usr/local/bin/" + name, nil
		},
		httpGet: stubHTTPGet200(`{"version":"0.1.34"}`),
		ollamaList: func(ctx context.Context) (string, error) {
			return "NAME                  ID      SIZE\nqwen2.5-coder:7b      abc     4.7 GB\n", nil
		},
	}

	_, handler := NewToolWithProbe(projectDir, probe)
	res := callPreflight(t, handler, map[string]any{"role": "ta-go-builder"})
	if res.IsError {
		t.Fatalf("expected IsError=false on mixed outcomes; got error: %s", resultText(t, res))
	}

	got := resultText(t, res)
	// Tier 1 (ollama OK) + Tier 3 (claude OK) emit bare empty reason cells;
	// Tier 2 (codex missing) emits a populated reason cell. The exact
	// message text is governed by checkCLI in preflight.go.
	want := "role: ta-go-builder\n" +
		"tiers[3]{tier,backend,model,ok,reason}:\n" +
		"  1,ollama-local,qwen2.5-coder:7b,true,\n" +
		"  2,codex-exec,gpt-5.5,false,codex CLI not on PATH: executable file not found in $PATH\n" +
		"  3,claude-native,haiku,true,\n"
	if got != want {
		t.Fatalf("TOON output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestPreflightToolOllamaDaemonDown verifies the ollama-daemon-unreachable
// path surfaces ok=false with a reason naming the daemon. The downstream
// claude tier still emits an OK row because the chain walk does NOT
// short-circuit on earlier failures (Preflight reports every tier).
func TestPreflightToolOllamaDaemonDown(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: func(ctx context.Context, url string) (*http.Response, error) {
			return nil, errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
		},
	}

	_, handler := NewToolWithProbe(projectDir, probe)
	res := callPreflight(t, handler, map[string]any{"role": "tier-failures"})
	if res.IsError {
		t.Fatalf("expected IsError=false (probes report per-tier); got error: %s", resultText(t, res))
	}

	got := resultText(t, res)
	if !strings.Contains(got, "1,ollama-local,qwen2.5-coder:7b,false,") {
		t.Errorf("expected ollama tier to render ok=false with populated reason\nfull output:\n%s", got)
	}
	if !strings.Contains(got, "ollama daemon unreachable") {
		t.Errorf("expected reason to name daemon unreachable\nfull output:\n%s", got)
	}
	if !strings.Contains(got, "2,claude-native,opus,true,\n") {
		t.Errorf("expected downstream claude tier to render ok=true (chain not short-circuited)\nfull output:\n%s", got)
	}
}

// TestPreflightToolOllamaModelMissing verifies the model-not-pulled path:
// daemon is reachable but `ollama list` does not include the configured
// model. The reason contains the model name in quotes (per checkOllamaModel),
// which embeds a comma-free message but contains `"` characters — toon must
// quote those, but since they are inside the reason value rendered as a
// CSV cell, the reason cell is rendered with TOON CSV quoting rules.
//
// We assert via substring rather than byte-for-byte because the reason
// string contains `"` which toon's CSV quoting wraps in additional quotes,
// and the precise output is more legible to assert by structure than by
// exact bytes.
func TestPreflightToolOllamaModelMissing(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: stubHTTPGet200(`{"version":"0.1.34"}`),
		ollamaList: func(ctx context.Context) (string, error) {
			return "NAME       ID      SIZE\nllama3:8b  def     4.7 GB\n", nil
		},
	}

	_, handler := NewToolWithProbe(projectDir, probe)
	res := callPreflight(t, handler, map[string]any{"role": "tier-failures"})
	if res.IsError {
		t.Fatalf("expected IsError=false; got error: %s", resultText(t, res))
	}

	got := resultText(t, res)
	// Verify the ollama-local tier is ok=false. The model-name part of the
	// reason includes `"` so the reason cell is CSV-quoted by toon; assert
	// only the not-pulled diagnostic substring rather than the precise byte
	// sequence.
	if !strings.Contains(got, "1,ollama-local,qwen2.5-coder:7b,false,") {
		t.Errorf("ollama tier did not render ok=false\nfull output:\n%s", got)
	}
	if !strings.Contains(got, "not pulled locally") {
		t.Errorf("reason missing 'not pulled locally' diagnostic\nfull output:\n%s", got)
	}
}

// TestPreflightToolEmitsRoleHeader pins the §3.2-mandated top-level role
// scalar separately from the tiers header. A previous (rejected) shape
// dropped the role field entirely; this assertion guards against that
// regression even when the rest of the output drifts in ways the
// byte-for-byte tests would also catch.
func TestPreflightToolEmitsRoleHeader(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeChainsFixture(t, projectDir, happyChainsTOML)

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: stubHTTPGet200(`{"version":"0.1.34"}`),
		ollamaList: func(ctx context.Context) (string, error) {
			return "NAME                  ID      SIZE\nqwen2.5-coder:7b      abc     4.7 GB\n", nil
		},
	}

	_, handler := NewToolWithProbe(projectDir, probe)
	res := callPreflight(t, handler, map[string]any{"role": "ta-go-builder"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res))
	}
	got := resultText(t, res)
	if !strings.HasPrefix(got, "role: ta-go-builder\n") {
		t.Errorf("output did not begin with role scalar\nfull output:\n%s", got)
	}
	if !strings.Contains(got, "\ntiers[3]{tier,backend,model,ok,reason}:\n") {
		t.Errorf("tiers header missing or malformed\nfull output:\n%s", got)
	}
}

// stubHTTPGet200 returns an httpGet function that produces a 200 response
// with the supplied body, regardless of url. Wraps the local httpRespOK
// helper so test cases can declare the expected payload inline.
func stubHTTPGet200(body string) func(ctx context.Context, url string) (*http.Response, error) {
	return func(ctx context.Context, url string) (*http.Response, error) {
		return httpRespOK(body), nil
	}
}
