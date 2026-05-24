// Package installseed owns the first-install seeding of sand's
// user-configurable backends config at $HOME/.config/sand/backends.toml.
//
// Why this lives in its own package: the seed function must be reachable from
// magefile.go (which is gated by //go:build mage) AND must be testable via
// standard `go test ./...`. A regular Go subpackage satisfies both — magefile
// imports installseed.Seed and the package's tests run under any tag set.
//
// The seed contents mirror SAND-SPEC §5.1: one ACTIVE [backends.claude-
// native] block matching the current `claude -p` invocation behavior, plus
// four COMMENTED example blocks (codex-exec, ollama-local, ollama-cloud,
// together-ai) so a user can uncomment-and-go for any of the supported
// providers without authoring TOML from scratch.
//
// Non-overwrite invariant: Seed NEVER replaces an existing backends.toml.
// Pre-existing files are preserved byte-for-byte regardless of content.
package installseed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultBackendsTOML is the baseline content written to
// $HOME/.config/sand/backends.toml on first install. The claude-native block
// is uncommented (active baseline); the remaining four provider examples are
// fully filled but #-commented so users can uncomment-and-go.
//
// Exported for visibility in tests and for any caller that wants to render
// the same baseline content for diagnostics.
const DefaultBackendsTOML = `# sand backends.toml — per-backend spawn templates.
#
# Each [backends.NAME] entry defines how sand builds the os/exec command for
# a backend KIND. chains.toml references these entries by name in the
# ` + "`backend = \"X\"`" + ` field. Add a new provider by adding a new
# [backends.NAME] block — no Go code change required.
#
# Template substitution applies inside string values:
#   {{.Model}}             - tier's model name (e.g. "haiku", "gpt-5.4")
#   {{.CWD}}               - caller project directory
#   {{.PersonaBody}}       - loaded persona file body (system prompt)
#   {{.PersonaToolsCSV}}   - persona's tools joined with commas
#   {{.McpConfigPath}}     - <cwd>/.mcp.json when present, else empty
#   {{env "VAR"}}          - process environment variable lookup
#   {{.Prompt}}            - the dispatch prompt text
#
# The [backends.claude-native] block below is ACTIVE (uncommented) and
# matches sand's current claude -p invocation. The remaining four examples
# are COMMENTED — uncomment the block + adjust env vars to enable.

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
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true

# -----------------------------------------------------------------------------
# Example: codex exec --ephemeral. Uncomment + ensure ` + "`codex`" + ` is on PATH.
# -----------------------------------------------------------------------------
# [backends.codex-exec]
# command = "codex"
# args = [
#   "exec",
#   "--ephemeral",
#   "--ignore-rules",
#   "--skip-git-repo-check",
#   "-C", "{{.CWD}}",
#   "-m", "{{.Model}}",
# ]
# mcp_injection = "codex_inline_toml"
# slots_default = 0
# envelope_format = "codex_stream"
# stdin_prompt = true

# -----------------------------------------------------------------------------
# Example: ollama-local — routes through claude CLI pointed at local daemon.
# Requires ollama running at http://localhost:11434.
# -----------------------------------------------------------------------------
# [backends.ollama-local]
# command = "claude"
# env = [
#   "ANTHROPIC_BASE_URL=http://localhost:11434",
#   "ANTHROPIC_API_KEY=ollama",
# ]
# args = [
#   "-p",
#   "--bare",
#   "--model", "{{.Model}}",
#   "--output-format", "json",
#   "--no-session-persistence",
#   "--append-system-prompt", "{{.PersonaBody}}",
# ]
# mcp_config_arg = "--mcp-config"
# allowed_tools_arg = "--allowedTools"
# allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
# slots_default = 1
# envelope_format = "claude_json"
# stdin_prompt = true

# -----------------------------------------------------------------------------
# Example: ollama-cloud — routes through claude CLI pointed at ollama.com API.
# Requires OLLAMA_API_KEY in the environment.
# -----------------------------------------------------------------------------
# [backends.ollama-cloud]
# command = "claude"
# env = [
#   "ANTHROPIC_BASE_URL=https://ollama.com/api",
#   "ANTHROPIC_API_KEY={{env \"OLLAMA_API_KEY\"}}",
# ]
# args = [
#   "-p",
#   "--bare",
#   "--model", "{{.Model}}",
#   "--output-format", "json",
#   "--no-session-persistence",
#   "--append-system-prompt", "{{.PersonaBody}}",
# ]
# mcp_config_arg = "--mcp-config"
# allowed_tools_arg = "--allowedTools"
# allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
# slots_default = 3
# envelope_format = "claude_json"
# stdin_prompt = true

# -----------------------------------------------------------------------------
# Example: together.ai — routes through claude CLI with Together API endpoint.
# Requires TOGETHER_API_KEY in the environment.
# -----------------------------------------------------------------------------
# [backends.together-ai]
# command = "claude"
# env = [
#   "ANTHROPIC_BASE_URL=https://api.together.xyz/v1",
#   "ANTHROPIC_API_KEY={{env \"TOGETHER_API_KEY\"}}",
# ]
# args = [
#   "-p",
#   "--bare",
#   "--model", "{{.Model}}",
#   "--output-format", "json",
#   "--no-session-persistence",
#   "--append-system-prompt", "{{.PersonaBody}}",
# ]
# mcp_config_arg = "--mcp-config"
# allowed_tools_arg = "--allowedTools"
# allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
# slots_default = 0
# envelope_format = "claude_json"
# stdin_prompt = true
`

// ErrHomeRequired is returned by Seed when the supplied home directory is
// empty. Seed never invents a default home — the caller (typically
// magefile.go's Install target) is responsible for resolving the user's
// home directory and passing it in explicitly.
var ErrHomeRequired = errors.New("installseed: home directory required")

// Seed writes the baseline backends.toml to <home>/.config/sand/backends.toml
// when that file does not already exist. If the file is already present,
// Seed returns nil without touching it — pre-existing user content is
// preserved byte-for-byte.
//
// Seed creates the enclosing <home>/.config/sand directory (mode 0o755) if
// missing. The seeded file is written with mode 0o644.
//
// Seed never overwrites. It returns nil on no-op (file already exists), nil
// on successful first-install, and a wrapped error on filesystem failure.
func Seed(home string) error {
	return seedFile(home, "backends.toml", DefaultBackendsTOML)
}

// SeedChains writes the baseline chains.toml to <home>/.config/sand/chains.toml
// when that file does not already exist. Same non-overwrite + mkdir contract
// as Seed.
//
// The baseline references only the claude-native backend so the seed is
// portable across users — anyone who has installed sand has claude-native
// active by default (Seed writes its block uncommented). Users who want
// ollama / codex / together-ai tiers must edit chains.toml after activating
// the matching backend block in backends.toml.
func SeedChains(home string) error {
	return seedFile(home, "chains.toml", DefaultChainsTOML)
}

// seedFile is the shared body for Seed and SeedChains: stat target,
// MkdirAll the enclosing dir, write the supplied baseline content iff the
// target does not exist. Never overwrites.
func seedFile(home, name, content string) error {
	if home == "" {
		return ErrHomeRequired
	}

	configDir := filepath.Join(home, ".config", "sand")
	target := filepath.Join(configDir, name)

	// Non-overwrite check FIRST. A successful Stat means the file exists and
	// we MUST NOT touch it; any error other than ENOENT also halts (we won't
	// blindly overwrite on permission errors either — bubble up).
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("installseed: stat %s: %w", target, err)
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("installseed: mkdir %s: %w", configDir, err)
	}

	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return fmt.Errorf("installseed: write %s: %w", target, err)
	}

	return nil
}

// DefaultChainsTOML is the baseline content written to
// $HOME/.config/sand/chains.toml on first install by SeedChains. It declares
// claude-native-only chains for the five canonical roles so a fresh install
// boots a working chain regardless of which backends the user later
// uncomments in backends.toml.
//
// Users override per-project by dropping a `.claude/sand-chains.toml` in
// the project root; the project rung wins per the hierarchical resolver.
const DefaultChainsTOML = `# sand chains.toml — per-role fallback chains.
#
# This baseline references claude-native only so it works out-of-the-box
# regardless of which backends you uncomment in backends.toml. Edit any
# role's tier list to add ollama-cloud / ollama-local / codex-exec tiers
# AFTER uncommenting the matching backend block in
# ~/.config/sand/backends.toml.
#
# Tier fields:
#   backend     — must match a [backends.NAME] entry in backends.toml
#   model       — backend-specific model identifier
#   slots       — cross-process concurrency cap; 0 = unlimited
#   wait_max    — seconds to wait for a slot before advancing
#   opts        — opaque extra CLI flags forwarded to the backend command
#
# Hierarchical resolution (first hit wins): project .claude/sand-chains.toml
# > $XDG_CONFIG_HOME/sand/chains.toml > ~/.config/sand/chains.toml >
# ~/.sand/chains.toml.

[chains]

"ta-go-builder" = [
  { backend = "claude-native", model = "haiku",  slots = 0, wait_max = 0, opts = "" },
  { backend = "claude-native", model = "sonnet", slots = 0, wait_max = 0, opts = "" },
]

"ta-go-planning" = [
  { backend = "claude-native", model = "opus", slots = 0, wait_max = 0, opts = "" },
]

"ta-go-qa-falsification" = [
  { backend = "claude-native", model = "opus", slots = 0, wait_max = 0, opts = "" },
]

"ta-go-qa-proof" = [
  { backend = "claude-native", model = "opus", slots = 0, wait_max = 0, opts = "" },
]

"ta-closeout" = [
  { backend = "claude-native", model = "opus", slots = 0, wait_max = 0, opts = "" },
]
`
