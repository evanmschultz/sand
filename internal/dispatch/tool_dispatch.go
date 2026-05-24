// Package dispatch — sand.dispatch MCP tool definition + handler.
//
// This file owns the wiring that exposes the typed dispatch entry point as
// an MCP tool per SAND-SPEC §3.1. It defines:
//
//   - NewDispatchTool: an mcp.Tool whose input schema matches §3.1
//     (role/prompt required; cwd/model_override/dry_run optional).
//   - DispatchHandler: the handler that decodes the MCP request into
//     Params, calls the package-level dispatch function variable, encodes
//     the typed Response as a TOON document, and returns it to the
//     orchestrator as mcp.NewToolResultText.
//
// To keep the handler unit-testable without spawning a real claude CLI, the
// underlying dispatch call goes through a package-level function variable
// dispatchFn. Production code wires dispatchFn to the real Dispatch entry
// point owned by a sibling droplet (drop_003.drop.droplet_dispatch_persona_chains_dryrun).
// Until that droplet's Dispatch function lands, dispatchFn defaults to a
// no-op stub returning zero-value Response so the MCP wiring compiles and
// the handler's structure can be exercised by tests.
//
// This file MUST NOT define or modify Params / Response / runClaudeNative /
// ParseEnvelope / ErrUnsupportedBackend — those are owned by sibling
// droplets in drop_003.
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/evanmschultz/sand/internal/toon"
	"github.com/mark3labs/mcp-go/mcp"
)

// dispatchFn is the indirection point the MCP handler uses to call into
// the typed dispatch entry point. Sibling droplet
// drop_003.drop.droplet_dispatch_persona_chains_dryrun owns the real
// Dispatch implementation; once that lands, dispatchFn should be reassigned
// to it (typically from cmd/sand/main.go wiring or this package's init).
//
// Wired to the real Dispatch function (provided by
// droplet_dispatch_persona_chains_dryrun in dispatch.go). Tests swap this via
// withDispatchFn to exercise the handler's TOON encoding without invoking a
// real backend.
var dispatchFn = Dispatch

// NewDispatchTool returns the mcp.Tool definition for sand.dispatch.
//
// Input schema per SAND-SPEC §3.1:
//
//	role           string  required
//	prompt         string  required
//	cwd            string  optional
//	model_override string  optional
//	dry_run        bool    optional (default false)
//
// The schema fields use mcp.WithString / mcp.WithBoolean and mcp.Required()
// per Context7 /mark3labs/mcp-go documentation. Descriptions are kept short;
// they appear in tool-routing UIs but the canonical contract is SAND-SPEC.
func NewDispatchTool() mcp.Tool {
	return mcp.NewTool(
		"dispatch",
		mcp.WithDescription("Dispatch a role-scoped agent prompt through the configured backend chain."),
		mcp.WithString(
			"role",
			mcp.Required(),
			mcp.Description("Persona role name (e.g. ta-go-builder); resolves <cwd>/.claude/agents/<role>.md and the chain entry."),
		),
		mcp.WithString(
			"prompt",
			mcp.Required(),
			mcp.Description("Task prompt forwarded to the spawned agent. Thin pointer prompts referencing ta records are preferred."),
		),
		mcp.WithString(
			"cwd",
			mcp.Description("Working directory for the dispatched agent. Defaults to the sand server's project dir when empty."),
		),
		mcp.WithString(
			"model_override",
			mcp.Description("Replaces tier-1 model only (e.g. opus). Empty preserves the chain's configured tier-1 model."),
		),
		mcp.WithBoolean(
			"dry_run",
			mcp.Description("When true, render the would-be command and skip the backend spawn entirely."),
		),
	)
}

// DispatchHandler is the mcp-go tool handler for sand.dispatch.
//
// Per Context7 /mark3labs/mcp-go handler convention:
//   - Validation / business errors return (mcp.NewToolResultError(msg), nil)
//     so the orchestrator sees IsError=true on the CallToolResult without
//     the MCP transport collapsing the call.
//   - Unexpected system errors may return (nil, err); this handler currently
//     surfaces dispatch errors as tool-result errors because they are
//     domain-level (backend selection / chain misconfiguration / spawn
//     failure) rather than protocol-level.
//
// Successful dispatches encode the typed Response as a TOON document per
// SAND-SPEC §3.1 / §4 and return it as text content.
func DispatchHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	role, err := req.RequireString("role")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	params := Params{
		Role:          role,
		Prompt:        prompt,
		CWD:           req.GetString("cwd", ""),
		ModelOverride: req.GetString("model_override", ""),
		DryRun:        req.GetBool("dry_run", false),
	}

	resp, err := dispatchFn(ctx, params)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("dispatch: %v", err)), nil
	}

	encoded, err := toon.Encode(responseToTOON(resp))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("dispatch: encode response: %v", err)), nil
	}

	return mcp.NewToolResultText(string(encoded)), nil
}

// responseToTOON builds the toon.Object that mirrors the SAND-SPEC §3.1 +
// SAND-SPEC §4 dispatch response layout. Field order matches the spec's
// golden example so encoder output stays byte-stable for orchestrator-side
// fixtures.
//
// Tokens is emitted as a nested object; tools_used / permission_denials /
// fallback_chain / tool_calls are emitted as tabular arrays whose length
// header is always present (the encoder writes `name[0]{...}:` when there are
// zero rows, per spec).
//
// Field order (SAND-SPEC §4):
//
//	result, served_by, tier, fallback, duration_ms, cost_usd, tokens,
//	fallback_chain[N], tools_used[N], permission_denials[N], tool_calls[N],
//	log_path.
func responseToTOON(r Response) toon.Object {
	return toon.Object{
		{Key: "result", Value: toon.Block(r.Result)},
		{Key: "served_by", Value: r.ServedBy},
		{Key: "tier", Value: r.Tier},
		{Key: "fallback", Value: r.Fallback},
		{Key: "duration_ms", Value: r.DurationMs},
		{Key: "cost_usd", Value: r.CostUSD},
		{Key: "tokens", Value: toon.Object{
			{Key: "input", Value: r.Tokens.Input},
			{Key: "output", Value: r.Tokens.Output},
			{Key: "cache_read", Value: r.Tokens.CacheRead},
			{Key: "cache_creation", Value: r.Tokens.CacheCreation},
		}},
		{Key: "fallback_chain", Value: fallbackChainTabular(r.FallbackChain)},
		{Key: "tools_used", Value: toolsUsedTabular(r.ToolsUsed)},
		{Key: "permission_denials", Value: permissionDenialsTabular(r.PermissionDenials)},
		{Key: "tool_calls", Value: toolCallsTabular(r.ToolCalls)},
		{Key: "log_path", Value: r.LogPath},
	}
}

// toolsUsedTabular converts a []ToolUse into the tabular array shape per
// SAND-SPEC §3.1 `tools_used[N]{name,count}:`.
func toolsUsedTabular(uses []ToolUse) toon.Tabular {
	rows := make([][]any, len(uses))
	for i, u := range uses {
		rows[i] = []any{u.Name, u.Count}
	}
	return toon.Tabular{Fields: []string{"name", "count"}, Rows: rows}
}

// permissionDenialsTabular converts []PermissionDenial into the tabular
// array shape per SAND-SPEC §3.1 `permission_denials[N]{tool,count}:`.
func permissionDenialsTabular(denials []PermissionDenial) toon.Tabular {
	rows := make([][]any, len(denials))
	for i, d := range denials {
		rows[i] = []any{d.Tool, d.Count}
	}
	return toon.Tabular{Fields: []string{"tool", "count"}, Rows: rows}
}

// fallbackChainTabular converts a []Attempt into the tabular array shape per
// SAND-SPEC §4 `fallback_chain[N]{tier,backend,model,attempted_at,outcome,reason}:`.
//
// Attempt.AttemptedAt is rendered as an RFC3339 timestamp (UTC). An empty
// FallbackChain still emits the `fallback_chain[0]{...}:` header — the
// encoder enforces the length-header-always rule.
func fallbackChainTabular(chain []Attempt) toon.Tabular {
	rows := make([][]any, len(chain))
	for i, a := range chain {
		rows[i] = []any{
			a.Tier,
			a.Backend,
			a.Model,
			a.AttemptedAt.UTC().Format(time.RFC3339),
			a.Outcome,
			a.Reason,
		}
	}
	return toon.Tabular{
		Fields: []string{"tier", "backend", "model", "attempted_at", "outcome", "reason"},
		Rows:   rows,
	}
}

// toolCallsTabular converts a []ToolCall into the tabular array shape per
// SAND-SPEC §4 `tool_calls[N]{idx,name,duration_ms,is_error}:`.
//
// The `idx` column is populated from ToolCall.Index — the 1-based ordering
// captured at envelope-parse time. An empty ToolCalls still emits the
// `tool_calls[0]{...}:` header.
func toolCallsTabular(calls []ToolCall) toon.Tabular {
	rows := make([][]any, len(calls))
	for i, c := range calls {
		rows[i] = []any{c.Index, c.Name, c.DurationMs, c.IsError}
	}
	return toon.Tabular{
		Fields: []string{"idx", "name", "duration_ms", "is_error"},
		Rows:   rows,
	}
}
