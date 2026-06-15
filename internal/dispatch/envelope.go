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
	"bufio"
	"bytes"
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
//
// ToolUseID carries the tool_use_id from stream-json tool_use events, used to
// correlate with tool_result.is_error. Empty for old JSON format or codex.
type OrderedToolCall struct {
	Index     int
	Name      string
	IsError   bool
	ToolUseID string
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

// EnvelopeDenial is one entry in the top-level permission_denials array
// emitted by `claude --output-format json` in the real result envelope.
// It is distinct from the Response-level PermissionDenial aggregate type
// (dispatch.go) which carries a count and no JSON tags.
// Extra JSON fields (tool_use_id, message, …) are silently ignored by
// encoding/json (tolerant decode per standard struct unmarshal rules).
type EnvelopeDenial struct {
	ToolName string `json:"tool_name"`
}

// streamJSONAssistantMessage represents the nested message structure in
// an assistant event of the stream-json format.
type streamJSONAssistantMessage struct {
	Content []streamJSONContentBlock `json:"content"`
}

// streamJSONContentBlock represents a single content block within an
// assistant or user message.
type streamJSONContentBlock struct {
	Type      string                 `json:"type"` // "tool_use", "text", "thinking", "tool_result"
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	IsError   bool                   `json:"is_error"`
	Content   interface{}            `json:"content,omitempty"`
}

// streamJSONEvent represents one NDJSON line from `claude --output-format stream-json`.
type streamJSONEvent struct {
	Type              string                      `json:"type"` // "system", "assistant", "user", "result"
	Subtype           string                      `json:"subtype,omitempty"`
	Message           *streamJSONAssistantMessage `json:"message,omitempty"`
	Result            string                      `json:"result,omitempty"`
	PermissionDenials []EnvelopeDenial            `json:"permission_denials,omitempty"`
	Usage             *Usage                      `json:"usage,omitempty"`
	SessionID         string                      `json:"session_id,omitempty"`
	TotalCostUSD      float64                     `json:"total_cost_usd,omitempty"`
	DurationMS        int                         `json:"duration_ms,omitempty"`
	NumTurns          int                         `json:"num_turns,omitempty"`
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

	// NumTurns is the number of agentic turns reported by the real claude
	// result envelope (top-level num_turns field). A value of 1 signals
	// likely no tool work; >1 signals real multi-turn execution. Decoded
	// directly from JSON; zero when absent.
	NumTurns int `json:"num_turns,omitempty"`

	// Usage carries the token-accounting block.
	Usage Usage `json:"usage"`

	// Iterations is the structured event stream from the envelope. Only
	// the typed fields on each Iteration are consulted by ParseEnvelope.
	Iterations []Iteration `json:"iterations,omitempty"`

	// PermissionDenialsRaw is the top-level permission_denials array from
	// the real claude result envelope, decoded directly from JSON. This is
	// the structured audit record of denials the CLI surfaces at the result
	// level; use len(PermissionDenialsRaw) for count and [i].ToolName for
	// the denied tool. Distinct from the iterations-derived PermissionDenials
	// map (which aggregates per-iteration denial events).
	PermissionDenialsRaw []EnvelopeDenial `json:"permission_denials,omitempty"`

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

// ParseEnvelope decodes the claude CLI's envelope from stdout and
// returns a typed Envelope with structured aggregates populated.
//
// This function auto-detects the format:
//   - If stdout starts with `[` or `{`, it is treated as JSON (old format).
//   - If stdout starts with `{"type":`, it is treated as NDJSON (stream-json format).
//   - Otherwise, returns ErrEmptyEnvelope or a decode error.
//
// The function:
//
//  1. Rejects empty input with ErrEmptyEnvelope so callers can distinguish a
//     missing CLI response from a malformed one.
//  2. Detects format and routes to the appropriate parser.
//  3. Walks events, aggregating tool_use events by Name into ToolsUsed and
//     permission_denial / permission_denied events by Tool into PermissionDenials.
//     Empty Name / Tool strings are skipped so a malformed event row cannot
//     poison the aggregate keys.
//
// Errors from json.Unmarshal are wrapped with %w so callers may inspect via
// errors.Is / errors.As. The returned Envelope is the zero value when err
// is non-nil.
func ParseEnvelope(stdout []byte) (Envelope, error) {
	if len(stdout) == 0 {
		return Envelope{}, ErrEmptyEnvelope
	}

	// Detect format: if it starts with `{"type":`, it is NDJSON stream-json.
	// Otherwise, assume old JSON format.
	trimmed := bytes.TrimSpace(stdout)
	if bytes.HasPrefix(trimmed, []byte(`{"type":`)) {
		return parseStreamJSON(trimmed)
	}

	// Fall back to old JSON format
	return parseJSON(trimmed)
}

// parseJSON decodes the old `--output-format json` format.
func parseJSON(stdout []byte) (Envelope, error) {
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

// parseStreamJSON decodes the `--output-format stream-json --verbose` NDJSON format.
// It reads line-by-line, collecting tool_use/tool_result pairs and extracting
// the final result text and permission denials from the result event.
func parseStreamJSON(stdout []byte) (Envelope, error) {
	env := Envelope{
		ToolsUsed:         make(map[string]int),
		PermissionDenials: make(map[string]int),
		ToolCallsOrdered:  make([]OrderedToolCall, 0),
	}

	// Map to correlate tool_use_id with tool_result.is_error
	toolResults := make(map[string]bool) // tool_use_id -> is_error

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var evt streamJSONEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			// Skip malformed lines; they may be incomplete or debug output
			continue
		}

		switch evt.Type {
		case "result":
			// Extract final result text, cost, duration, and permission denials
			env.Result = evt.Result
			if evt.SessionID != "" {
				env.SessionID = evt.SessionID
			}
			if evt.TotalCostUSD != 0 {
				env.TotalCostUSD = evt.TotalCostUSD
			}
			if evt.DurationMS != 0 {
				env.DurationMS = evt.DurationMS
			}
			if evt.NumTurns != 0 {
				env.NumTurns = evt.NumTurns
			}
			if evt.Usage != nil {
				env.Usage = *evt.Usage
			}
			// Copy permission_denials array from result event
			if len(evt.PermissionDenials) > 0 {
				env.PermissionDenialsRaw = evt.PermissionDenials
				for _, d := range evt.PermissionDenials {
					if d.ToolName == "" {
						continue
					}
					env.PermissionDenials[d.ToolName]++
				}
			}

		case "assistant":
			if evt.Message == nil || len(evt.Message.Content) == 0 {
				continue
			}
			// Extract tool_use blocks from assistant message content
			for _, block := range evt.Message.Content {
				if block.Type == "tool_use" && block.Name != "" {
					// Tool_use found; record it (order preserved)
					env.ToolCallsOrdered = append(env.ToolCallsOrdered, OrderedToolCall{
						Index:     len(env.ToolCallsOrdered) + 1,
						Name:      block.Name,
						IsError:   false,
						ToolUseID: block.ID,
					})
				}
			}

		case "user":
			if evt.Message == nil || len(evt.Message.Content) == 0 {
				continue
			}
			// Extract tool_result blocks from user message content
			for _, block := range evt.Message.Content {
				if block.Type == "tool_result" && block.ToolUseID != "" {
					// Record the result to pair with tool_use later
					toolResults[block.ToolUseID] = block.IsError
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return Envelope{}, fmt.Errorf("dispatch: scan stream-json: %w", err)
	}

	// Now that we've collected all tool_use and tool_result events,
	// walk the ordered tool calls to build the aggregate ToolsUsed map.
	// Correlate each tool_use with its tool_result via ToolUseID to check
	// is_error. A tool with is_error=true must NOT be counted as successful
	// in ToolsUsed, and its OrderedToolCall.IsError must be set true.
	for i, tc := range env.ToolCallsOrdered {
		// Check if this tool_use has a result with is_error=true
		if tc.ToolUseID != "" && toolResults[tc.ToolUseID] {
			// Mark as error and skip from ToolsUsed count
			env.ToolCallsOrdered[i].IsError = true
		} else if !tc.IsError {
			// No error result; count as successful tool_use
			env.ToolsUsed[tc.Name]++
		}
	}

	return env, nil
}
