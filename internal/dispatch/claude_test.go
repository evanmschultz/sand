package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
)

// installFakeClaude prepends a fresh temp dir to PATH containing a copy of
// the named fixture shell script, exposed under the configured claudeBin
// name. The script's stdout becomes the spawn's stdout. The script also
// writes its argv and stdin to recorder files so tests can assert against
// the exact CLI invocation. Returns the recorder paths for inspection.
//
// The PATH override is undone via t.Cleanup so tests stay hermetic.
func installFakeClaude(t *testing.T, fixture string) (argvOut, stdinOut string) {
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

	scriptPath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake claude script: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("FAKE_CLAUDE_ARGV_OUT", argvOut)
	t.Setenv("FAKE_CLAUDE_STDIN_OUT", stdinOut)

	return argvOut, stdinOut
}

func TestRunClaudeNative_HappyPath(t *testing.T) {
	argvFile, stdinFile := installFakeClaude(t, "fake-claude-happy")

	ctx := context.Background()
	params := Params{
		Role:   "ta-go-builder",
		Prompt: "build droplet X",
		CWD:    t.TempDir(),
	}
	p := persona.Persona{
		Name:        "ta-go-builder",
		Description: "builder",
		Model:       "haiku",
		Tools:       []string{"Read", "Edit", "Bash(mage testFunc *)"},
		Body:        "PERSONA BODY LINE 1\nPERSONA BODY LINE 2\n",
	}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	result, err := runClaudeNative(ctx, params, p, tier, "/abs/cwd/.mcp.json")
	if err != nil {
		t.Fatalf("runClaudeNative: %v", err)
	}

	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d want 0; stderr=%q", result.ExitCode, string(result.Stderr))
	}
	if !strings.Contains(string(result.Stdout), `"result"`) {
		t.Fatalf("stdout missing fixture marker; got %q", string(result.Stdout))
	}

	argvBytes, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv recorder: %v", err)
	}
	argv := splitArgv(argvBytes)

	// argv[0] is the script itself; argv[1:] is what runClaudeNative
	// constructed. Assert each required flag is present.
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

func TestRunClaudeNative_ModelOverrideWins(t *testing.T) {
	argvFile, _ := installFakeClaude(t, "fake-claude-happy")

	ctx := context.Background()
	params := Params{
		Role:          "ta-go-builder",
		Prompt:        "x",
		ModelOverride: "qwen3-coder:30b",
	}
	p := persona.Persona{Name: "ta-go-builder", Body: "B"}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	if _, err := runClaudeNative(ctx, params, p, tier, ""); err != nil {
		t.Fatalf("runClaudeNative: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if !containsAdjacent(argv, "--model", "qwen3-coder:30b") {
		t.Errorf("model override did not win; argv=%v", argv)
	}
}

func TestRunClaudeNative_OmitsMCPConfigWhenEmpty(t *testing.T) {
	argvFile, _ := installFakeClaude(t, "fake-claude-happy")

	ctx := context.Background()
	params := Params{Role: "ta-go-builder", Prompt: "x"}
	p := persona.Persona{Name: "ta-go-builder", Body: "B", Tools: []string{"Read"}}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	if _, err := runClaudeNative(ctx, params, p, tier, ""); err != nil {
		t.Fatalf("runClaudeNative: %v", err)
	}

	argvBytes, _ := os.ReadFile(argvFile)
	argv := splitArgv(argvBytes)
	if containsArg(argv, "--mcp-config") {
		t.Errorf("--mcp-config should be omitted when path empty; argv=%v", argv)
	}
}

func TestRunClaudeNative_MissingBinaryReturnsError(t *testing.T) {
	// Point PATH at an empty dir so claude can't be found.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	ctx := context.Background()
	params := Params{Role: "ta-go-builder", Prompt: "x"}
	p := persona.Persona{Name: "ta-go-builder", Body: "B"}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	_, err := runClaudeNative(ctx, params, p, tier, "")
	if err == nil {
		t.Fatal("expected error when claude is not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "locate claude on PATH") {
		t.Errorf("error message should mention PATH lookup; got %q", err)
	}
}

func TestRunClaudeNative_ContextCancellation(t *testing.T) {
	installFakeClaude(t, "fake-claude-sleep")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	params := Params{Role: "ta-go-builder", Prompt: "x"}
	p := persona.Persona{Name: "ta-go-builder", Body: "B"}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	start := time.Now()
	_, err := runClaudeNative(ctx, params, p, tier, "")
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

func TestRunClaudeNative_NonZeroExitNotAnError(t *testing.T) {
	installFakeClaude(t, "fake-claude-fail")

	ctx := context.Background()
	params := Params{Role: "ta-go-builder", Prompt: "x"}
	p := persona.Persona{Name: "ta-go-builder", Body: "B"}
	tier := chains.Tier{Backend: "claude-native", Model: "haiku"}

	result, err := runClaudeNative(ctx, params, p, tier, "")
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

// splitArgv parses a NUL-separated argv recording (one arg per record, no
// trailing record). The fake-claude shell scripts write argv this way so
// arguments with embedded newlines (notably --append-system-prompt) round-
// trip without being shredded by a naive line-split.
func splitArgv(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	// Trailing empty token from the final separator.
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

// containsAdjacent reports whether argv contains flag immediately followed
// by value (the standard flag-pair shape).
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
