// Package slimmcp is the pure data seam between sand's upstream MCP probe and
// the lagom engine. It maps upstream tool definitions (MCP camelCase JSON) to
// lagom-core tool definitions (snake_case JSON) and provides the shared types
// for the branded server.
package slimmcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Upstream is a stand-in for a real upstream MCP server: a set of full-fidelity
// tool defs plus a handler that runs each call. In production this is the wrapped
// child process; here it is the data seam sand uses to pass probed tools to lagom.
// lagom never authors the upstream — it only re-presents a projection of it.
type Upstream struct {
	// Defs is the full upstream tools/list as JSON (the lagom-core ToolDef array
	// shape: name, description, input_schema), exactly what an app would probe
	// from the real upstream and hand to lagom.
	Defs []byte
	// Run executes a fully-formed upstream call (post-gate: pins injected,
	// upstream name restored) and returns its text result.
	Run func(ctx context.Context, name string, args map[string]json.RawMessage) (string, error)
}

// upstreamCall is the lagom-core ToolCall shape Guard.Gate returns: the upstream
// tool name with the agent's args plus any pins lagom injected.
type upstreamCall struct {
	Name      string                     `json:"name"`
	Arguments map[string]json.RawMessage `json:"arguments"`
}

// slimDef is the subset of a projected tool def the branded server needs to
// register a tool: the branded name, the branded description, and the slim input
// schema (pinned args already pruned).
type slimDef struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// MapUpstreamDefs converts an array of MCP tool definitions (camelCase inputSchema)
// to a lagom-core ToolDef array (snake_case input_schema). It preserves tool order,
// omits the description field when absent or empty in the input, and passes the
// inputSchema raw bytes through verbatim. It returns a wrapped error on malformed
// JSON input.
func MapUpstreamDefs(upstreamToolsJSON []byte) ([]byte, error) {
	// Unmarshal as a slice of raw objects with flexible field names.
	var upstream []map[string]any
	if err := json.Unmarshal(upstreamToolsJSON, &upstream); err != nil {
		return nil, fmt.Errorf("slimmcp: unmarshal upstream tools: %w", err)
	}

	defs := make([]slimDef, 0, len(upstream))
	for _, tool := range upstream {
		name, ok := tool["name"].(string)
		if !ok {
			return nil, fmt.Errorf("slimmcp: missing or non-string name field")
		}

		// Extract description; omit if absent or empty.
		var desc *string
		if rawDesc, hasDesc := tool["description"]; hasDesc {
			if descStr, ok := rawDesc.(string); ok && descStr != "" {
				desc = &descStr
			}
		}

		// Extract inputSchema as raw bytes. We need to re-marshal it from the
		// parsed any to get the JSON bytes, since the schema is a raw object.
		var schema json.RawMessage
		if rawSchema, hasSchema := tool["inputSchema"]; hasSchema {
			schemaBytes, err := json.Marshal(rawSchema)
			if err != nil {
				return nil, fmt.Errorf("slimmcp: marshal inputSchema: %w", err)
			}
			schema = schemaBytes
		} else {
			// If inputSchema is missing, use empty object.
			schema = json.RawMessage("{}") //nolint:gocritic // false positive on []byte assignment
		}

		defs = append(defs, slimDef{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		})
	}

	result, err := json.Marshal(defs)
	if err != nil {
		return nil, fmt.Errorf("slimmcp: marshal lagom defs: %w", err)
	}
	return result, nil
}
