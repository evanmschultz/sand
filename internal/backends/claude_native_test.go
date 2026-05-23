// Tests for the claude-native Backend implementation. Coverage mirrors
// the committed internal/dispatch/TestRunClaudeNative_* suite per drop_011
// orchestrator amendment A5 — every behavior point the legacy spawn pins
// has an equivalent assertion here so the new Backend.Spawn /
// Backend.Preview pair preserves runClaudeNative bit-for-bit.
//
// Test seam: each test installs a fake `claude` shell script on a fresh
// PATH-prefix tempdir via installFakeClaude. The script records its argv
// (NUL-separated) + stdin + env to recorder files the test asserts
// against. This is the same pattern the committed TestRunClaudeNative_*
// suite uses; the testdata fixtures are deliberately copied into
// internal/backends/testdata/ so the backends package's tests do not
// reach across package boundaries for fixture data.
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
)

// fullArgsClaudeNativeTOML is the backends.toml fixture used by every
// TestClaudeNativeBackend_* test. It populates BackendConfig with the
// SAME set of flags the committed runClaudeNative hardcodes — `-p`,
// `--bare`, `--model {{model}}`, `--output-format json`,
// `--no-session-persistence`, `--append-system-prompt {{persona body}}` —
// plus McpConfigArg, AllowedToolsArg, AllowedToolsCSVTemplate so the
// conditional appends in Spawn fire correctly.
//
// stdin_prompt=true is the load-bearing claude-native contract: the
// prompt is piped to the child's stdin, NOT a `{{prompt}}` template
// substitution in args.
const fullArgsClaudeNativeTOML = `
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

// installFakeClaude prepends a fresh temp dir to PATH containing a copy
// of the named fixture shell script, exposed as `claude`. The script's
// stdout becomes Spawn's captured stdout; argv + stdin + env get
// recorded to per-test files for assertion. Returns the recorder paths.
//
// The PATH override is undone via t.Cleanup so tests stay hermetic.
func installFakeClaude(t *testing.T, fixture string) (argvOut, stdinOut, envOut string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI seam uses /bin/sh and is not portable to windows")
	}

	srcDir := filepath.Join("testdata", fixture)
	content, err := os.ReadFile(filepath.Join(srcDir, "claude.sh"))
	if err != nil {
		t.Fatalf("read fixture %s/claude.sh: %v", fixture, err)
	}

	binDir := t.TempDir()
	argvOut = filepath.Join(t.TempDir(), "argv")
	stdinOut = filepath.Join(t.TempDir(), "stdin")
	envOut = filepath.Join(t.TempDir(), "env")

	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("FAKE_CLAUDE_ARGV_OUT", argvOut)
	t.Setenv("FAKE_CLAUDE_STDIN_OUT", stdinOut)
	t.Setenv("FAKE_CLAUDE_ENV_OUT", envOut)

	return argvOut, stdinOut, envOut
}

// resolveClaudeNativeFromFixture seeds the fullArgsClaudeNativeTOML
// fixture at the project rung + scrubs XDG/HOME so the resolver winds
// up on the project file, then resolves `claude-native` via Resolve.
// Returns the concrete *claudeNativeBackend for assertion.
func resolveClaudeNativeFromFixture(t *testing.T) *claudeNativeBackend {
	t.Helper()

	projectDir := t.TempDir()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), fullArgsClaudeNativeTOML)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)

	b, err := Resolve(projectDir, "claude-native")
	if err != nil {
		t.Fatalf("Resolve(claude-native): %v", err)
	}
	cn, ok := b.(*claudeNativeBackend)
	if !ok {
		t.Fatalf("Resolve(claude-native): expected *claudeNativeBackend, got %T", b)
	}
	return cn
}

// splitArgv parses a NUL-separated argv recording (one arg per record,
// no trailing record). The fake-claude shell scripts write argv this
// way so arguments with embedded newlines (notably
// --append-system-prompt) round-trip without being shredded.
func splitArgv(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// containsArg reports whether argv contains the literal token target.
func containsArg(argv []string, target string) bool {
	for _, a := range argv {
		if a == target {
			return true
		}
	}
	return false
}

// containsAdjacent reports whether argv contains flag immediately
// followed by value (the standard flag-pair shape).
func containsAdjacent(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// findArgAfter returns the argv entry immediately following the first
// occurrence of flag, or "" when flag is absent or has no successor.
func findArgAfter(argv []string, flag string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// TestClaudeNativeBackend_HappyPath exercises the full Spawn argv +
// stdin + persona-body + anti-recursion + mcp-config + allowedTools
// pipeline against the fake-claude-happy fixture.
func TestClaudeNativeBackend_HappyPath(t *testing.T) {
	argvFile, stdinFile, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)

	ctx := context.Background()
	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "build droplet X",
		CWD:             t.TempDir(),
		Model:           "haiku",
		PersonaBody:     "PERSONA BODY LINE 1\nPERSONA BODY LINE 2\n",
		PersonaToolsCSV: "Read,Edit,Bash(mage testFunc *)",
		McpConfigPath:   "/abs/cwd/.mcp.json",
	}

	result, err := cn.Spawn(ctx, req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}
	if !strings.Contains(string(result.Stdout), `"result"`) {
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

	// argv[0] is the script itself; argv[1:] is what Spawn constructed.
	requiredFlags := []string{
		"-p",
		"--bare",
		"--model",
		"--output-format",
		"--no-session-persistence",
		"--append-system-prompt",
		"--mcp-config",
		"--allowedTools",
	}
	for _, flag := range requiredFlags {
		if !containsArg(argv, flag) {
			t.Errorf("argv missing required flag %q; argv=%v", flag, argv)
		}
	}

	if !containsAdjacent(argv, "--model", "haiku") {
		t.Errorf("argv missing --model haiku; argv=%v", argv)
	}
	if !containsAdjacent(argv, "--output-format", "json") {
		t.Errorf("argv missing --output-format json; argv=%v", argv)
	}
	if !containsAdjacent(argv, "--mcp-config", "/abs/cwd/.mcp.json") {
		t.Errorf("argv missing --mcp-config /abs/cwd/.mcp.json; argv=%v", argv)
	}
	if !containsAdjacent(argv, "--allowedTools", "Read,Edit,Bash(mage testFunc *)") {
		t.Errorf("argv allowedTools mismatch; argv=%v", argv)
	}

	systemPrompt := findArgAfter(argv, "--append-system-prompt")
	if !strings.Contains(systemPrompt, "PERSONA BODY LINE 1") {
		t.Errorf("system prompt missing persona body; got %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "DISPATCH CONTEXT:") {
		t.Errorf("system prompt missing anti-recursion suffix; got %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "ta-go-builder") {
		t.Errorf("system prompt missing role name in anti-recursion suffix; got %q", systemPrompt)
	}

	stdinBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin recorder: %v", err)
	}
	if strings.TrimSpace(string(stdinBytes)) != "build droplet X" {
		t.Errorf("stdin mismatch: got %q want %q", string(stdinBytes), "build droplet X")
	}
}

// TestClaudeNativeBackend_ModelHonored verifies the SpawnRequest.Model
// value flows through template substitution to the rendered --model
// flag. Equivalent to the committed ModelOverrideWins test (the
// dispatcher resolves the override BEFORE handing the request to the
// backend now — backends just see Model on the request).
func TestClaudeNativeBackend_ModelHonored(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "qwen3-coder:30b",
		PersonaBody: "B",
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if !containsAdjacent(argv, "--model", "qwen3-coder:30b") {
		t.Errorf("model not honored; argv=%v", argv)
	}
}

// TestClaudeNativeBackend_OmitsMCPConfigWhenEmpty proves --mcp-config is
// elided when SpawnRequest.McpConfigPath is empty (the caller project
// has no .mcp.json).
func TestClaudeNativeBackend_OmitsMCPConfigWhenEmpty(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "x",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read",
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if containsArg(argv, "--mcp-config") {
		t.Errorf("--mcp-config should be omitted when path empty; argv=%v", argv)
	}
}

// TestClaudeNativeBackend_OmitsAllowedToolsWhenEmpty proves
// --allowedTools is elided when SpawnRequest.PersonaToolsCSV is empty.
func TestClaudeNativeBackend_OmitsAllowedToolsWhenEmpty(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if containsArg(argv, "--allowedTools") {
		t.Errorf("--allowedTools should be omitted when CSV empty; argv=%v", argv)
	}
}

// TestClaudeNativeBackend_MissingBinaryReturnsError verifies a missing
// `claude` on PATH surfaces a wrapped lookup error rather than a
// silent zero-exit success.
func TestClaudeNativeBackend_MissingBinaryReturnsError(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}
	_, err := cn.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when claude is not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "locate claude on PATH") {
		t.Errorf("error should mention PATH lookup; got %q", err.Error())
	}
}

// TestClaudeNativeBackend_ContextCancellation verifies ctx
// cancellation propagates to the child via exec.CommandContext and
// Spawn surfaces the cancellation as a wrapped ctx.Err().
func TestClaudeNativeBackend_ContextCancellation(t *testing.T) {
	installFakeClaude(t, "fake-claude-sleep")
	cn := resolveClaudeNativeFromFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}

	start := time.Now()
	_, err := cn.Spawn(ctx, req)
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

// TestClaudeNativeBackend_NonZeroExitNotAnError pins the non-zero-exit-
// is-data contract: Spawn must NOT return a Go error for a child that
// ran but exited non-zero. The dispatcher classifies via stderr + exit
// code; that classification belongs in the dispatch layer.
func TestClaudeNativeBackend_NonZeroExitNotAnError(t *testing.T) {
	installFakeClaude(t, "fake-claude-fail")
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}
	result, err := cn.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("non-zero exit must not surface as Go error; got %v", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("exit code: got %d want 7", result.ExitCode)
	}
	if !strings.Contains(string(result.Stderr), "intentional failure") {
		t.Errorf("stderr should be captured; got %q", string(result.Stderr))
	}
}

// TestClaudeNativeBackend_EnvFilteredAndPassedThrough pins the env
// contract: the child sees os.Environ MINUS ANTHROPIC_BASE_URL +
// ANTHROPIC_AUTH_TOKEN. Other parent env vars pass through (the
// FAKE_CLAUDE_* test plumbing vars are the demonstrable case).
func TestClaudeNativeBackend_EnvFilteredAndPassedThrough(t *testing.T) {
	_, _, envFile := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)

	t.Setenv("ANTHROPIC_BASE_URL", "https://example.test/ollama")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sentinel-token")
	t.Setenv("SAND_BACKENDS_TEST_PASSTHROUGH", "passthrough-ok")

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}
	if _, err := cn.Spawn(context.Background(), req); err != nil {
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
	if !strings.Contains(envText, "SAND_BACKENDS_TEST_PASSTHROUGH=passthrough-ok") {
		t.Errorf("non-filtered env var should pass through; got:\n%s", envText)
	}
	if !strings.Contains(envText, "FAKE_CLAUDE_ARGV_OUT=") {
		t.Errorf("fake-CLI recorder var should pass through; got:\n%s", envText)
	}
}

// TestClaudeNativeBackend_PreviewShape verifies Backend.Preview renders
// the dry-run argv preserving the committed renderDryRunCommand byte
// shape: `--model <value>` space-separated (NOT `--model=value`),
// persona body via strconv-Quote-style escaping so embedded newlines
// stay on one line, conditional `--mcp-config` + `--allowedTools`,
// trailing `<<< "<prompt>"` heredoc marker.
func TestClaudeNativeBackend_PreviewShape(t *testing.T) {
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "build droplet X",
		Model:           "haiku",
		PersonaBody:     "PERSONA BODY LINE 1\nPERSONA BODY LINE 2",
		PersonaToolsCSV: "Read,Edit",
		McpConfigPath:   "/abs/cwd/.mcp.json",
	}

	preview, err := cn.Preview(req)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	wantSubstrings := []string{
		"claude -p",
		"  --bare",
		"  --model haiku",
		"  --output-format json",
		"  --no-session-persistence",
		"  --append-system-prompt ",
		`PERSONA BODY LINE 1\n`, // newline-escaped via quoteValue
		"  --mcp-config /abs/cwd/.mcp.json",
		"  --allowedTools Read,Edit",
		`  <<< "build droplet X"`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(preview, want) {
			t.Errorf("Preview missing %q; got:\n%s", want, preview)
		}
	}

	// `--model=haiku` (equals-form) MUST be absent — the byte shape
	// pins the space-separated pair.
	if strings.Contains(preview, "--model=haiku") {
		t.Errorf("Preview must not use --model=haiku equals form; got:\n%s", preview)
	}
}

// TestClaudeNativeBackend_PreviewOmitsConditionalFlags verifies the
// conditional --mcp-config + --allowedTools elision contract holds in
// Preview the same way it does in Spawn.
func TestClaudeNativeBackend_PreviewOmitsConditionalFlags(t *testing.T) {
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
		// no McpConfigPath, no PersonaToolsCSV
	}

	preview, err := cn.Preview(req)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if strings.Contains(preview, "--mcp-config") {
		t.Errorf("Preview should omit --mcp-config when path empty; got:\n%s", preview)
	}
	if strings.Contains(preview, "--allowedTools") {
		t.Errorf("Preview should omit --allowedTools when CSV empty; got:\n%s", preview)
	}
}

// TestClaudeNativeBackend_BackendConfigEnvAppended verifies that
// BackendConfig.Env entries are templated + appended to the child env
// on top of the filtered os.Environ. Demonstrates the templating
// surface fully even though the canonical claude-native config ships
// with empty Env.
func TestClaudeNativeBackend_BackendConfigEnvAppended(t *testing.T) {
	_, _, envFile := installFakeClaude(t, "fake-claude-happy")

	// Seed a project rung fixture with a populated Env list (one literal,
	// one templated via {{env "..."}}).
	const tomlWithEnv = `
[backends.claude-native]
command = "claude"
args = ["-p", "--model", "{{.Model}}"]
env = ["EXTRA_LITERAL=present", "EXTRA_TEMPLATED={{env \"HOST_PASSTHROUGH\"}}"]
mcp_config_arg = ""
allowed_tools_arg = ""
allowed_tools_csv_template = ""
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true
mcp_injection = ""
`
	projectDir := t.TempDir()
	homeDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), tomlWithEnv)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)
	t.Setenv("HOST_PASSTHROUGH", "from-os-getenv")

	b, err := Resolve(projectDir, "claude-native")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cn := b.(*claudeNativeBackend)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}
	if _, err := cn.Spawn(context.Background(), req); err != nil {
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
