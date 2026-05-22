// Package debugtools defines the read-only MCP debug/introspection tools
// exposed by sand: persona_get (this file) plus future chains_list and
// preflight wrappers. These tools wrap committed domain packages
// (internal/persona, internal/chains, internal/preflight) and render their
// outputs as TOON per SAND-SPEC §3.2-§3.4.
//
// debugtools holds NO dispatch state and NO backend probing logic of its
// own; it composes domain packages and produces TOON strings for the MCP
// envelope. Per the caller-project rule (memory project_sand_mcp_per_project_configurable
// + project_sand_persona_loading), every tool accepts a projectDir parameter
// supplied by the sand server entrypoint — nothing here is hardcoded to a
// single project tree.
package debugtools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/evanmschultz/sand/internal/persona"
)

// PersonaGetTool constructs the sand.persona_get MCP tool and a handler
// closure bound to projectDir. The returned tool definition + handler are
// passed straight into server.AddTool by cmd/sand/main.go wiring.
//
// projectDir is the caller project root; the handler reads
// <projectDir>/.claude/agents/<role>.md via persona.Load, which means the
// fixture/runtime layout for personas is the project tree the sand server
// was launched against (the --project arg, mirroring ta's MCP convention).
//
// The tool name is sand.persona_get (per SAND-SPEC §3.3) and the only input
// is a required `role` string.
func PersonaGetTool(projectDir string) (mcp.Tool, server.ToolHandlerFunc) {
	tool := mcp.NewTool(
		"sand.persona_get",
		mcp.WithDescription("Read and parse a role persona file as TOON (SAND-SPEC §3.3). Wraps internal/persona.Load against the caller project tree."),
		mcp.WithString(
			"role",
			mcp.Required(),
			mcp.Description("Persona role name (basename of <projectDir>/.claude/agents/<role>.md without the .md suffix)."),
		),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		role, err := req.RequireString("role")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if strings.TrimSpace(role) == "" {
			return mcp.NewToolResultError("role must not be empty"), nil
		}

		p, err := persona.Load(projectDir, role)
		if err != nil {
			// File-not-found (the persona markdown doesn't exist for the
			// supplied role under projectDir) maps to a stable, user-facing
			// "role not found" error so callers can distinguish typos from
			// genuine malformed-frontmatter situations.
			if errors.Is(err, os.ErrNotExist) {
				return mcp.NewToolResultError(fmt.Sprintf("role not found: %s", role)), nil
			}
			// Any of the three frontmatter sentinel errors from internal/persona
			// surfaces as "persona malformed: <err>". errors.Is walks the
			// wrapped chain Load produces via fmt.Errorf with %w.
			if errors.Is(err, persona.ErrMissingOpenDelimiter) ||
				errors.Is(err, persona.ErrMissingCloseDelimiter) ||
				errors.Is(err, persona.ErrMalformedFrontmatterLine) {
				return mcp.NewToolResultError(fmt.Sprintf("persona malformed: %v", err)), nil
			}
			// Any other error (permission, I/O) bubbles up as a generic
			// tool-error result rather than a Go error so MCP callers see a
			// structured IsError=true response instead of a transport fault.
			return mcp.NewToolResultError(fmt.Sprintf("persona load failed: %v", err)), nil
		}

		return mcp.NewToolResultText(renderPersonaTOON(p)), nil
	}

	return tool, handler
}

// renderPersonaTOON encodes a Persona as the §3.3 TOON shape:
//
//	name: <v>
//	description: <v>
//	model: <v>
//	tools[N]: <csv>
//	body: |
//	  <line1>
//	  <line2>
//
// Hand-rolled inline because the project's internal/toon encoder is not yet
// in tree. The exact shape is locked by SAND-SPEC §3.3 and verified by the
// persona_get_test cases. When internal/toon lands, this helper can be
// swapped for a structured encode call without changing the public surface.
func renderPersonaTOON(p persona.Persona) string {
	var b strings.Builder

	fmt.Fprintf(&b, "name: %s\n", p.Name)
	fmt.Fprintf(&b, "description: %s\n", p.Description)
	fmt.Fprintf(&b, "model: %s\n", p.Model)

	// tools[N]: inline CSV primitive-array form. N is always declared, even
	// when zero, per SAND-SPEC §4.2 (tabular/primitive array length is
	// required). Empty Tools renders as `tools[0]: ` with an empty CSV.
	fmt.Fprintf(&b, "tools[%d]: %s\n", len(p.Tools), strings.Join(p.Tools, ","))

	// body: | block scalar. Each body line is emitted indented by two spaces
	// to mark it as part of the block. An empty body still emits the `body: |`
	// header so the shape is uniform across personas with and without bodies.
	b.WriteString("body: |\n")
	if p.Body != "" {
		for _, line := range strings.Split(p.Body, "\n") {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	return b.String()
}
