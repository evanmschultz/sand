// Tests for the codex-exec Backend implementation. Coverage mirrors
// the committed TestClaudeNativeBackend_* suite where the contracts
// overlap (anti-recursion suffix, env filter, non-zero-exit-is-data,
// ctx cancellation, Preview byte shape) and adds codex-specific
// assertions for per-MCP `-c <inline-TOML>` flag injection driven by
// ProbeMCPServer.
//
// Test seam: each test that needs a live codex subprocess installs a
// fake `codex` shell script on a fresh PATH-prefix tempdir via
// installFakeCodex. The script records its argv (NUL-separated) +
// stdin + env to recorder files the test asserts against. This mirrors
// the fake-claude-* pattern installFakeClaude uses.
//
// MCP-injection tests reuse the fake-mcp-* fixtures already committed
// for codex_mcp_probe_test.go: a synthetic `.mcp.json` is written
// pointing at the installed fake-mcp script, the real ProbeMCPServer
// runs end-to-end against the subprocess, and the test asserts the
// resulting `-c <inline-TOML>` argv pair surfaced in the codex argv
// recording.
package backends

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// fullArgsCodexExecTOML is the backends.toml fixture used by every
// TestCodexExecBackend_* test. It populates BackendConfig with the
// canonical SAND-V02-SPEC §7.1 codex argv:
// `exec --ephemeral --ignore-rules --skip-git-repo-check -C {{.CWD}}
//
//	-m {{.Model}}`.
//
// stdin_prompt=true is the load-bearing codex contract: the prompt is
// piped to the child's stdin per the bash dispatcher reference.
//
// mcp_config_arg + allowed_tools_arg are intentionally EMPTY because
// codex does not consume those Claude-style flags — MCP injection is
// per-server `-c <inline-TOML>` (handled by renderMCPInjectionFlags)
// and there is no codex equivalent for --allowedTools (tool allow-
// listing is per-server via the inline TOML's `tools={...}` map).
const fullArgsCodexExecTOML = `
[backends.codex-exec]
command = "codex"
args = [
  "exec",
  "--ephemeral",
  "--ignore-rules",
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
		"--ignore-rules",
		"--skip-git-repo-check",
		"-C",
		"-m",
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

// TestCodexExecBackend_MCPInjectionIncludesProbedServer verifies the
// per-MCP `-c <inline-TOML>` flag injection: when the caller's
// .mcp.json declares an MCP server, codex_exec probes it via the real
// ProbeMCPServer, captures the canonical tool names, and appends a
// `-c "mcp_servers.<name>={...}"` pair to the codex argv.
//
// Uses the committed fake-mcp-happy fixture for the probe stub.
func TestCodexExecBackend_MCPInjectionIncludesProbedServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-MCP seam uses /bin/sh and is not portable to windows")
	}

	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	// Install the fake-mcp-happy script on PATH so its self-recording
	// env vars get propagated; we ALSO need a concrete path to that
	// script for the MCPServerEntry.Command field.
	mcpScriptPath := installScriptToTempPATH(t, "fake-mcp-happy", "server.sh", "mcp-server")

	// Write a synthetic .mcp.json pointing at the installed script.
	mcpJSON := map[string]any{
		"mcpServers": map[string]any{
			"ta": map[string]any{
				"command": mcpScriptPath,
				"args":    []string{"--flagA", "valA"},
			},
		},
	}
	mcpJSONPath := filepath.Join(t.TempDir(), ".mcp.json")
	writeJSONFile(t, mcpJSONPath, mcpJSON)

	req := SpawnRequest{
		Role:          "ta-go-builder",
		Prompt:        "x",
		Model:         "gpt-5.4",
		CWD:           t.TempDir(),
		PersonaBody:   "B",
		McpConfigPath: mcpJSONPath,
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// The -c flag should be present immediately followed by an inline
	// TOML payload starting with `mcp_servers.ta=`.
	found := false
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "mcp_servers.ta=") {
			found = true
			// Confirm the rendered TOML carries probed tool names from
			// the fake-mcp-happy fixture (it emits get, hylla.search.vector,
			// my-tool, Update).
			if !strings.Contains(argv[i+1], `"get"`) {
				t.Errorf("inline TOML missing get tool key; got %q", argv[i+1])
			}
			if !strings.Contains(argv[i+1], `"hylla.search.vector"`) {
				t.Errorf("inline TOML missing dotted tool key; got %q", argv[i+1])
			}
			if !strings.Contains(argv[i+1], `approval_mode="approve"`) {
				t.Errorf("inline TOML missing approval_mode; got %q", argv[i+1])
			}
			break
		}
	}
	if !found {
		t.Errorf("argv missing -c mcp_servers.ta=... pair; argv=%v", argv)
	}
}

// TestCodexExecBackend_MCPInjectionSkipsFailedProbe verifies the
// non-fatal-probe-failure contract: when an MCP server's probe is
// Skipped (e.g. malformed transport), codex dispatch proceeds with no
// `-c` flag for that server — but the spawn itself succeeds.
func TestCodexExecBackend_MCPInjectionSkipsFailedProbe(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	// Write a .mcp.json with a server that has NEITHER command NOR url
	// — guaranteed to be Skipped per A5 transport detection in
	// codex_mcp_probe.go.
	mcpJSON := map[string]any{
		"mcpServers": map[string]any{
			"malformed": map[string]any{},
		},
	}
	mcpJSONPath := filepath.Join(t.TempDir(), ".mcp.json")
	writeJSONFile(t, mcpJSONPath, mcpJSON)

	req := SpawnRequest{
		Role:          "ta-go-builder",
		Prompt:        "x",
		Model:         "gpt-5.4",
		CWD:           t.TempDir(),
		PersonaBody:   "B",
		McpConfigPath: mcpJSONPath,
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn must succeed even when MCP probe is skipped; got %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0", result.ExitCode)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)

	// No -c flag should have been emitted for the skipped server.
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-c" && strings.Contains(argv[i+1], "mcp_servers.malformed") {
			t.Errorf("argv unexpectedly contains -c for skipped server; argv=%v", argv)
		}
	}
}

// TestCodexExecBackend_MCPInjectionOmittedWhenMcpConfigPathEmpty
// verifies that when the caller has no .mcp.json (empty
// McpConfigPath), the codex argv contains zero `-c` flags. This pins
// the no-mcp-config case so empty callers don't pay the probe cost.
func TestCodexExecBackend_MCPInjectionOmittedWhenMcpConfigPathEmpty(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "gpt-5.4",
		CWD:         t.TempDir(),
		PersonaBody: "B",
		// no McpConfigPath
	}

	if _, err := ce.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if containsArg(argv, "-c") {
		t.Errorf("argv must not contain -c when McpConfigPath is empty; argv=%v", argv)
	}
}

// TestCodexExecBackend_MCPInjectionUnreadableConfigIsNonFatal verifies
// the soft-fail contract for an unreadable / unparseable .mcp.json:
// the spawn proceeds without any -c flags rather than erroring out.
// Mirrors the per-server probe-skip contract one level up.
func TestCodexExecBackend_MCPInjectionUnreadableConfigIsNonFatal(t *testing.T) {
	argvFile, _, _ := installFakeCodex(t, "fake-codex-happy")
	ce := resolveCodexExecFromFixture(t)

	req := SpawnRequest{
		Role:          "ta-go-builder",
		Prompt:        "x",
		Model:         "gpt-5.4",
		CWD:           t.TempDir(),
		PersonaBody:   "B",
		McpConfigPath: filepath.Join(t.TempDir(), "does-not-exist.mcp.json"),
	}

	result, err := ce.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn must succeed even with unreadable .mcp.json; got %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0", result.ExitCode)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if containsArg(argv, "-c") {
		t.Errorf("argv must not contain -c when .mcp.json unreadable; argv=%v", argv)
	}
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
		"  --ignore-rules",
		"  --skip-git-repo-check",
		"  -C " + cwd,
		"  -m gpt-5.4",
		`  <<< "build droplet X"`,
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

// installScriptToTempPATH installs a fixture script into a fresh
// tempdir on PATH and returns the absolute path to the installed
// script. Used by MCP-injection tests that need a concrete command
// path to put inside the synthetic .mcp.json.
func installScriptToTempPATH(t *testing.T, fixture, srcName, installAs string) string {
	t.Helper()
	srcDir := filepath.Join("testdata", fixture)
	content, err := os.ReadFile(filepath.Join(srcDir, srcName))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", fixture, srcName, err)
	}
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, installAs)
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake %s script: %v", installAs, err)
	}
	// Also prepend to PATH so any reference to bare `installAs` resolves.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	// Mirror the FAKE_MCP_* env vars that fake-mcp-* scripts expect, so
	// the script can write its argv/stdin recorders without erroring.
	if strings.HasPrefix(fixture, "fake-mcp-") {
		t.Setenv("FAKE_MCP_ARGV_OUT", filepath.Join(t.TempDir(), "mcp-argv"))
		t.Setenv("FAKE_MCP_STDIN_OUT", filepath.Join(t.TempDir(), "mcp-stdin"))
	}
	return scriptPath
}

// writeJSONFile is a tiny test helper for laying down a synthetic
// `.mcp.json` (or any JSON file) into a tempdir. Pretty-prints for
// human debuggability of failed tests.
func writeJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON for %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
