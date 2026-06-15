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
	"encoding/json"
	"fmt"
	"time"

	"github.com/evanmschultz/sand/internal/gate"
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
		mcp.WithObject(
			"gate",
			mcp.Description("Optional dispatch-time gate allowlist. JSON object with keys: edit (string array), writable_dirs (string array), bash_deny (string array), network (bool). Matches the gate.Allowlist contract (internal/gate/gate.go)."),
			mcp.Properties(map[string]any{
				"edit":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"writable_dirs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"bash_deny":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"network":       map[string]any{"type": "boolean"},
			}),
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

	var parsedGate *gate.Allowlist
	if gateRaw, ok := req.GetArguments()["gate"]; ok && gateRaw != nil {
		g, parseErr := parseGateArg(gateRaw)
		if parseErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("dispatch: gate: %v", parseErr)), nil
		}
		parsedGate = g
	}

	params := Params{
		Role:          role,
		Prompt:        prompt,
		CWD:           req.GetString("cwd", ""),
		ModelOverride: req.GetString("model_override", ""),
		DryRun:        req.GetBool("dry_run", false),
		Gate:          parsedGate,
	}

	resp, err := dispatchFn(ctx, params)
	if err != nil {
		// Surface ALL errors: the top-level error string alone hides WHY each
		// tier failed. Append the full Response as TOON so the fallback_chain
		// table (per-tier outcome + stderr summary in the `reason` column) is
		// always visible to the caller, not just on success. The encode is
		// best-effort — if it fails we still return the original error.
		detail := fmt.Sprintf("dispatch: %v", err)
		if encoded, encErr := toon.Encode(responseToTOON(resp)); encErr == nil {
			detail = detail + "\n\n" + string(encoded)
		}
		return mcp.NewToolResultError(detail), nil
	}

	encoded, err := toon.Encode(responseToTOON(resp))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("dispatch: encode response: %v", err)), nil
	}

	return mcp.NewToolResultText(string(encoded)), nil
}

// strictGateSchema is used to validate field types in a gate JSON object before
// passing to gate.ParseAllowlist. Standard json.Unmarshal will return an error
// if a field value does not match the declared Go type — e.g. a JSON string
// where a bool or []string is expected. ParseAllowlist silently ignores per-field
// decode errors (it uses json.RawMessage internally); this struct is the
// type-checking gate before we delegate to ParseAllowlist for the final value
// with EditPresent semantics.
type strictGateSchema struct {
	Edit         []string `json:"edit"`
	WritableDirs []string `json:"writable_dirs"`
	BashDeny     []string `json:"bash_deny"`
	Network      *bool    `json:"network"`
}

// parseGateArg validates and decodes the "gate" MCP argument into a
// *gate.Allowlist. It:
//
//  1. JSON-marshals the raw arg (any, expected map[string]any from mcp-go).
//  2. Strict-decodes into strictGateSchema — fails if any field has the wrong
//     type (e.g. "network":"false" or "edit":"/tmp/x").
//  3. On success, delegates to gate.ParseAllowlist for the canonical
//     *gate.Allowlist with EditPresent semantics.
//
// Returns an error on type mismatch, malformed JSON, or marshal failure.
// Returns (nil, nil) only when raw is nil (the caller handles the nil check
// before calling this function).
func parseGateArg(raw any) (*gate.Allowlist, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal gate arg: %w", err)
	}

	// Strict type-validation: fails on field-type mismatches before we
	// delegate to the more permissive gate.ParseAllowlist.
	var strict strictGateSchema
	if err := json.Unmarshal(b, &strict); err != nil {
		return nil, fmt.Errorf("gate field type mismatch: %w", err)
	}

	// Delegate to gate.ParseAllowlist for EditPresent semantics (tracks
	// whether the "edit" key was present at all, distinct from an empty slice).
	a, err := gate.ParseAllowlist(b)
	if err != nil {
		return nil, fmt.Errorf("parse gate allowlist: %w", err)
	}
	return a, nil
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
//	num_turns, tools_used_count, tool_calls_count, log_path.
func responseToTOON(r Response) toon.Object {
	toolsUsedCount := 0
	for _, tu := range r.ToolsUsed {
		toolsUsedCount += tu.Count
	}
	toolCallsCount := len(r.ToolCalls)

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
		{Key: "num_turns", Value: r.NumTurns},
		{Key: "tools_used_count", Value: toolsUsedCount},
		{Key: "tool_calls_count", Value: toolCallsCount},
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
