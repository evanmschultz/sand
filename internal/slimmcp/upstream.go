package slimmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	client "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// DialUpstream spawns a child MCP server using the provided spec and returns a
// wrapped Upstream with a closer function. The child process is configured as
// its own process group (on unix) so the entire tree can be killed at once via
// the closer. The closer Closes the client AND force-kills the process group,
// ensuring no resources leak. It is safe to call closer() even if the upstream
// child crashes or the dial partially fails.
func DialUpstream(ctx context.Context, spec UpstreamSpec) (*Upstream, func() error, error) {
	var childCmd *exec.Cmd

	// Create the MCP stdio client with a custom command function that captures
	// the child process and configures it as a process group leader (on unix).
	cli, err := client.NewStdioMCPClientWithOptions(
		spec.Command,
		spec.Env,
		spec.Args,
		transport.WithCommandFunc(func(cctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
			c := exec.CommandContext(cctx, command, args...)
			c.Env = env
			configureProcAttr(c)
			childCmd = c
			return c, nil
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
	}

	// Define the closer function so we can call it on errors below.
	closer := func() error {
		err := cli.Close()
		if childCmd != nil && childCmd.Process != nil {
			killProcessGroup(childCmd.Process.Pid)
		}
		return err
	}

	// Initialize the client with the MCP protocol.
	init := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "sand",
				Version: "0.1.0",
			},
		},
	}
	_, err = cli.Initialize(ctx, init)
	if err != nil {
		closer() //nolint:errcheck // cleanup is best-effort
		return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
	}

	// Retrieve the list of tools from the upstream.
	lr, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		closer() //nolint:errcheck // cleanup is best-effort
		return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
	}

	// Build an array of tool definitions in the camelCase MCP shape.
	toolArray := make([]map[string]any, 0, len(lr.Tools))
	for _, t := range lr.Tools {
		toolDef := map[string]any{
			"name":        t.Name,
			"description": t.Description,
		}
		// Use RawInputSchema if available; otherwise marshal the InputSchema.
		if len(t.RawInputSchema) > 0 {
			toolDef["inputSchema"] = t.RawInputSchema
		} else {
			schemaBytes, err := json.Marshal(t.InputSchema)
			if err != nil {
				closer() //nolint:errcheck // cleanup is best-effort
				return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
			}
			toolDef["inputSchema"] = json.RawMessage(schemaBytes)
		}
		toolArray = append(toolArray, toolDef)
	}

	// Marshal the tool array to JSON bytes for MapUpstreamDefs.
	toolJSON, err := json.Marshal(toolArray)
	if err != nil {
		closer() //nolint:errcheck // cleanup is best-effort
		return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
	}

	// Convert to lagom-core format (snake_case input_schema).
	defs, err := MapUpstreamDefs(toolJSON)
	if err != nil {
		closer() //nolint:errcheck // cleanup is best-effort
		return nil, nil, fmt.Errorf("slimmcp: dial upstream: %w", err)
	}

	// Build the Upstream wrapper with the Run handler.
	up := &Upstream{
		Defs: defs,
		Run: func(rctx context.Context, name string, args map[string]json.RawMessage) (string, error) {
			var req mcp.CallToolRequest
			req.Params.Name = name
			req.Params.Arguments = args
			res, err := cli.CallTool(rctx, req)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, c := range res.Content {
				if tc, ok := mcp.AsTextContent(c); ok {
					sb.WriteString(tc.Text)
				}
			}
			if res.IsError {
				return "", fmt.Errorf("slimmcp: upstream %q error: %s", name, sb.String())
			}
			return sb.String(), nil
		},
	}

	return up, closer, nil
}
