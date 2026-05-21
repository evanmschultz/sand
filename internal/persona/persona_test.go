package persona

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadFixtureRoundTrip exercises Load against the committed fixture
// under testdata/agents to confirm the happy-path resolver, frontmatter
// parser, and body splitter compose correctly.
func TestLoadFixtureRoundTrip(t *testing.T) {
	// testdata is the canonical Go convention for test fixtures; resolve
	// projectDir as the directory that contains .claude/agents.
	projectDir := filepath.Join("testdata")

	got, err := Load(projectDir, "ta-go-builder")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.Name != "ta-go-builder" {
		t.Errorf("Name = %q, want %q", got.Name, "ta-go-builder")
	}
	if got.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-sonnet-4-5")
	}
	if !strings.Contains(got.Description, "Build Go code with TDD") {
		t.Errorf("Description missing expected prefix; got %q", got.Description)
	}

	// Spot-check tools parsing on real fixture content.
	if len(got.Tools) == 0 {
		t.Fatalf("Tools is empty; expected populated allowlist")
	}
	wantContains := []string{"Read", "Edit", "Write", "Grep", "Glob", "LSP"}
	for _, w := range wantContains {
		if !containsString(got.Tools, w) {
			t.Errorf("Tools missing %q; got %v", w, got.Tools)
		}
	}

	// Body must contain the opening sentence of the persona text and must
	// NOT contain the frontmatter delimiters.
	if !strings.Contains(got.Body, "You are the Go Builder Agent.") {
		t.Errorf("Body missing opening sentence; got %q", truncate(got.Body, 120))
	}
	if strings.Contains(got.Body, "---") {
		t.Errorf("Body unexpectedly contains frontmatter delimiter; got %q", truncate(got.Body, 120))
	}
}

// TestLoadToolsParsing covers the six enumerated tools-string subcases from
// the droplet acceptance criteria (added per L3 plan-QA falsification A6
// finding). Each case writes a synthetic persona file into a temp dir and
// exercises Load end-to-end so the comma-split-trim-skip-empty contract is
// verified through the public surface, not just an internal helper.
func TestLoadToolsParsing(t *testing.T) {
	tests := []struct {
		name      string
		toolsLine string // verbatim line inserted into the frontmatter; empty string omits the key entirely
		want      []string
	}{
		{
			name:      "multi-tool",
			toolsLine: "tools: Read, Grep, Glob",
			want:      []string{"Read", "Grep", "Glob"},
		},
		{
			name:      "single-tool",
			toolsLine: "tools: Read",
			want:      []string{"Read"},
		},
		{
			name:      "skip-empty-mid-list",
			toolsLine: "tools: Read,, Grep",
			want:      []string{"Read", "Grep"},
		},
		{
			name:      "all-empty-entries",
			toolsLine: "tools: , ,",
			want:      []string{},
		},
		{
			name:      "empty-value",
			toolsLine: "tools:",
			want:      []string{},
		},
		{
			name:      "omitted-key",
			toolsLine: "", // sentinel: no tools line in frontmatter
			want:      []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			role := "synthetic"
			writePersona(t, dir, role, tc.toolsLine, "body content\n")

			got, err := Load(dir, role)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			// Normalize nil vs empty so reflect.DeepEqual does not split
			// hairs on a contract the spec treats as equivalent.
			gotTools := got.Tools
			if gotTools == nil {
				gotTools = []string{}
			}

			if !reflect.DeepEqual(gotTools, tc.want) {
				t.Errorf("Tools = %#v, want %#v", gotTools, tc.want)
			}
		})
	}
}

// TestLoadCRLFAndTrailingWhitespace verifies the loader tolerates CRLF line
// endings and trailing whitespace on the `---` delimiter lines, per the
// acceptance criteria.
func TestLoadCRLFAndTrailingWhitespace(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "crlf-line-endings",
			content: "---\r\n" +
				"name: synthetic\r\n" +
				"description: crlf test\r\n" +
				"model: test-model\r\n" +
				"tools: Read, Grep\r\n" +
				"---\r\n" +
				"\r\n" +
				"Body text line one.\r\n" +
				"Body text line two.\r\n",
		},
		{
			name: "trailing-whitespace-on-delimiters",
			content: "---   \n" +
				"name: synthetic\n" +
				"description: trailing ws test\n" +
				"model: test-model\n" +
				"tools: Read, Grep\n" +
				"---\t\n" +
				"\n" +
				"Body text line one.\n" +
				"Body text line two.\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			role := "synthetic"
			path := filepath.Join(dir, ".claude", "agents", role+".md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			got, err := Load(dir, role)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			if got.Name != "synthetic" {
				t.Errorf("Name = %q, want %q", got.Name, "synthetic")
			}
			if got.Model != "test-model" {
				t.Errorf("Model = %q, want %q", got.Model, "test-model")
			}
			if !reflect.DeepEqual(got.Tools, []string{"Read", "Grep"}) {
				t.Errorf("Tools = %#v, want %#v", got.Tools, []string{"Read", "Grep"})
			}
			if !strings.Contains(got.Body, "Body text line one.") {
				t.Errorf("Body missing expected content; got %q", truncate(got.Body, 120))
			}
			if !strings.Contains(got.Body, "Body text line two.") {
				t.Errorf("Body missing expected content; got %q", truncate(got.Body, 120))
			}
			// Body must not retain CR characters; CRLF normalization runs
			// on the full payload before frontmatter splitting.
			if strings.Contains(got.Body, "\r") {
				t.Errorf("Body unexpectedly contains CR after CRLF normalization")
			}
		})
	}
}

// TestLoadBodyPreservation confirms that internal blank lines and trailing
// content in the body survive the round trip, while the loader does not
// emit a leading blank line that the caller would have to strip.
func TestLoadBodyPreservation(t *testing.T) {
	dir := t.TempDir()
	role := "synthetic"
	body := "First paragraph.\n" +
		"\n" +
		"Second paragraph after blank line.\n" +
		"\n" +
		"- bullet one\n" +
		"- bullet two\n"
	writePersona(t, dir, role, "tools: Read", body)

	got, err := Load(dir, role)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.Body != body {
		t.Errorf("Body round-trip mismatch.\ngot:  %q\nwant: %q", got.Body, body)
	}
}

// TestLoadErrors covers the four distinct error paths for Load: missing
// file, missing opening frontmatter delimiter, missing closing delimiter,
// and a non-blank frontmatter line that lacks a `:` separator. Each case
// uses errors.Is to assert against a sentinel so the contract survives
// error-message phrasing changes.
func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		// setup writes (or chooses not to write) the persona file under
		// dir/.claude/agents/<role>.md and returns the role name to load.
		setup func(t *testing.T, dir string) string
		// wantErr is the sentinel (or wrapped sentinel like os.ErrNotExist)
		// that errors.Is must report true against the returned error.
		wantErr error
	}{
		{
			name: "missing-file",
			setup: func(t *testing.T, dir string) string {
				// Intentionally do not write any file under dir; the
				// .claude/agents tree may or may not exist, both are
				// acceptable for an os.ErrNotExist surface.
				return "absent-role"
			},
			wantErr: os.ErrNotExist,
		},
		{
			name: "missing-opening-delimiter",
			setup: func(t *testing.T, dir string) string {
				role := "no-open"
				content := "name: no-open\n" +
					"description: missing opening delimiter\n" +
					"---\n" +
					"\n" +
					"Body content.\n"
				writeRawPersona(t, dir, role, content)
				return role
			},
			wantErr: ErrMissingOpenDelimiter,
		},
		{
			name: "missing-closing-delimiter",
			setup: func(t *testing.T, dir string) string {
				role := "no-close"
				content := "---\n" +
					"name: no-close\n" +
					"description: opening but no closing delimiter\n" +
					"model: test-model\n" +
					"tools: Read\n" +
					"\n" +
					"Body content that never closes the frontmatter.\n"
				writeRawPersona(t, dir, role, content)
				return role
			},
			wantErr: ErrMissingCloseDelimiter,
		},
		{
			name: "malformed-frontmatter-line",
			setup: func(t *testing.T, dir string) string {
				role := "malformed"
				content := "---\n" +
					"name: malformed\n" +
					"this-line-has-no-colon-separator\n" +
					"description: malformed line above\n" +
					"---\n" +
					"\n" +
					"Body content.\n"
				writeRawPersona(t, dir, role, content)
				return role
			},
			wantErr: ErrMalformedFrontmatterLine,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			role := tc.setup(t, dir)

			_, err := Load(dir, role)
			if err == nil {
				t.Fatalf("Load returned nil error; want errors.Is(err, %v)", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Load error = %v; want errors.Is(err, %v) to be true", err, tc.wantErr)
			}
		})
	}
}

// writeRawPersona writes verbatim content to dir/.claude/agents/<role>.md.
// Unlike writePersona, it does not synthesize frontmatter — the caller is
// responsible for every byte, which is what the error-path tests need.
func writeRawPersona(t *testing.T, dir, role, content string) {
	t.Helper()

	path := filepath.Join(dir, ".claude", "agents", role+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// writePersona writes a synthetic persona markdown file under
// dir/.claude/agents/<role>.md. If toolsLine is the empty string the
// `tools:` key is omitted entirely (covers the omitted-key subcase).
// Otherwise toolsLine is inserted verbatim as a frontmatter line.
func writePersona(t *testing.T, dir, role, toolsLine, body string) {
	t.Helper()

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + role + "\n")
	sb.WriteString("description: synthetic test persona\n")
	sb.WriteString("model: test-model\n")
	if toolsLine != "" {
		sb.WriteString(toolsLine + "\n")
	}
	sb.WriteString("---\n")
	sb.WriteString("\n")
	sb.WriteString(body)

	path := filepath.Join(dir, ".claude", "agents", role+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
