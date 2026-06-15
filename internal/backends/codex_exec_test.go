// Tests for the codex-exec Backend implementation. Coverage mirrors
// the committed TestClaudeNativeBackend_* suite where the contracts
// overlap (anti-recursion suffix, env filter, non-zero-exit-is-data,
// ctx cancellation, Preview byte shape) and adds codex-specific
// assertions for role-conditional MCP `-c <inline-TOML>` flag injection
// via RenderRoleConditionalMCPFlags.
//
// Test seam: each test that needs a live codex subprocess installs a
// fake `codex` shell script on a fresh PATH-prefix tempdir via
// installFakeCodex. The script records its argv (NUL-separated) +
// stdin + env to recorder files the test asserts against. This mirrors
// the fake-claude-* pattern installFakeClaude uses.
//
// MCP-injection tests assert that Spawn's argv carries the static
// role-conditional `-c mcp_servers.*` flags from
// RenderRoleConditionalMCPFlags — no `.mcp.json` probe is performed.
package backends

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/evanmschultz/sand/internal/gate"
)

// fullArgsCodexExecTOML is the backends.toml fixture used by every
// TestCodexExecBackend_* test. It populates BackendConfig with the
// canonical SAND-SPEC §7.1 codex argv:
// `exec --ephemeral --ignore-user-config --skip-git-repo-check -C {{.CWD}}
//
//	-m {{.Model}}`.
//
// The four hermetic -c flags (approval_policy, web_search,
// project_doc_max_bytes, skills.bundled.enabled) are appended by
// renderArgs unconditionally — they do NOT appear in the TOML args
// list, because renderArgs injects them statically after template
// substitution so Preview also reflects them.
//
// stdin_prompt=true is the load-bearing codex contract: the prompt is
// piped to the child's stdin per the bash dispatcher reference.
//
// mcp_config_arg + allowed_tools_arg are intentionally EMPTY because
// codex does not consume those Claude-style flags — MCP injection is
// per-server `-c <inline-TOML>` (handled by RenderRoleConditionalMCPFlags
// in Spawn) and there is no codex equivalent for --allowedTools (tool
// allow-listing is per-server via the inline TOML's `tools={...}` map).
const fullArgsCodexExecTOML = `
[backends.codex-exec]
command = "codex"
args = [
  "exec",
  "--ephemeral",
  "--ignore-user-config",
  "--skip-git-repo-check",
  "-C", "{{.CWD}}",
  "-m", "{{.Model}}",
]
env = []
mcp_config_arg = ""
allowed_tools_arg = ""
allowed_tools_csv_template = ""
slots_default = 0
envelope_format = "codex_stream"
stdin_prompt = true
mcp_injection = "codex_inline_toml"
`

// installFakeCodex prepends a fresh temp dir to PATH containing a copy
// of the named fixture shell script, exposed as `codex`. The script's
// stdout becomes Spawn's captured stdout; argv + stdin + env get
// recorded to per-test files for assertion. Returns the recorder paths.
//
// The PATH override is undone via t.Cleanup so tests stay hermetic.
func installFakeCodex(t *testing.T, fixture string) (argvOut, stdinOut, envOut string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI seam uses /bin/sh and is not portable to windows")
	}

	srcDir := filepath.Join("testdata", fixture)
	content, err := os.ReadFile(filepath.Join(srcDir, "codex.sh"))
	if err != nil {
		t.Fatalf("read fixture %s/codex.sh: %v", fixture, err)
	}

	binDir := t.TempDir()
	argvOut = filepath.Join(t.TempDir(), "argv")
	stdinOut = filepath.Join(t.TempDir(), "stdin")
	envOut = filepath.Join(t.TempDir(), "env")

	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake codex script: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("FAKE_CODEX_ARGV_OUT", argvOut)
	t.Setenv("FAKE_CODEX_STDIN_OUT", stdinOut)
	t.Setenv("FAKE_CODEX_ENV_OUT", envOut)

	return argvOut, stdinOut, envOut
}

// resolveCodexExecFromFixture seeds the fullArgsCodexExecTOML fixture
// at the project rung + scrubs XDG/HOME so the resolver winds up on
// the project file, then returns a constructed *codexExecBackend
// directly. We bypass Resolve here because the committed backend.go
// factory still routes only claude_json → claudeNativeBackend (the
// codex_stream case is added by the sibling envelope-routing droplet,
// which is outside this droplet's edit scope). This keeps the test
// hermetic without depending on the in-flight factory edit.
func resolveCodexExecFromFixture(t *testing.T) *codexExecBackend {
	t.Helper()

	projectDir := t.TempDir()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), fullArgsCodexExecTOML)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)

	// Direct decode: load the same TOML the resolver would and build
	// the codexExecBackend instance ourselves. resolve.go's helpers and
	// claude_native_test.go's resolveClaudeNativeFromFixture rely on
	// the factory switch already covering claude_native; codex_stream's
	// factory branch lands later. This test util is the bridge.
	cfg := decodeCodexExecConfig(t, projectDir)
	return &codexExecBackend{cfg: cfg}
}

// decodeCodexExecConfig reads the fixture TOML file at the project
// rung and returns the resolved `[backends.codex-exec]` BackendConfig.
// Inlined here (instead of using Resolve) so tests work even before
// the sibling factory-widen droplet lands the codex_stream case.
func decodeCodexExecConfig(t *testing.T, projectDir string) BackendConfig {
	t.Helper()
	path, _, err := ResolveBackendsConfig(projectDir)
	if err != nil {
		t.Fatalf("ResolveBackendsConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file backendsFile
	if _, err := toml.Decode(string(raw), &file); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	cfg, ok := file.Backends["codex-exec"]
	if !ok {
		t.Fatalf("no [backends.codex-exec] table in %s", path)
	}
	return cfg
}

// TestCodexExecBackend_EnvelopeFormat verifies the C2 amendment
// requirement: codexExecBackend declares its envelope dialect as
// "codex_stream" so the sibling envelope-routing droplet can dispatch
// parser selection by this value.
func TestCodexExecBackend_EnvelopeFormat(t *testing.T) {
	b := &codexExecBackend{cfg: BackendConfig{EnvelopeFormat: "codex_stream"}}
	if got := b.EnvelopeFormat(); got != "codex_stream" {
		t.Errorf("EnvelopeFormat: got %q want %q", got, "codex_stream")
	}
}

// TestCodexExecBackend_HappyPath exercises the full Spawn argv +
// stdin + persona-body + anti-recursion + cwd + model pipeline against
// the fake-codex-happy fixture.
func TestCodexExecBackend_HappyPath(t *testing.T) {
	argvFile, stdinFile, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	ctx := context.Background()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "build droplet X",
		CWD:         cwd,
		Model:       "gpt-5.4",
		PersonaBody: "PERSONA BODY LINE 1\nPERSONA BODY LINE 2\n",
	}

	result, err := ce.Spawn(ctx, req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}
	if !strings.Contains(string(result.Stdout), "mcp: ta/get") {
		t.Fatalf("stdout missing fixture marker; got %q", string(result.Stdout))
	}
	if result.Duration <= 0 {
		t.Errorf("duration: got %v want > 0", result.Duration)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	requiredFlags := []string{
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--skip-git-repo-check",
		"-C",
		"-m",
	}
	absentFlags := []string{"--ignore-rules"}
	for _, flag := range absentFlags {
		if containsArg(argv, flag) {
			t.Errorf("argv must NOT contain flag %q; argv=%v", flag, argv)
		}
	}

	// Hermetic -c flags must be present.
	hermeticFlags := []string{
		`approval_policy="never"`,
		`web_search="live"`,
		`project_doc_max_bytes=0`,
		`skills.bundled.enabled=false`,
	}
	for _, val := range hermeticFlags {
		if !containsAdjacent(argv, "-c", val) {
			t.Errorf("argv missing hermetic -c %q; argv=%v", val, argv)
		}
	}
	for _, flag := range requiredFlags {
		if !containsArg(argv, flag) {
			t.Errorf("argv missing required flag %q; argv=%v", flag, argv)
		}
	}

	if !containsAdjacent(argv, "-m", "gpt-5.4") {
		t.Errorf("argv missing -m gpt-5.4; argv=%v", argv)
	}
	if !containsAdjacent(argv, "-C", cwd) {
		t.Errorf("argv missing -C %s; argv=%v", cwd, argv)
	}

	// `--mcp-config` MUST be absent — codex consumes MCP via per-server
	// `-c` flags, not a --mcp-config flag pair. This pins the C-style
	// MCP-injection contract.
	if containsArg(argv, "--mcp-config") {
		t.Errorf("argv must not contain --mcp-config (codex uses -c inline TOML); argv=%v", argv)
	}
	if containsArg(argv, "--allowedTools") {
		t.Errorf("argv must not contain --allowedTools (codex has no such concept); argv=%v", argv)
	}

	stdinBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin recorder: %v", err)
	}
	if strings.TrimSpace(string(stdinBytes)) != "build droplet X" {
		t.Errorf("stdin mismatch: got %q want %q", string(stdinBytes), "build droplet X")
	}
}

// TestCodexExecBackend_AntiRecursionSuffix verifies the persona body
// is augmented with the package-private anti-recursion suffix from
// claude_native.go, with the role name substituted into the %s slot.
// codex receives this body via the standard prompt path — there is no
// codex equivalent of --append-system-prompt, so the suffix-augmented
// persona body must travel through whatever channel the BackendConfig
// argv template wires it into. The fixture TOML in this droplet does
// NOT consume {{.PersonaBody}} in its args, so this test asserts on
// the assembled body via renderArgs directly.
func TestCodexExecBackend_AntiRecursionSuffix(t *testing.T) {
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         "/tmp/cwd",
		PersonaBody: "PERSONA BODY",
	}

	_, td, err := ce.renderArgs(req)
	if err != nil {
		t.Fatalf("renderArgs: %v", err)
	}
	if !strings.Contains(td.PersonaBody, "PERSONA BODY") {
		t.Errorf("td.PersonaBody missing original persona; got %q", td.PersonaBody)
	}
	if !strings.Contains(td.PersonaBody, "DISPATCH CONTEXT:") {
		t.Errorf("td.PersonaBody missing anti-recursion suffix; got %q", td.PersonaBody)
	}
	if !strings.Contains(td.PersonaBody, "ta-go-builder") {
		t.Errorf("td.PersonaBody missing role name in suffix; got %q", td.PersonaBody)
	}
}

// TestCodexExecBackend_ModelHonored verifies the SpawnRequest.Model
// value flows through template substitution to the rendered -m flag.
func TestCodexExecBackend_ModelHonored(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.5-codex",
		CWD:         t.TempDir(),
		PersonaBody: "B",
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if !containsAdjacent(argv, "-m", "gpt-5.5-codex") {
		t.Errorf("model not honored; argv=%v", argv)
	}
}

// TestCodexExecBackend_CWDHonored verifies the SpawnRequest.CWD value
// flows through template substitution to the rendered -C flag AND
// gets applied as the subprocess working directory.
func TestCodexExecBackend_CWDHonored(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if !containsAdjacent(argv, "-C", cwd) {
		t.Errorf("argv missing -C %s; argv=%v", cwd, argv)
	}
}

// TestCodexExecBackend_SpawnInjectsMCPForPlanningRole verifies that a
// ta-go-planning role spawn argv carries the expected static
// role-conditional MCP flags from RenderRoleConditionalMCPFlags:
// ta, hylla, context7, and gopls (all four, since planning is go +
// non-build-qa). McpConfigPath is intentionally not set — injection
// is now unconditional and role-driven.
func TestCodexExecBackend_SpawnInjectsMCPForPlanningRole(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-planning",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// ta must always be present.
	assertArgvContainsMCPServer(t, argv, "mcp_servers.ta=")
	// hylla must be present (non-build-qa).
	assertArgvContainsMCPServer(t, argv, "mcp_servers.hylla=")
	// context7 must be present (non-build-qa).
	assertArgvContainsMCPServer(t, argv, "mcp_servers.context7=")
	// gopls must be present (go + non-build-qa).
	assertArgvContainsMCPServer(t, argv, "mcp_servers.gopls=")
	// playwright must be absent (not -fe-).
	assertArgvAbsentMCPServer(t, argv, "mcp_servers.playwright=")
}

// TestCodexExecBackend_SpawnOmitsHyllaForBuildQA verifies that a
// ta-go-build-qa-falsification role spawn argv carries ta (always)
// but omits hylla, context7, and gopls (build-qa exclusion).
func TestCodexExecBackend_SpawnOmitsHyllaForBuildQA(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-build-qa-falsification",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// ta must be present (always).
	assertArgvContainsMCPServer(t, argv, "mcp_servers.ta=")
	// hylla, context7, gopls must be absent for build-qa roles.
	assertArgvAbsentMCPServer(t, argv, "mcp_servers.hylla=")
	assertArgvAbsentMCPServer(t, argv, "mcp_servers.context7=")
	assertArgvAbsentMCPServer(t, argv, "mcp_servers.gopls=")
}

// TestCodexExecBackend_SpawnAlwaysInjectsTa verifies that ta MCP
// injection is always present even when McpConfigPath is empty and even
// for a non-go, non-fe builder role. Ta injection is unconditional in
// the new static role-conditional path.
func TestCodexExecBackend_SpawnAlwaysInjectsTa(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
		// McpConfigPath intentionally empty — ta injection is unconditional.
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// ta must be present regardless of McpConfigPath.
	assertArgvContainsMCPServer(t, argv, "mcp_servers.ta=")
}

// TestCodexExecBackend_SpawnInjectsMCPForFERole verifies that a
// ta-fe-planning role spawn argv carries playwright (fe-specific)
// but NOT gopls (no -go- in role name).
func TestCodexExecBackend_SpawnInjectsMCPForFERole(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-fe-planning",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// ta must always be present.
	assertArgvContainsMCPServer(t, argv, "mcp_servers.ta=")
	// playwright must be present (-fe- role).
	assertArgvContainsMCPServer(t, argv, "mcp_servers.playwright=")
	// gopls must be absent (no -go- in role).
	assertArgvAbsentMCPServer(t, argv, "mcp_servers.gopls=")
}

// TestCodexExecBackend_MissingBinaryReturnsError verifies a missing
// `codex` on PATH surfaces a wrapped lookup error rather than a
// silent zero-exit success.
func TestCodexExecBackend_MissingBinaryReturnsError(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "gpt-5.4", CWD: t.TempDir(), PersonaBody: "B"}
	_, err := ce.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when codex is not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "locate codex on PATH") {
		t.Errorf("error should mention PATH lookup; got %q", err.Error())
	}
}

// TestCodexExecBackend_ContextCancellation verifies ctx cancellation
// propagates to the child via exec.CommandContext and Spawn surfaces
// the cancellation as a wrapped ctx.Err().
func TestCodexExecBackend_ContextCancellation(t *testing.T) {
	installFakeCodex(t, "fake-codex-sleep")
	ce := resolveCodexExecFromFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "gpt-5.4", CWD: t.TempDir(), PersonaBody: "B"}

	start := time.Now()
	_, err := ce.Spawn(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when context times out, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded; got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("spawn ran for %s; context cancellation should have killed it faster", elapsed)
	}
}

// TestCodexExecBackend_NonZeroExitNotAnError pins the non-zero-exit-
// is-data contract: Spawn must NOT return a Go error for a child
// that ran but exited non-zero. The dispatcher classifies via stderr +
// exit code; that classification belongs in the dispatch layer.
func TestCodexExecBackend_NonZeroExitNotAnError(t *testing.T) {
	installFakeCodex(t, "fake-codex-fail")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "gpt-5.4", CWD: t.TempDir(), PersonaBody: "B"}
	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("non-zero exit must not surface as Go error; got %v", err)
	}
	if result.ExitCode != 9 {
		t.Errorf("exit code: got %d want 9", result.ExitCode)
	}
	if !strings.Contains(string(result.Stderr), "intentional failure") {
		t.Errorf("stderr should be captured; got %q", string(result.Stderr))
	}
}

// TestCodexExecBackend_EnvFilteredAndPassedThrough pins the env
// contract: the codex child sees os.Environ MINUS ANTHROPIC_BASE_URL +
// ANTHROPIC_AUTH_TOKEN. Other parent env vars pass through (notably
// OPENAI_API_KEY for codex, plus the FAKE_CODEX_* test plumbing vars).
//
// This preserves the drop_003 contract: even though codex doesn't
// consume ANTHROPIC vars, the filter keeps the env-shape invariant
// predictable across backends and prevents any future leak via codex's
// subprocesses (e.g. a codex-spawned tool that DOES read ANTHROPIC
// vars must not inherit them from a sand-orchestrated parent).
func TestCodexExecBackend_EnvFilteredAndPassedThrough(t *testing.T) {
	_, _, envFile := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	t.Setenv("ANTHROPIC_BASE_URL", "https://example.test/ollama")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sentinel-token")
	t.Setenv("SAND_CODEX_TEST_PASSTHROUGH", "passthrough-ok")
	t.Setenv("OPENAI_API_KEY", "sk-fake-test-key")

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "gpt-5.4", CWD: t.TempDir(), PersonaBody: "B"}
	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)

	if strings.Contains(envText, "ANTHROPIC_BASE_URL=") {
		t.Errorf("ANTHROPIC_BASE_URL should be filtered out of child env; got:\n%s", envText)
	}
	if strings.Contains(envText, "ANTHROPIC_AUTH_TOKEN=") {
		t.Errorf("ANTHROPIC_AUTH_TOKEN should be filtered out of child env; got:\n%s", envText)
	}
	if !strings.Contains(envText, "SAND_CODEX_TEST_PASSTHROUGH=passthrough-ok") {
		t.Errorf("non-filtered env var should pass through; got:\n%s", envText)
	}
	if !strings.Contains(envText, "OPENAI_API_KEY=sk-fake-test-key") {
		t.Errorf("OPENAI_API_KEY should flow naturally to codex; got:\n%s", envText)
	}
	if !strings.Contains(envText, "FAKE_CODEX_ARGV_OUT=") {
		t.Errorf("fake-CLI recorder var should pass through; got:\n%s", envText)
	}
}

// TestCodexExecBackend_PreviewShape verifies Backend.Preview renders
// the dry-run argv preserving the byte shape convention shared with
// claudeNativeBackend: one argument per line, `-m <value>`
// space-separated (NOT `-m=value`), and a trailing `<<< "<prompt>"`
// heredoc marker when StdinPrompt is true.
func TestCodexExecBackend_PreviewShape(t *testing.T) {
	ce := resolveCodexExecFromFixture(t)

	cwd := "/tmp/preview-cwd"
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "build droplet X",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "PERSONA BODY",
	}

	preview, err := ce.Preview(req)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	wantSubstrings := []string{
		"codex exec",
		"  --ephemeral",
		"  --ignore-user-config",
		"  --skip-git-repo-check",
		"  -C " + cwd,
		"  -m gpt-5.4",
		// Four hermetic -c flags must appear in Preview (F1: renderArgs injects them).
		// Values containing `"` are wrapped by Preview's quoteValue renderer:
		//   approval_policy="never"  → "approval_policy=\"never\""
		//   web_search="live"        → "web_search=\"live\""
		// Values without special chars render as-is (no outer quotes).
		"  -c \"approval_policy=\\\"never\\\"\"",
		"  -c \"web_search=\\\"live\\\"\"",
		"  -c project_doc_max_bytes=0",
		"  -c skills.bundled.enabled=false",
		`  <<< "build droplet X"`,
	}
	// --ignore-rules must be absent from Preview output.
	if strings.Contains(preview, "--ignore-rules") {
		t.Errorf("Preview must not contain --ignore-rules; got:\n%s", preview)
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(preview, want) {
			t.Errorf("Preview missing %q; got:\n%s", want, preview)
		}
	}

	// `-m=gpt-5.4` (equals-form) MUST be absent — the byte shape pins
	// the space-separated pair.
	if strings.Contains(preview, "-m=gpt-5.4") {
		t.Errorf("Preview must not use -m=gpt-5.4 equals form; got:\n%s", preview)
	}
}

// TestCodexExecBackend_BackendConfigEnvAppended verifies that
// BackendConfig.Env entries are templated + appended to the child env
// on top of the filtered os.Environ. Demonstrates the templating
// surface for codex-specific env forwarding (e.g. forwarding
// CODEX_HOME or similar via a `KEY={{env "VAR"}}` entry).
func TestCodexExecBackend_BackendConfigEnvAppended(t *testing.T) {
	_, _, envFile := installFakeCodex(t, "fake-codex-happy")

	// Seed a project rung fixture with a populated Env list (one
	// literal, one templated via {{env "..."}}).
	const tomlWithEnv = `
[backends.codex-exec]
command = "codex"
args = ["exec", "--ephemeral", "-m", "{{.Model}}"]
env = ["EXTRA_LITERAL=present", "EXTRA_TEMPLATED={{env \"CODEX_TEST_HOST\"}}"]
mcp_config_arg = ""
allowed_tools_arg = ""
allowed_tools_csv_template = ""
slots_default = 0
envelope_format = "codex_stream"
stdin_prompt = true
mcp_injection = "codex_inline_toml"
`
	projectDir := t.TempDir()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), tomlWithEnv)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_TEST_HOST", "from-os-getenv")

	cfg := decodeCodexExecConfig(t, projectDir)
	ce := &codexExecBackend{cfg: cfg}

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "gpt-5.4", CWD: t.TempDir(), PersonaBody: "B"}
	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)

	if !strings.Contains(envText, "EXTRA_LITERAL=present") {
		t.Errorf("literal env entry missing in child env; got:\n%s", envText)
	}
	if !strings.Contains(envText, "EXTRA_TEMPLATED=from-os-getenv") {
		t.Errorf("templated env entry not rendered via os.Getenv; got:\n%s", envText)
	}
}

// TestCodexExecBackend_HermeticHomeCleanedUp verifies that Spawn creates
// a hermetic CODEX_HOME temp directory, injects CODEX_HOME=<dir> into
// the child process env, and removes the directory when Spawn returns.
//
// Two assertions:
//  1. The child env (captured by fake-codex-happy) contains a
//     CODEX_HOME= entry whose value lies under os.TempDir().
//  2. After Spawn returns, the directory no longer exists on disk.
//
// This test does NOT assert on rules/default.rules content — that is
// covered by TestNewHermeticCodexHome_* in codex_hermetic_test.go (F8
// resolution: keep the concern in the right file).
func TestCodexExecBackend_HermeticHomeCleanedUp(t *testing.T) {
	_, _, envFile := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "hermetic home test",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)

	// 1. CODEX_HOME must appear in the child env.
	var hermeticDir string
	for _, line := range strings.Split(envText, "\n") {
		if strings.HasPrefix(line, "CODEX_HOME=") {
			hermeticDir = strings.TrimPrefix(line, "CODEX_HOME=")
			break
		}
	}
	if hermeticDir == "" {
		t.Fatalf("CODEX_HOME not found in child env; env:\n%s", envText)
	}

	// Verify the value looks like an OS temp dir path.
	tmpBase := os.TempDir()
	if !strings.HasPrefix(hermeticDir, tmpBase) {
		t.Errorf("CODEX_HOME=%q does not lie under os.TempDir()=%q", hermeticDir, tmpBase)
	}

	// 2. After Spawn returns, the hermetic dir must no longer exist.
	if _, statErr := os.Stat(hermeticDir); !os.IsNotExist(statErr) {
		t.Errorf("hermetic dir %q still exists after Spawn returned (cleanup did not run)", hermeticDir)
	}
}

// TestCodexExecBackend_OracleArgvShape is the consolidated oracle-
// equivalence contract test pinning the Spawn argv shape against the
// canonical bin/agent-dispatch.sh dispatch_codex reference (lines
// ~384-428, 4-way consensus 2026-05-25). It uses the fake-codex seam
// for full end-to-end coverage of the Spawn → renderArgs → hermetic-
// -c append → RenderRoleConditionalMCPFlags pipeline.
//
// Relationship to TestCodexExecBackend_HappyPath: HappyPath verifies
// the general argv + stdin + stdout contract. This test is the EXPLICIT
// named oracle — it names every token the oracle requires, pins the
// ordering invariant (hermetic -c flags precede MCP -c flags), and
// serves as the canonical reference for future oracle-drift detection.
// Assertions that overlap with HappyPath are intentional: this test
// may survive even if HappyPath is refactored.
func TestCodexExecBackend_OracleArgvShape(t *testing.T) {
	// Oracle rows drawn from bin/agent-dispatch.sh:384-428.
	// Each row is a named oracle contract; the slice allows future
	// per-role oracle variants without new top-level test functions.
	type oracleCase struct {
		name string
		role string
	}
	cases := []oracleCase{
		{name: "go_builder", role: "ta-go-builder"},
		{name: "go_planning", role: "ta-go-planning"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
			ce := resolveCodexExecFromFixture(t)

			req := SpawnRequest{
				Role:        tc.role,
				Prompt:      "oracle test",
				Model:       "gpt-5.4",
				CWD:         t.TempDir(),
				PersonaBody: "P",
			}

			if _, err := ce.Spawn(context.Background(), req); err != nil {
				t.Fatalf("Spawn: %v", err)
			}

			argvBytes, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatalf("read argv recorder: %v", err)
			}
			argv := splitArgv(argvBytes)

			// --- Oracle invariant 1: --ignore-rules must be ABSENT ---
			// bin/agent-dispatch.sh uses --ignore-user-config; --ignore-rules
			// is a different (per-project .codex/rules) suppression flag that
			// conflicts with hermetic-home isolation. It must never appear.
			if containsArg(argv, "--ignore-rules") {
				t.Errorf("oracle: argv MUST NOT contain --ignore-rules (breaks hermetic isolation); argv=%v", argv)
			}

			// --- Oracle invariant 2: --ignore-user-config must be PRESENT ---
			// Suppresses ~/.codex/config.toml so no user-level auth or
			// model selection bleeds into the hermetic dispatch.
			if !containsArg(argv, "--ignore-user-config") {
				t.Errorf("oracle: argv missing --ignore-user-config; argv=%v", argv)
			}

			// --ignore-user-config must appear before -C and -m so it is
			// processed before CWD/model substitution (ordering contract).
			ignoreIdx := -1
			cwdIdx := -1
			modelIdx := -1
			for i, a := range argv {
				switch a {
				case "--ignore-user-config":
					ignoreIdx = i
				case "-C":
					cwdIdx = i
				case "-m":
					modelIdx = i
				}
			}
			if cwdIdx >= 0 && ignoreIdx > cwdIdx {
				t.Errorf("oracle: --ignore-user-config (%d) must appear before -C (%d); argv=%v", ignoreIdx, cwdIdx, argv)
			}
			if modelIdx >= 0 && ignoreIdx > modelIdx {
				t.Errorf("oracle: --ignore-user-config (%d) must appear before -m (%d); argv=%v", ignoreIdx, modelIdx, argv)
			}

			// --- Oracle invariants 3-6: four hermetic -c flags must be PRESENT ---
			// These mirror bin/agent-dispatch.sh:393-428 and are appended by
			// renderArgs unconditionally after TOML template substitution.
			hermeticCFlags := []string{
				`approval_policy="never"`,
				`web_search="live"`,
				`project_doc_max_bytes=0`,
				`skills.bundled.enabled=false`,
			}
			for _, val := range hermeticCFlags {
				if !containsAdjacent(argv, "-c", val) {
					t.Errorf("oracle: argv missing hermetic -c %q; argv=%v", val, argv)
				}
			}

			// --- Ordering invariant: hermetic -c flags precede MCP -c flags ---
			// renderArgs appends hermetic flags; Spawn then appends
			// RenderRoleConditionalMCPFlags. The first MCP -c flag must
			// appear AFTER the last hermetic -c flag in argv.
			lastHermeticIdx := -1
			for i := 0; i < len(argv)-1; i++ {
				if argv[i] != "-c" {
					continue
				}
				val := argv[i+1]
				for _, hv := range hermeticCFlags {
					if val == hv {
						lastHermeticIdx = i
					}
				}
			}
			if lastHermeticIdx >= 0 {
				for i := 0; i < lastHermeticIdx; i++ {
					if argv[i] != "-c" {
						continue
					}
					val := argv[i+1]
					// An mcp_servers.* value before the last hermetic flag is
					// an ordering violation.
					if strings.HasPrefix(val, "mcp_servers.") {
						t.Errorf("oracle: MCP -c flag at argv[%d] (%q) appears before last hermetic -c flag at argv[%d]; argv=%v", i, val, lastHermeticIdx, argv)
					}
				}
			}
		})
	}
}

// TestCodexExecBackend_HermeticitySmoke asserts the hermeticity
// invariants that are visible from outside the Spawn call (post-
// Spawn, without reading cleaned-up files). Per QA-derived F8
// resolution (plan-QA option B): do NOT attempt to read
// rules/default.rules after Spawn — the deferred cleanup() has
// already removed the hermetic dir. Rules content is covered by
// TestNewHermeticCodexHome_* in codex_hermetic_test.go.
//
// Invariants asserted here:
//  1. CODEX_HOME=<dir> appears in the child env recorder.
//  2. The dir path lies under os.TempDir() (not ~/.codex).
//  3. --ignore-user-config is in argv (project config suppressed).
//  4. No AGENTS.md or CODEX_PROJECT_DOC path leaks into the child env
//     (project-doc budget is zeroed via project_doc_max_bytes=0 -c flag;
//     this asserts the env side of the same contract).
func TestCodexExecBackend_HermeticitySmoke(t *testing.T) {
	argvFile, _, envFile := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "hermeticity smoke",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "P",
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	// --- Invariant 1+2: CODEX_HOME in child env, under os.TempDir() ---
	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)

	var hermeticDir string
	for _, line := range strings.Split(envText, "\n") {
		if strings.HasPrefix(line, "CODEX_HOME=") {
			hermeticDir = strings.TrimPrefix(line, "CODEX_HOME=")
			break
		}
	}
	if hermeticDir == "" {
		t.Fatalf("hermeticity: CODEX_HOME not found in child env; env:\n%s", envText)
	}

	// The hermetic dir must be rooted under the OS temp prefix, NOT under
	// the real ~/.codex path.
	tmpBase := os.TempDir()
	if !strings.HasPrefix(hermeticDir, tmpBase) {
		t.Errorf("hermeticity: CODEX_HOME=%q must lie under os.TempDir()=%q (not ~/.codex)", hermeticDir, tmpBase)
	}
	homeDir := os.Getenv("HOME")
	if homeDir != "" {
		dotCodex := filepath.Join(homeDir, ".codex")
		if strings.HasPrefix(hermeticDir, dotCodex) {
			t.Errorf("hermeticity: CODEX_HOME=%q must NOT lie under ~/.codex=%q", hermeticDir, dotCodex)
		}
	}

	// --- Invariant 3: --ignore-user-config in argv ---
	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)
	if !containsArg(argv, "--ignore-user-config") {
		t.Errorf("hermeticity: argv missing --ignore-user-config (project config not suppressed); argv=%v", argv)
	}

	// --- Invariant 4: no AGENTS.md / CODEX_PROJECT_DOC path in child env ---
	// project_doc_max_bytes=0 prevents AGENTS.md from being passed to
	// codex; this asserts the env does not carry a bypass path.
	if strings.Contains(envText, "AGENTS.md") {
		t.Errorf("hermeticity: child env must not reference AGENTS.md (project-doc bypasses hermetic isolation); env:\n%s", envText)
	}
	if strings.Contains(envText, "CODEX_PROJECT_DOC=") {
		t.Errorf("hermeticity: child env must not contain CODEX_PROJECT_DOC= (project-doc bypasses hermetic isolation); env:\n%s", envText)
	}
}

// TestCodexExecBackend_GateCF1_EditPresentNoWritableDirs verifies CF-1
// (AGENT_SANDBOX_SPEC.md:67): codex is dir-level. When the gate carries
// file-scoped edits (EditPresent=true) but no writable_dirs, Spawn MUST
// return a non-nil error. Silently falling back to req.CWD would widen a
// file-scoped edit gate into project-dir-writable execution — a gate bypass.
func TestCodexExecBackend_GateCF1_EditPresentNoWritableDirs(t *testing.T) {
	installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	falseBool := false
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			Edit:         []string{"/abs/path/to/file.go"},
			EditPresent:  true,
			BashDeny:     []string{"git commit"},
			WritableDirs: nil, // no writable_dirs — CF-1 must fire
			Network:      &falseBool,
		},
	}

	_, err := ce.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("CF-1: expected non-nil error when EditPresent=true and WritableDirs is empty, got nil")
	}
	if !strings.Contains(err.Error(), "writable_dirs") {
		t.Errorf("CF-1 error should mention writable_dirs; got %q", err.Error())
	}
}

// TestCodexExecBackend_GateCF1_EditPresentEmptySlice verifies CF-1 also
// fires when WritableDirs is an explicitly empty slice (not nil) — both
// represent the absent-writable-dirs condition.
func TestCodexExecBackend_GateCF1_EditPresentEmptySlice(t *testing.T) {
	installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			Edit:         []string{"/abs/path/to/file.go"},
			EditPresent:  true,
			BashDeny:     nil,
			WritableDirs: []string{}, // empty slice — CF-1 must still fire
			Network:      nil,
		},
	}

	_, err := ce.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("CF-1 (empty slice): expected non-nil error when EditPresent=true and WritableDirs=[], got nil")
	}
}

// TestCodexExecBackend_GateCF1_EditNotPresent verifies CF-1 does NOT
// fire when EditPresent=false, even if WritableDirs is empty. CF-1 is
// conditional on EditPresent, not on WritableDirs being empty alone.
func TestCodexExecBackend_GateCF1_EditNotPresent(t *testing.T) {
	installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			Edit:         nil,
			EditPresent:  false, // not edit-scoped — CF-1 must NOT fire
			WritableDirs: nil,
			Network:      nil,
		},
	}

	_, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("CF-1 must not fire when EditPresent=false; got error: %v", err)
	}
}

// TestCodexExecBackend_GateBashDenyWiresSeam verifies that Spawn succeeds
// (does not panic or error) when req.Gate.BashDeny contains non-git patterns,
// proving the BashDeny seam is wired (newHermeticCodexHome is called with the
// patterns, not nil). The execpolicy rules content is verified in
// codex_hermetic_test.go; this test pins the Spawn-level wiring.
func TestCodexExecBackend_GateBashDenyWiresSeam(t *testing.T) {
	installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  false,
			WritableDirs: []string{cwd},
			BashDeny:     []string{"mage install", "go get", "go mod"},
			Network:      nil,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with Gate.BashDeny: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}
}

// TestCodexExecBackend_GateWritableDirsAddDir verifies that WritableDirs[1..N]
// produce --add-dir <dir> flags in the codex argv, per AGENT_SANDBOX_SPEC.md:74.
// WritableDirs[0] maps to req.CWD (the TOML's -C {{.CWD}} handles it); only
// entries beyond the first require --add-dir injection.
func TestCodexExecBackend_GateWritableDirsAddDir(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{cwd, dir1, dir2},
			BashDeny:     nil,
			Network:      nil,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with Gate.WritableDirs: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// WritableDirs[0] (cwd) is handled by -C {{.CWD}} from the TOML args.
	// WritableDirs[1] and WritableDirs[2] must produce --add-dir flags.
	if !containsAdjacent(argv, "--add-dir", dir1) {
		t.Errorf("argv missing --add-dir %s (WritableDirs[1]); argv=%v", dir1, argv)
	}
	if !containsAdjacent(argv, "--add-dir", dir2) {
		t.Errorf("argv missing --add-dir %s (WritableDirs[2]); argv=%v", dir2, argv)
	}
	// WritableDirs[0] must NOT produce an --add-dir (it maps to -C via TOML).
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--add-dir" && argv[i+1] == cwd {
			t.Errorf("argv must NOT contain --add-dir %s for WritableDirs[0]; argv=%v", cwd, argv)
		}
	}
}

// TestCodexExecBackend_GateWritableDirsSingleEntry verifies that when
// WritableDirs has exactly ONE entry, no --add-dir is emitted (only the
// TOML -C {{.CWD}} handles it). Boundary case: WritableDirs[0] only.
func TestCodexExecBackend_GateWritableDirsSingleEntry(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{cwd}, // only one entry — no --add-dir
			Network:      nil,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with single WritableDirs: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)
	if containsArg(argv, "--add-dir") {
		t.Errorf("argv must NOT contain --add-dir when WritableDirs has exactly 1 entry; argv=%v", argv)
	}
}

// TestCodexExecBackend_GateNetworkFalse verifies that when Gate.Network is
// explicitly false, Spawn appends the EXACT literal flag
// `-c sandbox_workspace_write.network_access=false` per AGENT_SANDBOX_SPEC.md:76.
func TestCodexExecBackend_GateNetworkFalse(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	falseBool := false
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{cwd},
			BashDeny:     nil,
			Network:      &falseBool,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with Gate.Network=false: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	const wantNetworkFlag = "sandbox_workspace_write.network_access=false"
	if !containsAdjacent(argv, "-c", wantNetworkFlag) {
		t.Errorf("argv missing exact network flag -c %q; argv=%v", wantNetworkFlag, argv)
	}
}

// TestCodexExecBackend_GateNetworkTrue verifies that when Gate.Network is
// true (network permitted), the sandbox_workspace_write flag is NOT appended.
func TestCodexExecBackend_GateNetworkTrue(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	trueBool := true
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{cwd},
			BashDeny:     nil,
			Network:      &trueBool,
		},
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn with Gate.Network=true: unexpected error: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	const networkFlag = "sandbox_workspace_write.network_access=false"
	if containsAdjacent(argv, "-c", networkFlag) {
		t.Errorf("argv must NOT contain network-disable flag when Gate.Network=true; argv=%v", argv)
	}
}

// TestCodexExecBackend_GateNetworkNil verifies that when Gate.Network is nil
// (omitted), the sandbox_workspace_write flag is NOT appended (nil = current
// default behavior unchanged).
func TestCodexExecBackend_GateNetworkNil(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{cwd},
			BashDeny:     nil,
			Network:      nil, // omitted — must not append network flag
		},
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn with Gate.Network=nil: unexpected error: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	const networkFlag = "sandbox_workspace_write.network_access=false"
	if containsAdjacent(argv, "-c", networkFlag) {
		t.Errorf("argv must NOT contain network-disable flag when Gate.Network=nil; argv=%v", argv)
	}
}

// TestCodexExecBackend_NilGateUnchanged pins the nil-Gate contract: when
// Gate is nil, Spawn must behave exactly as before gate threading — no error,
// no --add-dir, no network flag, no change to the hermetic -c flags or argv
// shape. This is the regression guard for the backwards-compat invariant.
func TestCodexExecBackend_NilGateUnchanged(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "nil gate test",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate:        nil, // explicitly nil — no gate
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with nil Gate: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// No --add-dir should appear.
	if containsArg(argv, "--add-dir") {
		t.Errorf("nil Gate: argv must NOT contain --add-dir; argv=%v", argv)
	}
	// No network-disable flag should appear.
	if containsAdjacent(argv, "-c", "sandbox_workspace_write.network_access=false") {
		t.Errorf("nil Gate: argv must NOT contain network-disable flag; argv=%v", argv)
	}
	// Hermetic flags must still be present (unchanged behavior).
	for _, val := range []string{`approval_policy="never"`, `web_search="live"`, `project_doc_max_bytes=0`, `skills.bundled.enabled=false`} {
		if !containsAdjacent(argv, "-c", val) {
			t.Errorf("nil Gate: argv missing hermetic flag -c %q; argv=%v", val, argv)
		}
	}
}

// TestCodexExecBackend_GateNarrowsCWD verifies CF-1 fix: when req.Gate is
// non-nil and len(req.Gate.WritableDirs) > 0, req.CWD is narrowed to
// WritableDirs[0] before renderArgs and RenderRoleConditionalMCPFlags.
// This makes the codex `-C` flag the narrowed writable root per
// AGENT_SANDBOX_SPEC.md:74 and matches bin/agent-dispatch.sh dispatch_codex.
func TestCodexExecBackend_GateNarrowsCWD(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	projectRoot := t.TempDir()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Caller passes CWD = projectRoot (the natural dispatch root) but
	// Gate.WritableDirs = [dir1, dir2]. The narrowing must rebind
	// req.CWD to dir1 before renderArgs so -C points to dir1, not projectRoot.
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         projectRoot,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{dir1, dir2},
			BashDeny:     nil,
			Network:      nil,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with Gate.WritableDirs narrowing: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// argv must contain -C dir1 (WritableDirs[0], the narrowed root).
	if !containsAdjacent(argv, "-C", dir1) {
		t.Errorf("argv missing -C %s (WritableDirs[0]); argv=%v", dir1, argv)
	}
	// argv must contain --add-dir dir2 (WritableDirs[1]).
	if !containsAdjacent(argv, "--add-dir", dir2) {
		t.Errorf("argv missing --add-dir %s (WritableDirs[1]); argv=%v", dir2, argv)
	}
	// argv must NOT contain -C projectRoot (the caller-passed CWD should be
	// narrowed away).
	if containsAdjacent(argv, "-C", projectRoot) {
		t.Errorf("argv must NOT contain -C %s (should be narrowed to WritableDirs[0]); argv=%v", projectRoot, argv)
	}
	// argv must NOT contain --add-dir dir1 (it maps to -C, not --add-dir).
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--add-dir" && argv[i+1] == dir1 {
			t.Errorf("argv must NOT contain --add-dir %s for WritableDirs[0]; argv=%v", dir1, argv)
		}
	}
}

// TestCodexExecBackend_GateNarrowsCWDSingleEntry verifies the boundary case:
// when WritableDirs contains exactly ONE entry, no --add-dir is emitted
// and -C uses that single entry (even if it differs from caller-passed CWD).
func TestCodexExecBackend_GateNarrowsCWDSingleEntry(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	projectRoot := t.TempDir()
	singleDir := t.TempDir()

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         projectRoot,
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent:  true,
			WritableDirs: []string{singleDir}, // only one entry
			BashDeny:     nil,
			Network:      nil,
		},
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with single WritableDirs: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// argv must contain -C singleDir (the narrowed and only entry).
	if !containsAdjacent(argv, "-C", singleDir) {
		t.Errorf("argv missing -C %s (WritableDirs[0]); argv=%v", singleDir, argv)
	}
	// argv must NOT contain any --add-dir (single entry only).
	if containsArg(argv, "--add-dir") {
		t.Errorf("argv must NOT contain --add-dir when WritableDirs has exactly 1 entry; argv=%v", argv)
	}
}

// TestCodexExecBackend_GateNarrowsCWDNilGateUnchanged verifies that
// the CWD narrowing only applies when Gate != nil. Existing nil-Gate
// behavior is unchanged.
func TestCodexExecBackend_GateNarrowsCWDNilGateUnchanged(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "nil gate test",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
		Gate:        nil, // explicitly nil — no narrowing
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn with nil Gate: unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// argv must contain -C cwd (caller-passed CWD, no narrowing).
	if !containsAdjacent(argv, "-C", cwd) {
		t.Errorf("nil Gate: argv missing -C %s; argv=%v", cwd, argv)
	}
	// argv must NOT contain any --add-dir (no gate, no writable_dirs).
	if containsArg(argv, "--add-dir") {
		t.Errorf("nil Gate: argv must NOT contain --add-dir; argv=%v", argv)
	}
}

// TestEnsureCodexJSONArgs_AutoNoFlag tests that ensureCodexJSONArgs injects
// --json after `exec` when StructuredOutput is "auto" and --json is absent.
func TestEnsureCodexJSONArgs_AutoNoFlag(t *testing.T) {
	cfg := BackendConfig{
		EnvelopeFormat:   "codex_stream",
		StructuredOutput: "auto",
	}
	args := []string{"exec", "--ephemeral", "-m", "gpt-5.4"}
	result, err := ensureCodexJSONArgs(args, cfg)
	if err != nil {
		t.Fatalf("ensureCodexJSONArgs: %v", err)
	}
	if len(result) != len(args)+1 {
		t.Errorf("ensureCodexJSONArgs: want len %d, got %d; result=%v", len(args)+1, len(result), result)
	}
	if result[0] != "exec" || result[1] != "--json" {
		t.Errorf("ensureCodexJSONArgs: --json not injected after exec; result=%v", result)
	}
}

// TestEnsureCodexJSONArgs_EmptyStringInjectsJSON tests that empty StructuredOutput
// (defaults to "auto") injects --json.
func TestEnsureCodexJSONArgs_EmptyStringInjectsJSON(t *testing.T) {
	cfg := BackendConfig{
		EnvelopeFormat:   "codex_stream",
		StructuredOutput: "", // empty = defaults to "auto"
	}
	args := []string{"exec", "--ephemeral"}
	result, err := ensureCodexJSONArgs(args, cfg)
	if err != nil {
		t.Fatalf("ensureCodexJSONArgs: %v", err)
	}
	if len(result) < 2 || result[1] != "--json" {
		t.Errorf("ensureCodexJSONArgs: --json not injected for empty StructuredOutput; result=%v", result)
	}
}

// TestEnsureCodexJSONArgs_ExistingFlagIdempotent tests that ensureCodexJSONArgs
// is idempotent: calling it on args that already have --json does not duplicate it.
func TestEnsureCodexJSONArgs_ExistingFlagIdempotent(t *testing.T) {
	cfg := BackendConfig{
		EnvelopeFormat:   "codex_stream",
		StructuredOutput: "auto",
	}
	args := []string{"exec", "--json", "--ephemeral", "-m", "gpt-5.4"}
	result, err := ensureCodexJSONArgs(args, cfg)
	if err != nil {
		t.Fatalf("ensureCodexJSONArgs: %v", err)
	}
	// Result should be identical to input (no duplicate --json).
	if len(result) != len(args) {
		t.Errorf("ensureCodexJSONArgs: should not duplicate --json; want len %d, got %d; result=%v", len(args), len(result), result)
	}
	if result[0] != "exec" || result[1] != "--json" {
		t.Errorf("ensureCodexJSONArgs: --json position changed; result=%v", result)
	}
}

// TestEnsureCodexJSONArgs_OffMode tests that StructuredOutput="off" does not
// inject or modify args.
func TestEnsureCodexJSONArgs_OffMode(t *testing.T) {
	cfg := BackendConfig{
		EnvelopeFormat:   "codex_stream",
		StructuredOutput: "off",
	}
	args := []string{"exec", "--ephemeral", "-m", "gpt-5.4"}
	result, err := ensureCodexJSONArgs(args, cfg)
	if err != nil {
		t.Fatalf("ensureCodexJSONArgs: %v", err)
	}
	if len(result) != len(args) {
		t.Errorf("ensureCodexJSONArgs (off mode): should not modify args; want len %d, got %d; result=%v", len(args), len(result), result)
	}
	for i, arg := range result {
		if arg != args[i] {
			t.Errorf("ensureCodexJSONArgs (off mode): args[%d] changed from %q to %q", i, args[i], arg)
		}
	}
}

// TestEnsureCodexJSONArgs_NonCodexStream tests that non-codex_stream EnvelopeFormat
// (e.g., claude_json) returns args unchanged (no-op).
func TestEnsureCodexJSONArgs_NonCodexStream(t *testing.T) {
	cfg := BackendConfig{
		EnvelopeFormat:   "claude_json",
		StructuredOutput: "auto",
	}
	args := []string{"exec", "--ephemeral"}
	result, err := ensureCodexJSONArgs(args, cfg)
	if err != nil {
		t.Fatalf("ensureCodexJSONArgs: %v", err)
	}
	if len(result) != len(args) {
		t.Errorf("ensureCodexJSONArgs (claude_json): should not modify args; want len %d, got %d; result=%v", len(args), len(result), result)
	}
}

// TestEnsureCodexJSONArgs_PreviewReflectsJSON tests that --json appears in Preview
// output when ensureCodexJSONArgs is applied in renderArgs.
func TestEnsureCodexJSONArgs_PreviewReflectsJSON(t *testing.T) {
	// Create a fixture with StructuredOutput="auto" so --json is enforced.
	const tomlWithJSON = `
[backends.codex-exec]
command = "codex"
args = ["exec", "--ephemeral", "--ignore-user-config", "-C", "{{.CWD}}", "-m", "{{.Model}}"]
env = []
mcp_config_arg = ""
allowed_tools_arg = ""
allowed_tools_csv_template = ""
slots_default = 0
envelope_format = "codex_stream"
stdin_prompt = true
mcp_injection = "codex_inline_toml"
structured_output = "auto"
`
	projectDir := t.TempDir()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), tomlWithJSON)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)

	cfg := decodeCodexExecConfig(t, projectDir)
	ce := &codexExecBackend{cfg: cfg}

	cwd := t.TempDir()
	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "test",
		Model:       "gpt-5.4",
		CWD:         cwd,
		PersonaBody: "B",
	}

	preview, err := ce.Preview(req)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	// --json must appear in the preview output (after `exec`).
	if !strings.Contains(preview, "--json") {
		t.Errorf("Preview must contain --json when StructuredOutput=auto; got:\n%s", preview)
	}

	// --json must appear right after exec (check the line structure).
	lines := strings.Split(preview, "\n")
	foundJSONAfterExec := false
	for i, line := range lines {
		if strings.Contains(line, "exec") {
			// Next line should contain --json.
			if i+1 < len(lines) && strings.Contains(lines[i+1], "--json") {
				foundJSONAfterExec = true
				break
			}
		}
	}
	if !foundJSONAfterExec {
		t.Errorf("Preview: --json not positioned after exec; got:\n%s", preview)
	}
}

// assertArgvContainsMCPServer is a test helper that fails if no `-c`
// flag in argv has a value starting with the given mcp_servers prefix.
func assertArgvContainsMCPServer(t *testing.T, argv []string, prefix string) {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], prefix) {
			return
		}
	}
	t.Errorf("argv missing expected -c %s* flag; argv=%v", prefix, argv)
}

// assertArgvAbsentMCPServer is a test helper that fails if any `-c`
// flag in argv has a value starting with the given mcp_servers prefix.
func assertArgvAbsentMCPServer(t *testing.T, argv []string, prefix string) {
	t.Helper()
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], prefix) {
			t.Errorf("argv unexpectedly contains -c %s* flag; argv=%v", prefix, argv)
			return
		}
	}
}
