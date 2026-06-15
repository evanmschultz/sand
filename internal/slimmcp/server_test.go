package slimmcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// connect starts an mcp-go in-process client against srv and runs the MCP
// initialize handshake, returning a ready client. The whole exchange is
// in-process (no stdio, no network) — it exercises the real mcp-go protocol
// path an agent's harness would use.
func connect(t *testing.T, ctx context.Context, srv *server.MCPServer) *client.Client {
	t.Helper()
	cli, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("start client: %v", err)
	}
	var init mcp.InitializeRequest
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "agent", Version: "0.1.0"}
	if _, err := cli.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return cli
}

// listToolNames returns the agent-visible tool names, sorted.
func listToolNames(t *testing.T, ctx context.Context, cli *client.Client) []string {
	t.Helper()
	res, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// callText calls a tool that is expected to exist and returns its text result
// plus whether it was an error result. A gate rejection of an *existing* tool
// (constraint violation) rides back as an mcp tool-error result (IsError=true);
// a call to a tool the agent cannot see at all is a separate case.
func callText(t *testing.T, ctx context.Context, cli *client.Client, name string, args map[string]any) (string, bool) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := cli.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("call %q: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

// callRejected returns true if the call did not go through — either because the
// tool is not in the agent's surface at all (an mcp protocol "tool not found"
// error, e.g. a dropped tool) or because lagom gated it (an IsError result).
func callRejected(ctx context.Context, cli *client.Client, name string, args map[string]any) bool {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := cli.CallTool(ctx, req)
	if err != nil {
		return true
	}
	return res.IsError
}

// testSearchUpstream is a simple upstream with two tools: "search" and "secret".
// Both use the lagom-core snake_case input_schema form (the form that
// MapUpstreamDefs produces).
func testSearchUpstream(defs []byte) Upstream {
	run := func(_ context.Context, name string, args map[string]json.RawMessage) (string, error) {
		// Echo the resolved upstream call so a test can see exactly which
		// upstream tool ran and which args lagom injected.
		out, err := json.Marshal(map[string]any{"tool": name, "args": args})
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	return Upstream{Defs: defs, Run: run}
}

// TestBrandedServerHidesLagomAndEnforces drives the branded server (built from
// a policy over a test upstream) through the mcp-go in-process client and proves
// the consumption story end to end:
//
//   - tools/list shows the app's OWN names (e.g. "find"), never the upstream
//     names (e.g. "search") and never the word "lagom";
//   - the pinned arg is absent from the slim schemas (hidden);
//   - a valid call is gated: the upstream sees the pin injected and the upstream
//     name restored;
//   - a constraint violation (query failing a pattern) is rejected and annotated,
//     never forwarded.
func TestBrandedServerHidesLagomAndEnforces(t *testing.T) {
	ctx := context.Background()

	// Define upstream tool defs in MCP camelCase form (the form a real MCP
	// server probe would return).
	upstreamJSON := []byte(`[
		{
			"name": "search",
			"description": "Search an index for a query string.",
			"inputSchema": {
				"type": "object",
				"properties": {
					"index": {"type": "string"},
					"query": {"type": "string"}
				},
				"required": ["index", "query"]
			}
		},
		{
			"name": "secret",
			"description": "Fetch a secret by key.",
			"inputSchema": {
				"type": "object",
				"properties": {
					"key": {"type": "string"}
				},
				"required": ["key"]
			}
		}
	]`)

	// Run through MapUpstreamDefs to convert camelCase inputSchema to snake_case
	// input_schema — this proves the gotcha fix end-to-end.
	defs, err := MapUpstreamDefs(upstreamJSON)
	if err != nil {
		t.Fatalf("MapUpstreamDefs: %v", err)
	}

	// Build a policy: drop by default, keep "search" renamed to "find" with a
	// pinned index and a constrained query pattern. "secret" stays dropped.
	policy := []byte(`{
		"default_presence": "drop",
		"tools": {
			"search": {
				"presence": "keep",
				"rename": "find",
				"description": {"override": "Look up terms in the index."},
				"args": {
					"index": {"pin": "docs"},
					"query": {"constrain": {"pattern": "^[a-z0-9 ]+$"}}
				}
			}
		}
	}`)

	// Build the branded server.
	srv, err := NewBrandedServer(ctx, "search-app", testSearchUpstream(defs), policy)
	if err != nil {
		t.Fatalf("NewBrandedServer: %v", err)
	}

	cli := connect(t, ctx, srv)

	// Surface is branded and narrowed: only "find", no "search", no "secret".
	got := listToolNames(t, ctx, cli)
	want := []string{"find"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("branded tools = %v, want %v", got, want)
	}

	// No upstream name and no "lagom" leaks into the surface.
	res, _ := cli.ListTools(ctx, mcp.ListToolsRequest{})
	for _, tool := range res.Tools {
		blob, _ := json.Marshal(tool)
		low := strings.ToLower(string(blob))
		if strings.Contains(low, "lagom") {
			t.Errorf("tool %q surface leaks the word lagom: %s", tool.Name, blob)
		}
		if strings.Contains(low, "search") || strings.Contains(low, "secret") {
			t.Errorf("tool %q leaks an upstream name: %s", tool.Name, blob)
		}
		// The pinned `index` arg must not appear in the slim schema.
		if strings.Contains(string(tool.RawInputSchema), `"index"`) {
			t.Errorf("tool %q slim schema must not expose the pinned `index` arg: %s", tool.Name, tool.RawInputSchema)
		}
	}

	// Valid call: the upstream sees the injected pin + restored name.
	out, isErr := callText(t, ctx, cli, "find", map[string]any{"query": "lagom"})
	if isErr {
		t.Fatalf("valid find must not error: %s", out)
	}
	var ran struct {
		Tool string                     `json:"tool"`
		Args map[string]json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(out), &ran); err != nil {
		t.Fatalf("decode upstream echo: %v (raw %s)", err, out)
	}
	if ran.Tool != "search" {
		t.Errorf("gate must map branded find -> upstream search, got %q", ran.Tool)
	}
	if string(ran.Args["index"]) != `"docs"` {
		t.Errorf("gate must inject the pinned index, got %s", ran.Args["index"])
	}

	// Constraint violation: query outside the allowed pattern is rejected + annotated.
	out, isErr = callText(t, ctx, cli, "find", map[string]any{"query": "!invalid@#"})
	if !isErr {
		t.Fatalf("query outside the pattern must reject, got success: %s", out)
	}
	if out == "" {
		t.Error("rejection must carry an annotation, got empty text")
	}

	// Dropped tool ("secret") is not callable.
	if !callRejected(ctx, cli, "secret", map[string]any{"key": "api_key"}) {
		t.Error("secret (dropped tool) must not be callable")
	}
}

// TestTwoPoliciesTwoSurfaces proves the multi-agent story: TWO different policies
// applied to the SAME upstream produce TWO different slim surfaces and TWO different
// enforcement envelopes — a policy is data, not a compiled artifact.
func TestTwoPoliciesTwoSurfaces(t *testing.T) {
	ctx := context.Background()

	// Define upstream tool defs in MCP camelCase form.
	upstreamJSON := []byte(`[
		{
			"name": "search",
			"description": "Search an index for a query string.",
			"inputSchema": {
				"type": "object",
				"properties": {
					"index": {"type": "string"},
					"query": {"type": "string"}
				},
				"required": ["index", "query"]
			}
		},
		{
			"name": "secret",
			"description": "Fetch a secret by key.",
			"inputSchema": {
				"type": "object",
				"properties": {
					"key": {"type": "string"}
				},
				"required": ["key"]
			}
		}
	]`)

	// Convert to lagom form via MapUpstreamDefs.
	defs, err := MapUpstreamDefs(upstreamJSON)
	if err != nil {
		t.Fatalf("MapUpstreamDefs: %v", err)
	}

	// Policy 1: read-only agent, only "search" visible.
	policy1 := []byte(`{
		"default_presence": "drop",
		"tools": {
			"search": {
				"presence": "keep",
				"rename": "find",
				"description": {"override": "Look up terms in the docs."},
				"args": {
					"index": {"pin": "docs"}
				}
			}
		}
	}`)

	// Policy 2: admin agent, both "search" and "secret" visible with different pins.
	policy2 := []byte(`{
		"default_presence": "drop",
		"tools": {
			"search": {
				"presence": "keep",
				"rename": "find",
				"description": {"override": "Look up terms in the docs."},
				"args": {
					"index": {"pin": "docs"}
				}
			},
			"secret": {
				"presence": "keep",
				"rename": "get_secret",
				"description": {"override": "Retrieve a credential."},
				"args": {
					"key": {"pin": "admin_key"}
				}
			}
		}
	}`)

	// Build two servers from the same upstream with different policies.
	srv1, err := NewBrandedServer(ctx, "search-app", testSearchUpstream(defs), policy1)
	if err != nil {
		t.Fatalf("server 1: %v", err)
	}
	srv2, err := NewBrandedServer(ctx, "search-app", testSearchUpstream(defs), policy2)
	if err != nil {
		t.Fatalf("server 2: %v", err)
	}

	cli1 := connect(t, ctx, srv1)
	cli2 := connect(t, ctx, srv2)

	// Server 1: only "find" visible.
	got1 := listToolNames(t, ctx, cli1)
	if strings.Join(got1, ",") != "find" {
		t.Fatalf("server1 surface = %v, want [find]", got1)
	}

	// Server 2: "find" and "get_secret" visible.
	got2 := listToolNames(t, ctx, cli2)
	if strings.Join(got2, ",") != "find,get_secret" {
		t.Fatalf("server2 surface = %v, want [find get_secret]", got2)
	}

	// The two surfaces differ — the key multi-agent assertion.
	if strings.Join(got1, ",") == strings.Join(got2, ",") {
		t.Fatal("server1 and server2 surfaces must differ from the same binary")
	}

	// Server 2's "get_secret" schema must not expose the pinned "key".
	res2, _ := cli2.ListTools(ctx, mcp.ListToolsRequest{})
	for _, tool := range res2.Tools {
		if tool.Name == "get_secret" && strings.Contains(string(tool.RawInputSchema), `"key"`) {
			t.Errorf("server2 get_secret must hide the pinned `key`: %s", tool.RawInputSchema)
		}
	}

	// Server 1: a valid call sees the pinned index injected.
	out1, isErr1 := callText(t, ctx, cli1, "find", map[string]any{"query": "test"})
	if isErr1 {
		t.Fatalf("server1 find must succeed: %s", out1)
	}
	var ran1 struct {
		Tool string                     `json:"tool"`
		Args map[string]json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(out1), &ran1); err != nil {
		t.Fatalf("decode server1 echo: %v (%s)", err, out1)
	}
	if ran1.Tool != "search" {
		t.Errorf("server1 must map find->search, got %q", ran1.Tool)
	}
	if string(ran1.Args["index"]) != `"docs"` {
		t.Errorf("server1 must inject index=docs, got %s", ran1.Args["index"])
	}

	// Server 2: both pins are injected on a get_secret call.
	out2, isErr2 := callText(t, ctx, cli2, "get_secret", map[string]any{})
	if isErr2 {
		t.Fatalf("server2 get_secret must succeed: %s", out2)
	}
	var ran2 struct {
		Tool string                     `json:"tool"`
		Args map[string]json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(out2), &ran2); err != nil {
		t.Fatalf("decode server2 echo: %v (%s)", err, out2)
	}
	if ran2.Tool != "secret" {
		t.Errorf("server2 must map get_secret->secret, got %q", ran2.Tool)
	}
	if string(ran2.Args["key"]) != `"admin_key"` {
		t.Errorf("server2 must inject key=admin_key, got %s", ran2.Args["key"])
	}

	// Server 1 cannot reach "get_secret" at all.
	if !callRejected(ctx, cli1, "get_secret", map[string]any{}) {
		t.Error("server1 must not be able to call get_secret")
	}
}
