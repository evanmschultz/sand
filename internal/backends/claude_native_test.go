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

	"github.com/evanmschultz/sand/internal/gate"
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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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
		"--verbose",
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
	if !containsAdjacent(argv, "--output-format", "stream-json") {
		t.Errorf("argv missing --output-format stream-json; argv=%v", argv)
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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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

	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")
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
	if !strings.Contains(envText, "ANTHROPIC_API_KEY=sentinel-token") {
		t.Errorf("ANTHROPIC_API_KEY should pass through to child env; got:\n%s", envText)
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
		"  --output-format stream-json",
		"  --verbose",
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

	// `--output-format json` (old form) MUST be absent — it's upgraded to stream-json.
	if strings.Contains(preview, "--output-format json") {
		t.Errorf("Preview must not have --output-format json (should be stream-json); got:\n%s", preview)
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
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

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

// TestClaudeNativeSpawn_GateEditScoped verifies that when req.Gate carries
// an edit-scoped Allowlist (EditPresent=true, non-empty Edit), renderArgs
// emits --allowedTools with persona base tools prepended and scoped
// Edit(//abs), Write(//abs), and MultiEdit(//abs) entries appended for each
// file. The persona CSV is rendered FIRST, then the scoped entries appended.
//
// Acceptance criteria from drop_014.drop.a5_claude_p_cf2_fix:
//   - Persona base tools (Read, Glob, Grep, mcp__ta__update, etc.) prepended.
//   - Edit(//abs), Write(//abs), MultiEdit(//abs) per file in Gate.Edit appended.
//   - CSV ordering: persona tokens first, scoped triples after, separated by comma.
//   - No bare "Bash" token (without parens) in the --allowedTools value.
func TestClaudeNativeSpawn_GateEditScoped(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	editFiles := []string{
		"/abs/project/internal/foo/foo.go",
		"/abs/project/internal/foo/foo_test.go",
	}

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "build thing",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read,Glob,Grep,Bash(mage testFunc *),mcp__ta__update",
		Gate: &gate.Allowlist{
			EditPresent: true,
			Edit:        editFiles,
			BashDeny: []string{
				"git commit", "git push",
			},
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// --allowedTools must be present.
	allowedToolsVal := findArgAfter(argv, "--allowedTools")
	if allowedToolsVal == "" {
		t.Fatalf("--allowedTools must be present when Gate.EditPresent; argv=%v", argv)
	}

	// Persona CSV tokens must appear BEFORE scoped Edit/Write/MultiEdit entries.
	// The ordering is deterministic: persona base tools first (comma-separated),
	// then scoped triples appended. Verify by checking each persona token exists
	// and each scoped entry exists, then assert comma ordering.
	personaTokens := []string{"Read", "Glob", "Grep", "Bash(mage testFunc *)", "mcp__ta__update"}
	for _, token := range personaTokens {
		if !strings.Contains(allowedToolsVal, token) {
			t.Errorf("--allowedTools missing persona token %q; got %q", token, allowedToolsVal)
		}
	}

	// Each file must appear as Edit(//abs), Write(//abs), MultiEdit(//abs).
	// The oracle (bin/agent-dispatch.sh dispatch_ollama) uses ${ef#/} to strip
	// the leading slash from an absolute path, then prepends "//" — so
	// "/abs/foo.go" becomes "Edit(//abs/foo.go)".
	for _, f := range editFiles {
		stripped := strings.TrimPrefix(f, "/")
		wantEdit := "Edit(//" + stripped + ")"
		wantWrite := "Write(//" + stripped + ")"
		wantMultiEdit := "MultiEdit(//" + stripped + ")"
		if !strings.Contains(allowedToolsVal, wantEdit) {
			t.Errorf("--allowedTools missing %q; got %q", wantEdit, allowedToolsVal)
		}
		if !strings.Contains(allowedToolsVal, wantWrite) {
			t.Errorf("--allowedTools missing %q; got %q", wantWrite, allowedToolsVal)
		}
		if !strings.Contains(allowedToolsVal, wantMultiEdit) {
			t.Errorf("--allowedTools missing %q; got %q", wantMultiEdit, allowedToolsVal)
		}
		// Double-slash form required: a single-slash form would deny all edits.
		// Verify the canonical //abs form (not single-slash /(abs)).
		singleSlashEdit := "Edit(/" + stripped + ")"
		if strings.Contains(allowedToolsVal, singleSlashEdit) && !strings.Contains(allowedToolsVal, wantEdit) {
			t.Errorf("--allowedTools has single-slash form %q instead of double-slash; got %q", singleSlashEdit, allowedToolsVal)
		}
	}

	// No bare "Bash" token in --allowedTools value. Persona-declared patterns
	// like "Bash(mage testFunc *)" are allowed; we reject any standalone "Bash"
	// that is NOT followed by an opening paren (which would indicate the gate
	// translation itself added bare Bash, violating the contract).
	parts := strings.Split(allowedToolsVal, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "Bash" {
			t.Errorf("--allowedTools must not contain standalone Bash token; got %q", allowedToolsVal)
		}
	}

	// --disallowedTools must be present for each BashDeny pattern.
	if !containsArg(argv, "--disallowedTools") {
		t.Fatalf("--disallowedTools must be present when Gate.BashDeny non-empty; argv=%v", argv)
	}
	// Both deny patterns should appear somewhere in argv as Bash(<pat>:*).
	allArgv := strings.Join(argv, " ")
	for _, deny := range req.Gate.BashDeny {
		want := "Bash(" + deny + ":*)"
		if !strings.Contains(allArgv, want) {
			t.Errorf("argv missing disallowed tool entry %q; argv=%v", want, argv)
		}
	}
}

// TestClaudeNativeSpawn_GateEditPresentEmptyList exercises EditPresent=true
// with an empty Edit slice: --allowedTools should still appear (no file-scoped
// entries, but the flag is emitted) and no bare Bash should be present.
func TestClaudeNativeSpawn_GateEditPresentEmptyList(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent: true,
			Edit:        []string{}, // empty — read-only edit-scoped role
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// With EditPresent=true and empty Edit list the gate path still fires.
	// No file scopes are added but --allowedTools must NOT have bare Bash.
	allowedToolsVal := findArgAfter(argv, "--allowedTools")
	if allowedToolsVal != "" && strings.Contains(allowedToolsVal, "Bash") {
		t.Errorf("bare Bash must never appear in --allowedTools under a Gate; got %q", allowedToolsVal)
	}
	// No --disallowedTools when BashDeny is empty.
	if containsArg(argv, "--disallowedTools") {
		t.Errorf("--disallowedTools should be absent when Gate.BashDeny is empty; argv=%v", argv)
	}
}

// TestClaudeNativeSpawn_GateBashDenyOnly verifies that BashDeny entries are
// translated to --disallowedTools flags even when EditPresent is false.
func TestClaudeNativeSpawn_GateBashDenyOnly(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
		Gate: &gate.Allowlist{
			EditPresent: false,
			Edit:        nil,
			BashDeny:    []string{"git commit", "mage install"},
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	if !containsArg(argv, "--disallowedTools") {
		t.Fatalf("--disallowedTools must be present when Gate.BashDeny non-empty; argv=%v", argv)
	}
	allArgv := strings.Join(argv, " ")
	for _, deny := range req.Gate.BashDeny {
		want := "Bash(" + deny + ":*)"
		if !strings.Contains(allArgv, want) {
			t.Errorf("argv missing disallowed tool entry %q; argv=%v", want, argv)
		}
	}
}

// TestClaudeNativeSpawn_NilGateUnchanged verifies that nil Gate leaves the
// existing PersonaToolsCSV/AllowedToolsCSVTemplate path fully intact.
// This is the backwards-compatibility regression guard.
func TestClaudeNativeSpawn_NilGateUnchanged(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "x",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read,Edit,Bash(mage testFunc *)",
		McpConfigPath:   "/abs/.mcp.json",
		Gate:            nil, // explicit nil — existing path must be unchanged
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// Existing path: --allowedTools with the raw CSV value.
	if !containsAdjacent(argv, "--allowedTools", "Read,Edit,Bash(mage testFunc *)") {
		t.Errorf("nil Gate must preserve PersonaToolsCSV path; argv=%v", argv)
	}
	// No --disallowedTools injected.
	if containsArg(argv, "--disallowedTools") {
		t.Errorf("nil Gate must not inject --disallowedTools; argv=%v", argv)
	}
}

// TestClaudeNativePreview_GateEditScoped verifies that Preview renders the
// gate-translated --allowedTools (with persona base tools prepended + scoped
// entries appended) and --disallowedTools flags in the dry-run output
// alongside the standard argv shape.
func TestClaudeNativePreview_GateEditScoped(t *testing.T) {
	cn := resolveClaudeNativeFromFixture(t)

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "build thing",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read,Glob,Bash(mage check)",
		Gate: &gate.Allowlist{
			EditPresent: true,
			Edit:        []string{"/abs/foo.go", "/abs/foo_test.go"},
			BashDeny:    []string{"git commit"},
		},
	}

	preview, err := cn.Preview(req)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	// Persona base tools should appear.
	if !strings.Contains(preview, "Read") {
		t.Errorf("Preview missing persona base tool Read; got:\n%s", preview)
	}
	if !strings.Contains(preview, "Glob") {
		t.Errorf("Preview missing persona base tool Glob; got:\n%s", preview)
	}
	if !strings.Contains(preview, "Bash(mage check)") {
		t.Errorf("Preview missing persona tool Bash(mage check); got:\n%s", preview)
	}

	// --allowedTools must appear with double-slash file scopes.
	// "/abs/foo.go" → strip leading "/" → "abs/foo.go" → prefix "//" → "Edit(//abs/foo.go)".
	if !strings.Contains(preview, "Edit(//abs/foo.go)") {
		t.Errorf("Preview missing Edit(//abs/foo.go); got:\n%s", preview)
	}
	if !strings.Contains(preview, "Write(//abs/foo.go)") {
		t.Errorf("Preview missing Write(//abs/foo.go); got:\n%s", preview)
	}
	if !strings.Contains(preview, "MultiEdit(//abs/foo.go)") {
		t.Errorf("Preview missing MultiEdit(//abs/foo.go); got:\n%s", preview)
	}
	// --disallowedTools must appear.
	if !strings.Contains(preview, "--disallowedTools") {
		t.Errorf("Preview missing --disallowedTools; got:\n%s", preview)
	}
	if !strings.Contains(preview, "Bash(git commit:*)") {
		t.Errorf("Preview missing Bash(git commit:*) in disallowed; got:\n%s", preview)
	}
}

// TestClaudeNativeSpawn_GatePreservesPersonaBaseTools verifies that when both
// PersonaToolsCSV and Gate are present, the rendered --allowedTools value
// contains BOTH the persona base tools AND the scoped Edit/Write/MultiEdit
// entries, with persona tokens appearing first.
//
// This is the core CF-2 fix: gated builders must retain their base read/test/MCP
// tool grants in addition to having their edits confined to declared files.
func TestClaudeNativeSpawn_GatePreservesPersonaBaseTools(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "build",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read,Glob,Grep,Bash(mage testPkg *),mcp__ta__update",
		Gate: &gate.Allowlist{
			EditPresent: true,
			Edit:        []string{"/abs/a.go"},
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	allowedToolsVal := findArgAfter(argv, "--allowedTools")
	if allowedToolsVal == "" {
		t.Fatalf("--allowedTools must be present; argv=%v", argv)
	}

	// Persona tokens must all be present.
	personaTokens := []string{"Read", "Glob", "Grep", "Bash(mage testPkg *)", "mcp__ta__update"}
	for _, token := range personaTokens {
		if !strings.Contains(allowedToolsVal, token) {
			t.Errorf("--allowedTools missing persona token %q; got %q", token, allowedToolsVal)
		}
	}

	// Scoped Edit/Write/MultiEdit entries must be present.
	if !strings.Contains(allowedToolsVal, "Edit(//abs/a.go)") {
		t.Errorf("--allowedTools missing Edit(//abs/a.go); got %q", allowedToolsVal)
	}
	if !strings.Contains(allowedToolsVal, "Write(//abs/a.go)") {
		t.Errorf("--allowedTools missing Write(//abs/a.go); got %q", allowedToolsVal)
	}
	if !strings.Contains(allowedToolsVal, "MultiEdit(//abs/a.go)") {
		t.Errorf("--allowedTools missing MultiEdit(//abs/a.go); got %q", allowedToolsVal)
	}

	// Verify no standalone "Bash" token without parens.
	parts := strings.Split(allowedToolsVal, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "Bash" {
			t.Errorf("--allowedTools must not contain standalone Bash; got %q", allowedToolsVal)
		}
	}
}

// TestClaudeNativeSpawn_GatePersonaCSVEmpty verifies that when PersonaToolsCSV
// is empty and Gate is present with EditPresent=true, renderArgs emits
// --allowedTools with only the scoped Edit/Write/MultiEdit entries
// (no persona prefix).
func TestClaudeNativeSpawn_GatePersonaCSVEmpty(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "x",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "", // empty — gate path only
		Gate: &gate.Allowlist{
			EditPresent: true,
			Edit:        []string{"/abs/a.go"},
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	allowedToolsVal := findArgAfter(argv, "--allowedTools")
	if allowedToolsVal == "" {
		t.Fatalf("--allowedTools must be present when EditPresent=true; argv=%v", argv)
	}

	// Should contain only the scoped entry, no persona prefix.
	if allowedToolsVal != "Edit(//abs/a.go),Write(//abs/a.go),MultiEdit(//abs/a.go)" {
		t.Errorf("--allowedTools with empty PersonaToolsCSV should be scoped-only; got %q", allowedToolsVal)
	}
}

// TestClaudeNativeSpawn_GatePersonaCSVOnlyNoEdit verifies that when
// Gate.EditPresent is false (read-only gate), --allowedTools renders
// just the persona CSV without any scoped entries.
func TestClaudeNativeSpawn_GatePersonaCSVOnlyNoEdit(t *testing.T) {
	argvFile, _, _ := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:            "ta-go-builder",
		Prompt:          "x",
		Model:           "haiku",
		PersonaBody:     "B",
		PersonaToolsCSV: "Read,Glob",
		Gate: &gate.Allowlist{
			EditPresent: false, // read-only gate
			Edit:        nil,
		},
	}

	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	allowedToolsVal := findArgAfter(argv, "--allowedTools")
	if allowedToolsVal == "" {
		t.Fatalf("--allowedTools must be present when PersonaToolsCSV non-empty; argv=%v", argv)
	}

	// Should equal just the persona CSV, no scoped entries (because EditPresent=false).
	if allowedToolsVal != "Read,Glob" {
		t.Errorf("--allowedTools with EditPresent=false should be persona-only; got %q", allowedToolsVal)
	}
}

// TestClaudeNativeBackend_RefusesWithoutAPIKey verifies that Spawn returns
// ErrAPIKeyRequired (wrapped) when ANTHROPIC_API_KEY is absent from the
// rendered environment, and that no subprocess is spawned.
func TestClaudeNativeBackend_RefusesWithoutAPIKey(t *testing.T) {
	// Install fake claude to verify no subprocess was spawned. The gate fires
	// before exec.CommandContext, so the recorder files won't be created.
	// We install the fake to confirm it's never invoked (which would be caught
	// by the gate firing before PATH lookup creates the recorder env vars).
	installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
	}

	result, err := cn.Spawn(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY is absent, got nil")
	}
	if !errors.Is(err, ErrAPIKeyRequired) {
		t.Errorf("error should wrap ErrAPIKeyRequired; got %v", err)
	}

	// result should be zero-valued (no subprocess ran)
	if result.ExitCode != 0 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Errorf("SpawnResult should be zero-valued on refusal; got %+v", result)
	}
}

// TestClaudeNativeBackend_AcceptsAPIKeyFromProcessEnv verifies that Spawn
// succeeds when ANTHROPIC_API_KEY is set via t.Setenv (process environment
// passthrough), and the spawned subprocess receives the key.
func TestClaudeNativeBackend_AcceptsAPIKeyFromProcessEnv(t *testing.T) {
	argvFile, _, envFile := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	req := SpawnRequest{
		Role:        "ta-go-builder",
		Prompt:      "x",
		Model:       "haiku",
		PersonaBody: "B",
	}

	result, err := cn.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	// argv should be recorded (subprocess was spawned)
	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)
	if len(argv) == 0 {
		t.Fatal("argv should be recorded when Spawn succeeds")
	}

	// env should contain ANTHROPIC_API_KEY=sentinel-token
	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)
	if !strings.Contains(envText, "ANTHROPIC_API_KEY=sentinel-token") {
		t.Errorf("env should contain ANTHROPIC_API_KEY=sentinel-token; got:\n%s", envText)
	}
}

// TestRenderEnv_StripsCleanRoomVars verifies that renderEnv drops the
// inherited HOME, CLAUDE_CONFIG_DIR, and XDG_CONFIG_HOME from the parent
// environment so the clean-room values appended in Spawn are never
// shadowed by a duplicate from the parent env.
func TestRenderEnv_StripsCleanRoomVars(t *testing.T) {
	cn := resolveClaudeNativeFromFixture(t)
	// Override with sentinel values that must NOT appear in renderEnv output.
	t.Setenv("HOME", "/tmp/parent-home-sentinel")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/parent-config-sentinel")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/parent-xdg-sentinel")

	envOut, err := cn.renderEnv(TemplateData{Model: "haiku"})
	if err != nil {
		t.Fatalf("renderEnv: %v", err)
	}

	for _, kv := range envOut {
		switch {
		case strings.HasPrefix(kv, "HOME="):
			t.Errorf("renderEnv leaked HOME into env: %q", kv)
		case strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR="):
			t.Errorf("renderEnv leaked CLAUDE_CONFIG_DIR into env: %q", kv)
		case strings.HasPrefix(kv, "XDG_CONFIG_HOME="):
			t.Errorf("renderEnv leaked XDG_CONFIG_HOME into env: %q", kv)
		}
	}
}

// TestEnsureStreamingArgs_AutoMode verifies that "auto" (or empty/unset)
// mode injects --output-format stream-json + --verbose when absent.
func TestEnsureStreamingArgs_AutoMode(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		mode  string
		want  []string
	}{
		{
			name:  "auto: inject stream-json + verbose",
			input: []string{"-p", "--bare"},
			mode:  "auto",
			want:  []string{"-p", "--bare", "--output-format", "stream-json", "--verbose"},
		},
		{
			name:  "auto: upgrade json to stream-json, add verbose",
			input: []string{"-p", "--output-format", "json"},
			mode:  "auto",
			want:  []string{"-p", "--output-format", "stream-json", "--verbose"},
		},
		{
			name:  "auto: already has stream-json, add verbose",
			input: []string{"-p", "--output-format", "stream-json"},
			mode:  "auto",
			want:  []string{"-p", "--output-format", "stream-json", "--verbose"},
		},
		{
			name:  "auto: already has both stream-json + verbose",
			input: []string{"-p", "--output-format", "stream-json", "--verbose"},
			mode:  "auto",
			want:  []string{"-p", "--output-format", "stream-json", "--verbose"},
		},
		{
			name:  "empty mode (default to auto): inject both",
			input: []string{"-p"},
			mode:  "",
			want:  []string{"-p", "--output-format", "stream-json", "--verbose"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := BackendConfig{
				EnvelopeFormat:   "claude_json",
				StructuredOutput: tt.mode,
			}
			result, err := ensureStreamingArgs(tt.input, cfg)
			if err != nil {
				t.Fatalf("ensureStreamingArgs: %v", err)
			}
			if !sliceEqual(result, tt.want) {
				t.Errorf("got %v, want %v", result, tt.want)
			}
		})
	}
}

// TestEnsureStreamingArgs_JsonMode verifies that "json" mode leaves
// --output-format json unchanged and does not inject --verbose.
func TestEnsureStreamingArgs_JsonMode(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "json mode: leave --output-format json unchanged",
			input: []string{"-p", "--output-format", "json"},
			want:  []string{"-p", "--output-format", "json"},
		},
		{
			name:  "json mode: no --output-format initially, no inject",
			input: []string{"-p"},
			want:  []string{"-p"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := BackendConfig{
				EnvelopeFormat:   "claude_json",
				StructuredOutput: "json",
			}
			result, err := ensureStreamingArgs(tt.input, cfg)
			if err != nil {
				t.Fatalf("ensureStreamingArgs: %v", err)
			}
			if !sliceEqual(result, tt.want) {
				t.Errorf("got %v, want %v", result, tt.want)
			}
		})
	}
}

// TestEnsureStreamingArgs_OffMode verifies that "off" mode disables
// enforcement and returns args unchanged.
func TestEnsureStreamingArgs_OffMode(t *testing.T) {
	input := []string{"-p", "--output-format", "json"}
	cfg := BackendConfig{
		EnvelopeFormat:   "claude_json",
		StructuredOutput: "off",
	}
	result, err := ensureStreamingArgs(input, cfg)
	if err != nil {
		t.Fatalf("ensureStreamingArgs: %v", err)
	}
	if !sliceEqual(result, input) {
		t.Errorf("off mode should not modify args; got %v, want %v", result, input)
	}
}

// TestEnsureStreamingArgs_NonClaudeJsonEnvelope verifies that non-claude_json
// envelopes (e.g., codex_stream) are not modified.
func TestEnsureStreamingArgs_NonClaudeJsonEnvelope(t *testing.T) {
	input := []string{"-c", "some-config"}
	cfg := BackendConfig{
		EnvelopeFormat:   "codex_stream",
		StructuredOutput: "auto",
	}
	result, err := ensureStreamingArgs(input, cfg)
	if err != nil {
		t.Fatalf("ensureStreamingArgs: %v", err)
	}
	if !sliceEqual(result, input) {
		t.Errorf("non-claude_json envelope should not be modified; got %v, want %v", result, input)
	}
}

// TestEnsureStreamingArgs_Idempotent verifies that calling ensureStreamingArgs
// multiple times produces the same result (no flag duplication).
func TestEnsureStreamingArgs_Idempotent(t *testing.T) {
	input := []string{"-p", "--bare"}
	cfg := BackendConfig{
		EnvelopeFormat:   "claude_json",
		StructuredOutput: "auto",
	}

	result1, _ := ensureStreamingArgs(input, cfg)
	result2, _ := ensureStreamingArgs(result1, cfg)

	if !sliceEqual(result1, result2) {
		t.Errorf("idempotency failed: first call %v, second call %v", result1, result2)
	}
}

// sliceEqual is a helper to compare two string slices for equality.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSpawn_CleanRoomEnvIsolation verifies that the spawned child process
// receives a fresh clean-room HOME (distinct from the parent's HOME) and
// that the temporary directory is removed after Spawn returns.
//
// The env-merge logic is validated indirectly via the fake-claude-happy
// fixture's env recorder: we extract the child's HOME= line and confirm:
//  1. HOME in the child differs from the parent's HOME (isolation).
//  2. The cleanroom dir no longer exists after Spawn returns (cleanup ran).
//  3. CLAUDE_CONFIG_DIR is present in the child env.
func TestSpawn_CleanRoomEnvIsolation(t *testing.T) {
	_, _, envFile := installFakeClaude(t, "fake-claude-happy")
	cn := resolveClaudeNativeFromFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-token")

	parentHome := os.Getenv("HOME") // set by resolveClaudeNativeFromFixture

	req := SpawnRequest{
		Role: "ta-go-builder", Prompt: "x",
		Model: "haiku", PersonaBody: "B",
	}
	if _, err := cn.Spawn(context.Background(), req); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)

	// Locate the HOME= line the child process saw.
	var crHome string
	for _, line := range strings.Split(envText, "\n") {
		if strings.HasPrefix(line, "HOME=") {
			crHome = strings.TrimPrefix(line, "HOME=")
			break
		}
	}
	if crHome == "" {
		t.Fatal("child env must contain a HOME= entry (clean-room injection)")
	}
	if crHome == parentHome {
		t.Errorf("child HOME %q must differ from parent HOME %q", crHome, parentHome)
	}
	// Cleanup must have run: the cleanroom dir should be gone.
	if _, statErr := os.Stat(crHome); !os.IsNotExist(statErr) {
		t.Errorf("cleanroom dir %q should not exist after Spawn returns; stat: %v", crHome, statErr)
	}
	// CLAUDE_CONFIG_DIR must also be injected.
	if !strings.Contains(envText, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("child env missing CLAUDE_CONFIG_DIR=; env:\n%s", envText)
	}
}

// TestClaudeNativeBackend_AcceptsAPIKeyFromBackendConfigEnv verifies that
// Spawn succeeds when ANTHROPIC_API_KEY is supplied via BackendConfig.Env
// templating (e.g., {{env "FAKE_HOST_API_KEY"}}), even when the process
// environment ANTHROPIC_API_KEY is unset.
func TestClaudeNativeBackend_AcceptsAPIKeyFromBackendConfigEnv(t *testing.T) {
	_, _, envFile := installFakeClaude(t, "fake-claude-happy")

	// Seed a project-rung fixture with BackendConfig.Env containing a templated
	// ANTHROPIC_API_KEY entry that pulls from a host env var.
	const tomlWithTemplatedKey = `
[backends.claude-native]
command = "claude"
args = ["-p", "--model", "{{.Model}}"]
env = ["ANTHROPIC_API_KEY={{env \"FAKE_HOST_API_KEY\"}}"]
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
	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), tomlWithTemplatedKey)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)
	t.Setenv("FAKE_HOST_API_KEY", "templated-sentinel")
	// Explicitly unset process ANTHROPIC_API_KEY to verify the template is
	// the source of truth, not process env passthrough.
	t.Setenv("ANTHROPIC_API_KEY", "")

	b, err := Resolve(projectDir, "claude-native")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	cn := b.(*claudeNativeBackend)

	req := SpawnRequest{Role: "ta-go-builder", Prompt: "x", Model: "haiku", PersonaBody: "B"}
	result, err := cn.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}

	// env recorder should contain the templated key
	envBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env recorder: %v", err)
	}
	envText := string(envBytes)
	if !strings.Contains(envText, "ANTHROPIC_API_KEY=templated-sentinel") {
		t.Errorf("env should contain templated ANTHROPIC_API_KEY=templated-sentinel; got:\n%s", envText)
	}
}
