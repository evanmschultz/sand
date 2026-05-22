package debugtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestPersonaGetHappy verifies the sand.persona_get TOON output shape per
// SAND-SPEC §3.3 against a full-frontmatter, multi-line-body fixture.
//
// Fixtures are materialized into t.TempDir() under .claude/agents/ so the
// projectDir handed to PersonaGetTool matches persona.Load's join contract
// (`<projectDir>/.claude/agents/<role>.md`). This mirrors the acceptance
// criteria's "path INCLUDES .claude/agents/ subdir" rule while keeping the
// repo free of committed test artifacts.
func TestPersonaGetHappy(t *testing.T) {
	projectDir := t.TempDir()
	writeFixture(t, projectDir, "happy-role", happyPersonaBody)

	_, handler := PersonaGetTool(projectDir)
	res := callPersonaGet(t, handler, "happy-role")

	if res.IsError {
		t.Fatalf("expected IsError=false on happy path; got error result: %s", textOf(t, res))
	}

	out := textOf(t, res)

	wantLines := []string{
		"name: happy-role\n",
		"description: Fixture persona for sand.persona_get happy-path tests.\n",
		"model: claude-sonnet-4-5\n",
		"tools[4]: Read,Edit,Bash(mage testFunc *),mcp__ta__get\n",
		"body: |\n",
	}
	for _, w := range wantLines {
		if !strings.Contains(out, w) {
			t.Errorf("output missing expected line %q\nfull output:\n%s", w, out)
		}
	}

	// Body block must indent each persona-body line by two spaces (block
	// scalar convention per SAND-SPEC §4.3). Pick the opening sentence; the
	// rendered line should appear with the indent prefix.
	if !strings.Contains(out, "  You are the Happy Test Agent.\n") {
		t.Errorf("body line not indented per block-scalar convention\nfull output:\n%s", out)
	}

	// A line containing a literal colon inside the body must survive unchanged
	// inside the block scalar (no escaping required for `|` literal form).
	if !strings.Contains(out, "  Body content spans multiple lines and includes a colon: like so.\n") {
		t.Errorf("body line with embedded colon malformed\nfull output:\n%s", out)
	}
}

// TestPersonaGetRoleNotFound verifies that handing a nonexistent role to the
// handler produces an IsError=true result whose message starts with the
// stable "role not found:" prefix the acceptance criteria pins. The
// underlying error from persona.Load wraps os.ErrNotExist via %w, and the
// handler maps it via errors.Is.
func TestPersonaGetRoleNotFound(t *testing.T) {
	projectDir := t.TempDir()
	// Deliberately do NOT write a fixture for "missing-role".

	_, handler := PersonaGetTool(projectDir)
	res := callPersonaGet(t, handler, "missing-role")

	if !res.IsError {
		t.Fatalf("expected IsError=true on missing role; got success: %s", textOf(t, res))
	}

	msg := textOf(t, res)
	if !strings.Contains(msg, "role not found") {
		t.Errorf("error message missing 'role not found' prefix; got %q", msg)
	}
	if !strings.Contains(msg, "missing-role") {
		t.Errorf("error message missing role name 'missing-role'; got %q", msg)
	}
}

// TestPersonaGetMalformedFrontmatter verifies that a persona file with an
// open `---` but no close `---` (the simplest reachable malformed-frontmatter
// case) surfaces as IsError=true with a "persona malformed:" prefix. The
// handler maps any of the three internal/persona sentinel errors
// (ErrMissingOpenDelimiter / ErrMissingCloseDelimiter /
// ErrMalformedFrontmatterLine) into this single user-facing message.
func TestPersonaGetMalformedFrontmatter(t *testing.T) {
	projectDir := t.TempDir()
	writeFixture(t, projectDir, "malformed-role", malformedPersonaBody)

	_, handler := PersonaGetTool(projectDir)
	res := callPersonaGet(t, handler, "malformed-role")

	if !res.IsError {
		t.Fatalf("expected IsError=true on malformed frontmatter; got success: %s", textOf(t, res))
	}

	msg := textOf(t, res)
	if !strings.Contains(msg, "persona malformed") {
		t.Errorf("error message missing 'persona malformed' prefix; got %q", msg)
	}
}

// happyPersonaBody is the full markdown payload for the happy-path fixture.
// Four tools so the test pins tools[4]:..., body spans multiple lines and
// contains an embedded colon to verify the block-scalar emission survives
// YAML-significant characters per SAND-SPEC §4.3.
const happyPersonaBody = `---
name: happy-role
description: Fixture persona for sand.persona_get happy-path tests.
model: claude-sonnet-4-5
tools: Read, Edit, Bash(mage testFunc *), mcp__ta__get
---

You are the Happy Test Agent.

You exist to validate the TOON output shape of sand.persona_get.
Body content spans multiple lines and includes a colon: like so.
`

// malformedPersonaBody opens a frontmatter block but never closes it, which
// drives persona.Load to return ErrMissingCloseDelimiter.
const malformedPersonaBody = `---
name: malformed-role
description: Fixture persona missing the closing frontmatter delimiter.
model: claude-sonnet-4-5
tools: Read

This file deliberately omits the closing dashes so persona.Load returns
ErrMissingCloseDelimiter and debugtools maps it to "persona malformed".
`

// writeFixture writes content to <projectDir>/.claude/agents/<role>.md,
// creating the intermediate directories. This matches persona.Load's path
// join contract exactly — the .claude/agents/ subdirectory is required.
func writeFixture(t *testing.T, projectDir, role, content string) {
	t.Helper()
	dir := filepath.Join(projectDir, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, role+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

// callPersonaGet invokes the handler with a synthetic CallToolRequest whose
// only argument is `role`. Returning the result lets each test assert on
// IsError and content independently.
func callPersonaGet(t *testing.T, handler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error), role string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "sand.persona_get"
	req.Params.Arguments = map[string]any{"role": role}

	res, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if res == nil {
		t.Fatalf("handler returned nil result with nil error")
	}
	return res
}

// textOf returns the concatenated text content of the result. Tests use this
// to inspect either the rendered TOON (success) or the error message
// (IsError=true) — both flow through mcp.Content text entries.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
