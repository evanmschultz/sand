package backends

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fullFixtureTOML is a backends.toml document that populates every one
// of the 10 BackendConfig fields verbatim per SAND-SPEC §5.1.
// Used by both TestBackendResolve and TestBackendConfigTOMLDecodeAllFields
// to exercise full round-trip fidelity.
const fullFixtureTOML = `
[backends.claude-native]
command = "claude"
args = ["-p", "--bare", "--model", "{{model}}", "--output-format", "json"]
env = ["FOO=bar", "BAZ=qux"]
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{persona_tools_csv}}"
tools_arg = "--tools"
tools_csv_template = "{{.PersonaToolNamesCSV}}"
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true
mcp_injection = ""

[backends.codex-exec]
command = "codex"
args = ["exec", "--ephemeral", "-C", "{{cwd}}", "-m", "{{model}}"]
env = []
mcp_config_arg = ""
allowed_tools_arg = ""
allowed_tools_csv_template = ""
slots_default = 7
envelope_format = "codex_stream"
stdin_prompt = false
mcp_injection = "codex_inline_toml"
`

// TestBackendResolve covers the drop_011 Resolve factory: known-name
// success, unknown-name ErrUnknownBackend, missing config-file
// ErrBackendsConfigNotFound bubble-up, and stub-method
// ErrBackendNotImplemented surface.
//
// Distinct from TestResolveBackendsConfig (which covers path
// resolution only); this test exercises the file-parse + map-lookup
// layered on top.
func TestBackendResolve(t *testing.T) {
	// No t.Parallel: subtests use t.Setenv on process-global state.

	t.Run("known_name_resolves_to_backend", func(t *testing.T) {
		projectDir := t.TempDir()
		homeDir := t.TempDir()

		// Seed the project rung so ResolveBackendsConfig wins on it
		// without HOME/XDG interference.
		writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), fullFixtureTOML)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		b, err := Resolve(projectDir, "claude-native")
		if err != nil {
			t.Fatalf("Resolve: unexpected error: %v", err)
		}
		if b == nil {
			t.Fatal("Resolve: returned nil Backend without error")
		}

		// Sanity check: the resolved value is the concrete
		// claudeNativeBackend bound to the fixture config. End-to-end
		// Spawn / Preview behavior is exercised in claude_native_test.go;
		// here we only assert the factory's type binding so a future
		// refactor that drops the concrete return type surfaces at
		// build / test time.
		if _, ok := b.(*claudeNativeBackend); !ok {
			t.Errorf("Resolve: expected *claudeNativeBackend, got %T", b)
		}
	})

	t.Run("unknown_name_returns_ErrUnknownBackend", func(t *testing.T) {
		projectDir := t.TempDir()
		homeDir := t.TempDir()

		writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), fullFixtureTOML)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		b, err := Resolve(projectDir, "does-not-exist")
		if !errors.Is(err, ErrUnknownBackend) {
			t.Fatalf("expected ErrUnknownBackend, got %v", err)
		}
		if b != nil {
			t.Errorf("expected nil Backend on miss, got %#v", b)
		}
		// The requested name should appear in the wrapped message for
		// diagnostic context — protects against silent regressions
		// where the sentinel matches but the message is useless.
		if !strings.Contains(err.Error(), "does-not-exist") {
			t.Errorf("error message missing requested name: %v", err)
		}
	})

	t.Run("missing_config_file_wraps_ErrBackendsConfigNotFound", func(t *testing.T) {
		projectDir := t.TempDir() // no .claude/sand-backends.toml seeded
		homeDir := t.TempDir()    // no HOME-anchored config seeded

		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		b, err := Resolve(projectDir, "claude-native")
		if !errors.Is(err, ErrBackendsConfigNotFound) {
			t.Fatalf("expected ErrBackendsConfigNotFound, got %v", err)
		}
		if b != nil {
			t.Errorf("expected nil Backend on miss, got %#v", b)
		}
	})

	t.Run("malformed_toml_returns_decode_error", func(t *testing.T) {
		// Defensive: not in the brief's enumerated cases but covers
		// the third error branch in Resolve (read/decode failure).
		// Required to keep the package's coverage above the project's
		// 70% floor on touched packages.
		projectDir := t.TempDir()
		homeDir := t.TempDir()

		writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), "this is = = not toml [[[[")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		b, err := Resolve(projectDir, "claude-native")
		if err == nil {
			t.Fatal("expected decode error, got nil")
		}
		// Must NOT be confused with ErrUnknownBackend or
		// ErrBackendsConfigNotFound — those have distinct semantics.
		if errors.Is(err, ErrUnknownBackend) {
			t.Errorf("decode error must not satisfy ErrUnknownBackend: %v", err)
		}
		if errors.Is(err, ErrBackendsConfigNotFound) {
			t.Errorf("decode error must not satisfy ErrBackendsConfigNotFound: %v", err)
		}
		if b != nil {
			t.Errorf("expected nil Backend on decode failure, got %#v", b)
		}
	})
}

// TestBackendConfigTOMLDecodeAllFields verifies every one of the 10
// BackendConfig fields round-trips correctly when decoded from a TOML
// fixture containing the full set per SAND-SPEC §5.1. Per-field
// assertions (not reflection-equality) so a single failing field
// surfaces a precise diagnostic.
func TestBackendConfigTOMLDecodeAllFields(t *testing.T) {
	projectDir := t.TempDir()
	homeDir := t.TempDir()

	writeFile(t, filepath.Join(projectDir, ".claude", "sand-backends.toml"), fullFixtureTOML)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", homeDir)

	// Reach into the decoder path Resolve exercises by resolving both
	// fixture entries and unwrapping the cfg field for assertion. We
	// can't read the unexported cfg directly from outside the package
	// in a black-box test, but this IS the package's own test file
	// so the unexported access is in-bounds.
	b1, err := Resolve(projectDir, "claude-native")
	if err != nil {
		t.Fatalf("Resolve claude-native: %v", err)
	}
	// codex-exec uses envelope_format=codex_stream. After drop_005 wired
	// codexExecBackend into Resolve, the factory returns it directly
	// instead of declining with ErrUnsupportedEnvelopeFormat. Assert the
	// positive resolution + concrete type so a future refactor that
	// drops the codex_stream case surfaces at test time.
	b2, err := Resolve(projectDir, "codex-exec")
	if err != nil {
		t.Fatalf("Resolve codex-exec: %v", err)
	}
	cx, ok := b2.(*codexExecBackend)
	if !ok {
		t.Fatalf("Resolve codex-exec: expected *codexExecBackend, got %T", b2)
	}
	gotCXcfg := cx.cfg

	cn, ok := b1.(*claudeNativeBackend)
	if !ok {
		t.Fatalf("Resolve claude-native: expected *claudeNativeBackend, got %T", b1)
	}

	// claude-native fixture assertions: every field populated, exercised
	// against the SAND-SPEC §5.1 example.
	gotCN := cn.cfg
	if gotCN.Command != "claude" {
		t.Errorf("claude-native Command: got %q want %q", gotCN.Command, "claude")
	}
	wantArgs := []string{"-p", "--bare", "--model", "{{model}}", "--output-format", "json"}
	if !reflect.DeepEqual(gotCN.Args, wantArgs) {
		t.Errorf("claude-native Args: got %v want %v", gotCN.Args, wantArgs)
	}
	wantEnv := []string{"FOO=bar", "BAZ=qux"}
	if !reflect.DeepEqual(gotCN.Env, wantEnv) {
		t.Errorf("claude-native Env: got %v want %v", gotCN.Env, wantEnv)
	}
	if gotCN.McpConfigArg != "--mcp-config" {
		t.Errorf("claude-native McpConfigArg: got %q want %q", gotCN.McpConfigArg, "--mcp-config")
	}
	if gotCN.AllowedToolsArg != "--allowedTools" {
		t.Errorf("claude-native AllowedToolsArg: got %q want %q", gotCN.AllowedToolsArg, "--allowedTools")
	}
	if gotCN.AllowedToolsCSVTemplate != "{{persona_tools_csv}}" {
		t.Errorf("claude-native AllowedToolsCSVTemplate: got %q want %q",
			gotCN.AllowedToolsCSVTemplate, "{{persona_tools_csv}}")
	}
	if gotCN.SlotsDefault != 0 {
		t.Errorf("claude-native SlotsDefault: got %d want 0", gotCN.SlotsDefault)
	}
	if gotCN.EnvelopeFormat != "claude_json" {
		t.Errorf("claude-native EnvelopeFormat: got %q want %q", gotCN.EnvelopeFormat, "claude_json")
	}
	if !gotCN.StdinPrompt {
		t.Errorf("claude-native StdinPrompt: got false want true")
	}
	if gotCN.McpInjection != "" {
		t.Errorf("claude-native McpInjection: got %q want empty", gotCN.McpInjection)
	}

	// codex-exec fixture assertions: exercises the contrasting field
	// values (mcp_injection set, stdin_prompt false, slots_default
	// non-zero) so per-field decode paths are all hit.
	gotCX := gotCXcfg
	if gotCX.Command != "codex" {
		t.Errorf("codex-exec Command: got %q want %q", gotCX.Command, "codex")
	}
	wantCxArgs := []string{"exec", "--ephemeral", "-C", "{{cwd}}", "-m", "{{model}}"}
	if !reflect.DeepEqual(gotCX.Args, wantCxArgs) {
		t.Errorf("codex-exec Args: got %v want %v", gotCX.Args, wantCxArgs)
	}
	if len(gotCX.Env) != 0 {
		t.Errorf("codex-exec Env: got %v want empty", gotCX.Env)
	}
	if gotCX.McpConfigArg != "" {
		t.Errorf("codex-exec McpConfigArg: got %q want empty", gotCX.McpConfigArg)
	}
	if gotCX.AllowedToolsArg != "" {
		t.Errorf("codex-exec AllowedToolsArg: got %q want empty", gotCX.AllowedToolsArg)
	}
	if gotCX.AllowedToolsCSVTemplate != "" {
		t.Errorf("codex-exec AllowedToolsCSVTemplate: got %q want empty", gotCX.AllowedToolsCSVTemplate)
	}
	if gotCX.SlotsDefault != 7 {
		t.Errorf("codex-exec SlotsDefault: got %d want 7", gotCX.SlotsDefault)
	}
	if gotCX.EnvelopeFormat != "codex_stream" {
		t.Errorf("codex-exec EnvelopeFormat: got %q want %q", gotCX.EnvelopeFormat, "codex_stream")
	}
	if gotCX.StdinPrompt {
		t.Errorf("codex-exec StdinPrompt: got true want false")
	}
	if gotCX.McpInjection != "codex_inline_toml" {
		t.Errorf("codex-exec McpInjection: got %q want %q", gotCX.McpInjection, "codex_inline_toml")
	}
}

// TestBackendInterfaceCompileCheck pins the Backend interface contract
// at compile time: every concrete Backend impl must satisfy Backend.
// This is a belt-and-braces guard so a future refactor that drops a
// method signature surfaces at build time rather than only via failing
// runtime tests. Per drop_005 L3 amendment B3, the interface widened to
// include EnvelopeFormat() string — both concrete backends must satisfy
// the wider contract.
func TestBackendInterfaceCompileCheck(t *testing.T) {
	var _ Backend = (*claudeNativeBackend)(nil)
	var _ Backend = (*codexExecBackend)(nil)
}
