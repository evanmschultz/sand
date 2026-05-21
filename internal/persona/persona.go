// Package persona defines the in-memory representation of a sand role persona
// and the loader that materializes it from a caller project's .claude/agents
// tree.
//
// Personas are loaded from `<caller-project>/.claude/agents/<role>.md` at
// dispatch time (see SAND-SPEC §2.5 and §3.3). The markdown file consists of
// YAML frontmatter (Name, Description, Model, Tools) plus a body that becomes
// the system prompt for the spawned headless agent.
//
// Persona content is NOT baked into the sand binary; it is read from the
// caller project tree on every dispatch.
package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors for the persona loader. Callers use errors.Is to
// distinguish frontmatter parse failures from file-read failures (which
// wrap os.ErrNotExist and friends via the underlying os.ReadFile error).
var (
	// ErrMissingOpenDelimiter indicates the file did not begin with a
	// `---` frontmatter open delimiter (after permitted leading blank lines).
	ErrMissingOpenDelimiter = errors.New("persona: missing opening frontmatter delimiter")

	// ErrMissingCloseDelimiter indicates an opening `---` delimiter was
	// found but no closing `---` delimiter followed it.
	ErrMissingCloseDelimiter = errors.New("persona: missing closing frontmatter delimiter")

	// ErrMalformedFrontmatterLine indicates a non-blank frontmatter line
	// lacked a `:` separator and could not be parsed as a key/value pair.
	ErrMalformedFrontmatterLine = errors.New("persona: malformed frontmatter line")
)

// Persona is the parsed representation of a single role definition file.
//
// Fields Name, Description, Model, and Tools come from the YAML frontmatter
// of the persona markdown file. Body is the remaining markdown content after
// the frontmatter block, used as the spawned agent's system prompt.
type Persona struct {
	// Name is the role identifier (e.g. "ta-go-builder"), matching the
	// frontmatter `name` field and the basename of the source markdown file.
	Name string

	// Description is a short human-readable summary of the persona's purpose,
	// taken from the frontmatter `description` field.
	Description string

	// Model is the backend model identifier the dispatcher should target for
	// this role (e.g. "qwen2.5-coder:7b", "opus", "gpt-5.4+medium"), taken
	// from the frontmatter `model` field.
	Model string

	// Tools is the per-role tool allowlist parsed from the frontmatter
	// `tools` field. It is passed to the backend as the agent's sandbox
	// spec (e.g. Claude Code's --allowedTools).
	Tools []string

	// Body is the markdown content following the frontmatter block. It is
	// used as the system prompt for the spawned headless agent.
	Body string
}

// Load reads and parses the persona file for role under projectDir.
//
// The resolved path is filepath.Join(projectDir, ".claude", "agents",
// role+".md"). The file is expected to begin with a YAML-frontmatter block
// delimited by line-start `---` markers. The frontmatter is parsed as scalar
// `key: value` lines (split on the first `:`, with keys and values trimmed).
// Recognized keys populate the corresponding Persona fields; the `tools`
// value is comma-split, each entry trimmed, and empty entries are skipped so
// inputs like `Read,,Grep` do not produce empty tool tokens.
//
// All remaining content after the closing `---` delimiter is preserved as
// Body. Delimiter detection tolerates CRLF line endings and trailing
// whitespace on the `---` line.
//
// Load does not validate field semantics (e.g. required fields, model
// identifiers, tool well-formedness); callers layer that on top. Malformed
// inputs and missing files are handled by sibling error-path code; this
// happy-path implementation returns an error only when the file cannot be
// read or when no frontmatter block is present.
func Load(projectDir, role string) (Persona, error) {
	path := filepath.Join(projectDir, ".claude", "agents", role+".md")

	raw, err := os.ReadFile(path)
	if err != nil {
		return Persona{}, fmt.Errorf("persona: read %s: %w", path, err)
	}

	// Normalize CRLF so downstream splitting can rely on `\n`. We do this
	// once on the byte slice rather than per-line so the body preserves its
	// original line discipline modulo CR stripping.
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	frontmatter, body, err := splitFrontmatter(text)
	if err != nil {
		return Persona{}, fmt.Errorf("persona: parse %s: %w", path, err)
	}

	p := Persona{Body: body}
	for _, line := range strings.Split(frontmatter, "\n") {
		// Skip blank frontmatter lines without complaint; permissive on
		// purpose because YAML allows them.
		if strings.TrimSpace(line) == "" {
			continue
		}

		key, value, ok := splitFirst(line, ':')
		if !ok {
			// A non-blank frontmatter line without a `:` is malformed.
			// Wrap the sentinel so callers can errors.Is against
			// ErrMalformedFrontmatterLine.
			return Persona{}, fmt.Errorf("persona: parse %s: %w: %q", path, ErrMalformedFrontmatterLine, line)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "name":
			p.Name = value
		case "description":
			p.Description = value
		case "model":
			p.Model = value
		case "tools":
			p.Tools = parseTools(value)
		}
	}

	return p, nil
}

// splitFrontmatter separates the leading YAML frontmatter block (delimited
// by line-start `---` markers, with possible trailing whitespace) from the
// remaining body. text is expected to have already been CRLF-normalized.
func splitFrontmatter(text string) (frontmatter, body string, err error) {
	lines := strings.Split(text, "\n")

	openIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "---" {
			openIdx = i
			break
		}
		if strings.TrimSpace(line) != "" {
			// Non-delimiter, non-blank content before the open delimiter
			// means there is no frontmatter block.
			return "", "", ErrMissingOpenDelimiter
		}
	}
	if openIdx < 0 {
		return "", "", ErrMissingOpenDelimiter
	}

	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		if trimmed == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return "", "", ErrMissingCloseDelimiter
	}

	frontmatter = strings.Join(lines[openIdx+1:closeIdx], "\n")
	if closeIdx+1 < len(lines) {
		body = strings.Join(lines[closeIdx+1:], "\n")
	}
	// Body convention: drop a single leading blank line so the spawned
	// agent's system prompt does not start with a stray newline. Keeps
	// fixture round-trips clean without losing intentional internal blanks.
	body = strings.TrimPrefix(body, "\n")
	return frontmatter, body, nil
}

// splitFirst splits s on the first occurrence of sep, returning the parts
// before and after. ok is false when sep is absent.
func splitFirst(s string, sep byte) (head, tail string, ok bool) {
	idx := strings.IndexByte(s, sep)
	if idx < 0 {
		return s, "", false
	}
	return s[:idx], s[idx+1:], true
}

// parseTools comma-splits a tools-field value, trims each entry, and drops
// entries that are empty after trimming. The result is a non-nil slice when
// at least one tool survives and a zero-length slice otherwise. Callers
// should treat nil and empty as equivalent.
func parseTools(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
