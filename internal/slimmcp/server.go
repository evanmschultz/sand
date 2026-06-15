package slimmcp

import (
	"context"
	"encoding/json"
	"fmt"

	lagom "github.com/hylla-io/lagom/go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NewBrandedServer builds a branded mcp-go server that serves the slim
// projection of upstream defined by policyJSON, with lagom invisible.
//
// serverName is sand's OWN server identity (never "lagom"). policyJSON is the
// branded Policy as JSON — it carries the renamed tool names, the override
// descriptions, and which tools survive; lagom reads nothing itself. The Guard
// projects the slim surface once (failing loud here on drift/malformed input),
// each slim def is registered as an mcp-go tool with its slim schema verbatim,
// and each handler gates the incoming call through lagom before forwarding the
// rewritten upstream call to up.Run.
//
// A gate rejection (dropped tool, hidden original name, violated constraint) is
// surfaced to the agent as an mcp tool-error result carrying lagom's annotation
// — never swallowed (SPEC.md §9.1).
func NewBrandedServer(ctx context.Context, serverName string, up Upstream, policyJSON []byte) (*server.MCPServer, error) {
	guard, err := lagom.NewGuard(ctx, up.Defs, policyJSON)
	if err != nil {
		return nil, fmt.Errorf("slimmcp: build guard for %q: %w", serverName, err)
	}

	var defs []slimDef
	if err := json.Unmarshal(guard.SlimDefs(), &defs); err != nil {
		return nil, fmt.Errorf("slimmcp: decode slim defs for %q: %w", serverName, err)
	}

	srv := server.NewMCPServer(serverName, "0.1.0")
	for _, d := range defs {
		tool := mcp.Tool{Name: d.Name, RawInputSchema: d.InputSchema}
		if d.Description != nil {
			tool.Description = *d.Description
		}
		srv.AddTool(tool, gateHandler(guard, up))
	}
	return srv, nil
}

// gateHandler returns an mcp-go tool handler that gates one downstream call
// through the Guard and forwards the rewritten upstream call to up.Run. The
// branded (downstream) name and the agent-supplied args go in; lagom maps the
// name back, injects pins, fills defaults, and enforces constraints.
func gateHandler(guard *lagom.Guard, up Upstream) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Re-marshal the downstream call into the lagom-core ToolCall shape.
		downstream, err := json.Marshal(map[string]any{
			"name":      req.Params.Name,
			"arguments": req.GetArguments(),
		})
		if err != nil {
			return nil, fmt.Errorf("slimmcp: marshal downstream call: %w", err)
		}

		gated, err := guard.Gate(ctx, downstream)
		if err != nil {
			// lagom rejected the call (dropped/renamed-away tool, constraint
			// violation). Relay the annotation to the agent as a tool error
			// rather than swallowing it (SPEC.md §9.1).
			return mcp.NewToolResultError(err.Error()), nil
		}

		var call upstreamCall
		if err := json.Unmarshal(gated, &call); err != nil {
			return nil, fmt.Errorf("slimmcp: decode gated call: %w", err)
		}

		text, err := up.Run(ctx, call.Name, call.Arguments)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(text), nil
	}
}
