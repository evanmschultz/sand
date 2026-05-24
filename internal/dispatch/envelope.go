// Package dispatch implements sand's per-backend agent dispatch wiring.
//
// This file defines the Envelope type and ParseEnvelope entry point that
// consume the JSON envelope emitted by `claude -p --output-format json` (and
// sibling stream-json aggregations that sand may wrap). Per SAND-SPEC §3.1
// the dispatch response surfaces two aggregate fields populated EXCLUSIVELY
// from structured event records inside the envelope:
//
//   - tools_used[N]{name,count}        — counts of tool_use events keyed by
//     the event's `name` field.
//   - permission_denials[N]{tool,count} — counts of permission denial events
//     keyed by the event's `tool` field.
//
// Agent narrative text in the envelope's `result` field is preserved verbatim
// but is NEVER scanned to produce these aggregates. The orchestrator memory
// note `feedback_always_verify_tool_calls` is explicit: tool counts come from
// parsed events, not the agent's self-report. ParseEnvelope keeps that
// boundary by reading only typed fields off Envelope.Iterations.
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrEmptyEnvelope is returned by ParseEnvelope when the stdout slice has
// zero length. Callers use errors.Is to distinguish missing CLI output from
// other malformed-JSON failures.
var ErrEmptyEnvelope = errors.New("dispatch: empty envelope")

// Usage captures the token-accounting block emitted by the claude CLI.
//
// Field names mirror the JSON keys the CLI uses (input_tokens / output_tokens
// / cache_read_input_tokens / cache_creation_input_tokens) and are surfaced
// into the TOON response under sand.dispatch's `tokens` block.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// OrderedToolCall is one entry in Envelope.ToolCallsOrdered: the
// preserved-order per-call breakdown of tool invocations a parser observed in
// the dispatched agent's event stream. Index is 1-based call order across
// BOTH successful tool_use and permission-denied events combined, so a
// downstream Response.ToolCalls renderer can reconstruct the exact sequence
// the agent attempted.
//
// IsError is true when the call was a permission denial (or, for the codex
// stream, a `(failed)` invocation). The aggregate ToolsUsed /
// PermissionDenials maps remain authoritative for total counts; this slice is
// the ordered audit complement.
//
// Per-call timing is intentionally NOT carried here — neither the claude
// envelope's Iteration record nor the codex stream's mcp log line surfaces a
// per-invocation duration today. Response.ToolCall.DurationMs is documented
// as zero pending an upstream emitter that publishes per-call timing.
type OrderedToolCall struct {
	Index   int
	Name    string
	IsError bool
}

// Iteration is one structured event in the envelope's iterations array.
//
// The canonical event types ParseEnvelope inspects are:
//   - "tool_use"           — populates ToolsUsed[Name]++.
//   - "permission_denial"  — populates PermissionDenials[Tool]++.
//   - "permission_denied"  — accepted as a synonym of "permission_denial"
//     to tolerate naming drift between claude CLI versions; both spellings
//     are documented here and exercised by table-driven tests.
//
// Any other Type value is preserved on the struct but ignored by the
// aggregation pass. The Name and Tool fields are kept distinct because the
// two event families key off different JSON keys per SAND-SPEC §3.1's
// example (`tools_used` keys by `name`, `permission_denials` keys by `tool`).
type Iteration struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	Tool string `json:"tool,omitempty"`
}

// Envelope is the typed shape ParseEnvelope decodes claude CLI stdout into.
//
// Result holds the agent's final text response (the `result` field of the
// claude -p --output-format=json envelope). SessionID, TotalCostUSD,
// DurationMS, and Usage propagate to the TOON response per SAND-SPEC §3.1.
// Iterations is the structured event stream consumed by the aggregation
// pass; ToolsUsed and PermissionDenials are derived maps materialized by
// ParseEnvelope and are NOT decoded from JSON directly.
//
// The exact claude CLI envelope schema is verified at the spawn droplet
// (drop_003.drop.build_claude_spawn_l4) against live CLI output; this
// parser is tolerant — missing fields decode to zero values and an absent
// iterations array yields empty aggregate maps without error.
type Envelope struct {
	// Result is the agent's final text response, preserved verbatim. This
	// field is NEVER scanned to produce tool-use or permission-denial
	// counts — those derive only from Iterations.
	Result string `json:"result"`

	// SessionID is the claude CLI's session identifier for the dispatch.
	SessionID string `json:"session_id,omitempty"`

	// TotalCostUSD is the total dollar cost reported by the CLI envelope.
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`

	// DurationMS is the wall-clock duration the CLI reports for the run.
	DurationMS int `json:"duration_ms,omitempty"`

	// Usage carries the token-accounting block.
	Usage Usage `json:"usage"`

	// Iterations is the structured event stream from the envelope. Only
	// the typed fields on each Iteration are consulted by ParseEnvelope.
	Iterations []Iteration `json:"iterations,omitempty"`

	// ToolsUsed is the aggregated count of tool_use events keyed by the
	// event's Name. Populated by ParseEnvelope; not decoded from JSON.
	ToolsUsed map[string]int `json:"-"`

	// PermissionDenials is the aggregated count of permission denial
	// events keyed by the event's Tool. Populated by ParseEnvelope; not
	// decoded from JSON.
	PermissionDenials map[string]int `json:"-"`

	// ToolCallsOrdered is the preserved-order per-call breakdown of tool
	// invocations the parser observed. drop_007a wires this from both
	// ParseEnvelope (Iteration walk) and ParseCodexEnvelope (mcp: line
	// scan). The aggregate ToolsUsed / PermissionDenials maps stay
	// authoritative for total counts; ToolCallsOrdered is the ordered
	// complement that lets Response.ToolCalls reconstruct the sequence.
	ToolCallsOrdered []OrderedToolCall `json:"-"`
}

// ParseEnvelope decodes the claude CLI's JSON envelope from stdout and
// returns a typed Envelope with structured aggregates populated.
//
// The function:
//
//  1. Rejects empty input with ErrEmptyEnvelope so callers can distinguish a
//     missing CLI response from a malformed one.
//  2. Decodes stdout strictly enough to surface JSON syntax errors but
//     tolerantly with respect to absent optional fields (missing
//     iterations / session_id / usage all decode to zero values).
//  3. Walks Envelope.Iterations exactly once, aggregating tool_use events
//     by Name into ToolsUsed and permission_denial / permission_denied
//     events by Tool into PermissionDenials. Empty Name / Tool strings are
//     skipped so a malformed event row cannot poison the aggregate keys.
//
// Errors from json.Unmarshal are wrapped with %w so callers may inspect via
// errors.Is / errors.As. The returned Envelope is the zero value when err
// is non-nil.
func ParseEnvelope(stdout []byte) (Envelope, error) {
	if len(stdout) == 0 {
		return Envelope{}, ErrEmptyEnvelope
	}

	var env Envelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		return Envelope{}, fmt.Errorf("dispatch: decode envelope: %w", err)
	}

	env.ToolsUsed = make(map[string]int)
	env.PermissionDenials = make(map[string]int)
	env.ToolCallsOrdered = make([]OrderedToolCall, 0, len(env.Iterations))

	for _, it := range env.Iterations {
		switch it.Type {
		case "tool_use":
			if it.Name == "" {
				continue
			}
			env.ToolsUsed[it.Name]++
			env.ToolCallsOrdered = append(env.ToolCallsOrdered, OrderedToolCall{
				Index:   len(env.ToolCallsOrdered) + 1,
				Name:    it.Name,
				IsError: false,
			})
		case "permission_denial", "permission_denied":
			if it.Tool == "" {
				continue
			}
			env.PermissionDenials[it.Tool]++
			env.ToolCallsOrdered = append(env.ToolCallsOrdered, OrderedToolCall{
				Index:   len(env.ToolCallsOrdered) + 1,
				Name:    it.Tool,
				IsError: true,
			})
		}
	}

	return env, nil
}
