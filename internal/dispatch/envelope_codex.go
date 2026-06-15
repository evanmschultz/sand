package dispatch

// envelope_codex.go implements ParseCodexEnvelope: a format-detecting parser
// for codex exec stdout, supporting both the legacy text-line format and the
// new --json NDJSON stream format.
//
// LEGACY TEXT-LINE FORMAT (old `codex exec` without --json):
//
// Per SAND-SPEC §7.3, each MCP tool invocation surfaces as one log line
// of the form:
//
//	mcp: <server>/<tool> (completed)
//	mcp: <server>/<tool> (failed)
//
// The token following `mcp: ` (server/tool joined by `/`) IS the canonical
// codex tool identifier. Lines lacking the `mcp: ` prefix are treated as
// narrative output and accumulated into the Envelope.Result field.
//
// Permission denials surface either as `(failed)` markers on an `mcp:` line
// OR as free-form lines containing the substring `permission_denial`. Both
// patterns are accepted because codex variants observed in the wild use
// either form.
//
// NEW --json NDJSON STREAM FORMAT (codex-cli v0.139.0+):
//
// When spawned with `codex exec --json`, codex emits NDJSON events on stdout.
// The stream contains ThreadEvent records (thread.started, turn.started,
// item.completed, turn.completed, turn.failed, error) and ThreadItem records
// of various types (command_execution, mcp_tool_call, file_change, agent_message).
//
// Tool calls are extracted from item.completed events:
// - command_execution: shell commands (tool_use if exit_code==0, denial if exit_code!=0)
// - mcp_tool_call: MCP tool invocations (tool_use if status=="completed", denial if status=="failed")
// - file_change: edits (accumulated but not tool-counted)
//
// drop_007a wires the ordered per-call breakdown: ParseCodexEnvelope now
// populates Envelope.ToolCallsOrdered alongside the aggregate maps, and
// dispatch.buildSuccessResponse copies that into Response.ToolCalls so the
// ordered audit complement reaches the TOON encoder. ToolsUsed +
// PermissionDenials remain authoritative for total counts; the ordered slice
// preserves the original event sequence.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// codexScannerBufSize sizes the bufio.Scanner buffer. Codex stream lines
// can include long mcp-server payload echoes (file dumps, large tool
// outputs); 1 MiB gives generous headroom over the default 64 KiB without
// blowing memory on pathological input.
const codexScannerBufSize = 1024 * 1024

// codexMCPPrefix is the line prefix codex emits for every MCP tool log
// line. The post-prefix token (everything up to the trailing `(state)`
// marker) is the canonical codex tool identifier.
const codexMCPPrefix = "mcp: "

// codexCompletedSuffix marks a successful MCP tool invocation.
const codexCompletedSuffix = "(completed)"

// codexFailedSuffix marks a failed MCP tool invocation. Sand treats this
// as a permission denial for tool-call audit purposes — `failed` in codex
// usually means the tool was rejected before execution rather than running
// and erroring.
const codexFailedSuffix = "(failed)"

// codexPermissionDenialMarker is the alternate free-form denial pattern.
// Some codex variants emit `permission_denial: <tool>` lines outside the
// `mcp:` prefix; sand accepts both shapes per the drop_005 acceptance.
const codexPermissionDenialMarker = "permission_denial"

// ParseCodexEnvelope decodes codex exec stdout and returns a typed Envelope
// with structured aggregates populated. The function auto-detects format:
// if stdout is NDJSON (starts with `{"type":`), it uses parseCodexStream;
// otherwise it uses the legacy line-scan parser.
//
// For the legacy text-line format, the function:
//
//  1. Rejects empty input with ErrEmptyEnvelope so callers can distinguish
//     a missing CLI response from a malformed one. The same sentinel is
//     reused across both parsers so the dispatch layer can branch
//     uniformly.
//
//  2. Scans stdout line-by-line. For each line:
//     - If it starts with `mcp: ` AND ends with `(completed)`, the
//     middle token populates ToolsUsed[token]++.
//     - If it starts with `mcp: ` AND ends with `(failed)`, OR the line
//     contains `permission_denial`, the affected tool token populates
//     PermissionDenials[token]++.
//     - Otherwise the line is appended (joined by `\n`) to Envelope.Result
//     as narrative text.
//
//  3. Returns the assembled Envelope. ToolsUsed and PermissionDenials are
//     always non-nil maps so downstream TOON emission can render empty
//     `tools_used[0]` / `permission_denials[0]` rows without nil checks.
//
// ToolCallsOrdered (the ordered per-call breakdown described in
// SAND-SPEC §4) is populated from the same scan: each `(completed)` mcp line
// appends an OrderedToolCall with IsError=false; each `(failed)` mcp line
// and each free-form `permission_denial` line appends one with IsError=true.
// The 1-based Index is assigned at emit time so it stays aligned with the
// operator-visible event sequence.
//
// For the new --json NDJSON format, the function routes to parseCodexStream
// which walks NDJSON events and extracts tool calls from item.completed records.
func ParseCodexEnvelope(stdout []byte) (Envelope, error) {
	if len(stdout) == 0 {
		return Envelope{}, ErrEmptyEnvelope
	}

	trimmed := bytes.TrimSpace(stdout)

	// Auto-detect NDJSON format: if it starts with `{"type":`, use the JSON stream parser.
	if bytes.HasPrefix(trimmed, []byte(`{"type":`)) {
		return parseCodexStream(trimmed)
	}

	// Fall back to legacy text-line format
	return parseCodexLegacy(trimmed)
}

// parseCodexLegacy handles the legacy text-line codex format (pre --json).
func parseCodexLegacy(stdout []byte) (Envelope, error) {
	env := Envelope{
		ToolsUsed:         make(map[string]int),
		PermissionDenials: make(map[string]int),
		ToolCallsOrdered:  make([]OrderedToolCall, 0),
	}

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), codexScannerBufSize)

	var narrative []string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t\r")

		if classified := classifyCodexLine(trimmed, env.ToolsUsed, env.PermissionDenials, &env.ToolCallsOrdered); classified {
			continue
		}
		narrative = append(narrative, line)
	}

	// bufio.Scanner.Err() surfaces I/O / buffer-overflow errors. Reading
	// from bytes.Reader makes I/O impossible; the only realistic error
	// path is a line longer than codexScannerBufSize. Treat that as a
	// soft failure: preserve what was parsed and continue. Returning an
	// error here would lose every prior aggregated event on a single
	// pathological line, which is the wrong tradeoff for an audit
	// parser. The behavior is documented + locked by test.
	_ = scanner.Err()

	env.Result = strings.Join(narrative, "\n")
	return env, nil
}

// classifyCodexLine inspects a single trimmed stream line, updates the
// supplied aggregate maps if the line matches a known marker, and returns
// true when the line was classified (so the caller skips narrative
// accumulation). Empty / whitespace-only lines are classified as
// non-narrative so they do not pollute Result with trailing blanks.
//
// drop_007a: the ordered slice pointer carries the preserved-order per-call
// breakdown. Every classified tool_use / permission_denial event appends a
// row whose Index is 1-based across the combined sequence (Index = len+1 at
// emit time). Empty / whitespace-only lines and unrecognised mcp suffixes do
// NOT bump the ordered sequence so the Index aligns with operator-visible
// audit events only.
func classifyCodexLine(line string, tools, denials map[string]int, ordered *[]OrderedToolCall) bool {
	if line == "" {
		return true
	}

	if strings.HasPrefix(line, codexMCPPrefix) {
		body := strings.TrimPrefix(line, codexMCPPrefix)

		switch {
		case strings.HasSuffix(body, " "+codexCompletedSuffix):
			tool := strings.TrimSuffix(body, " "+codexCompletedSuffix)
			tool = strings.TrimSpace(tool)
			if tool == "" {
				return true
			}
			tools[tool]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    tool,
				IsError: false,
			})
			return true

		case strings.HasSuffix(body, " "+codexFailedSuffix):
			tool := strings.TrimSuffix(body, " "+codexFailedSuffix)
			tool = strings.TrimSpace(tool)
			if tool == "" {
				return true
			}
			denials[tool]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    tool,
				IsError: true,
			})
			return true
		}
		// `mcp:` prefix without a recognised suffix → treat as narrative
		// so the audit operator can see the raw line in Result.
		return false
	}

	if strings.Contains(line, codexPermissionDenialMarker) {
		tool := extractDenialTool(line)
		if tool == "" {
			tool = codexPermissionDenialMarker
		}
		denials[tool]++
		*ordered = append(*ordered, OrderedToolCall{
			Index:   len(*ordered) + 1,
			Name:    tool,
			IsError: true,
		})
		return true
	}

	return false
}

// extractDenialTool pulls the tool name out of a free-form
// `permission_denial` line. Accepted shapes (best-effort, tolerant of
// codex variants):
//
//	permission_denial: <tool>
//	permission_denial <tool>
//	... permission_denial for <tool> ...
//
// Returns the empty string when no token can be isolated; the caller then
// falls back to the marker itself as the denials key so the event is not
// dropped silently.
func extractDenialTool(line string) string {
	idx := strings.Index(line, codexPermissionDenialMarker)
	if idx < 0 {
		return ""
	}
	tail := line[idx+len(codexPermissionDenialMarker):]
	tail = strings.TrimLeft(tail, " :\t")

	// Some shapes carry a `for ` lead-in (`permission_denial for Bash`).
	tail = strings.TrimPrefix(tail, "for ")

	// First whitespace-delimited token is the tool. Strip surrounding
	// punctuation that codex sometimes appends.
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], ".,;:()")
}

// parseCodexStream handles the new --json NDJSON stream format from codex-cli v0.139.0+.
//
// The function walks NDJSON events, extracts tool calls from item.completed records,
// and accumulates narrative from agent_message events.
//
// Tool calls are extracted by switching on item.type:
// - command_execution: tool_use when exit_code==0 and status=="completed"; denial when exit_code!=0 or status=="failed"
// - mcp_tool_call: tool_use when status=="completed" and no error; denial when status=="failed" or error is set
// - file_change: edits are recorded but not counted as tool uses
//
// Shell commands use the command string as the tool name; MCP calls use "server/tool" format.
//
// Run-fatal errors (top-level error event or turn.failed) are accumulated in the result
// as narrative context.
func parseCodexStream(stdout []byte) (Envelope, error) {
	env := Envelope{
		ToolsUsed:         make(map[string]int),
		PermissionDenials: make(map[string]int),
		ToolCallsOrdered:  make([]OrderedToolCall, 0),
	}

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), codexScannerBufSize)

	var narrative []string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event codexStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Malformed JSON: accumulate the line as narrative and continue.
			narrative = append(narrative, string(line))
			continue
		}

		// Process the event based on its type.
		switch event.Type {
		case "thread.started":
			// Start of stream; may capture session ID for future use.
			if event.ThreadID != "" {
				env.SessionID = event.ThreadID
			}

		case "turn.started":
			// Turn begins; no action needed.

		case "turn.completed":
			// Turn ended successfully; capture usage if present.
			if event.Usage != nil {
				env.Usage = Usage{
					InputTokens:         event.Usage.InputTokens,
					OutputTokens:        event.Usage.OutputTokens,
					CacheReadTokens:     event.Usage.CachedInputTokens,
					CacheCreationTokens: 0, // Not available in codex stream
				}
			}

		case "turn.failed":
			// Turn failed; accumulate error as narrative.
			if event.Error != nil && event.Error.Message != "" {
				narrative = append(narrative, "turn.failed: "+event.Error.Message)
			}

		case "error":
			// Stream-level error; accumulate as narrative.
			if event.Message != "" {
				narrative = append(narrative, "error: "+event.Message)
			}

		case "item.started":
			// Item starts; no action needed for tool tracking.

		case "item.completed":
			// Item completed; extract tool call data based on item type.
			if event.Item == nil {
				continue
			}

			classifyCodexStreamItem(event.Item, env.ToolsUsed, env.PermissionDenials, &env.ToolCallsOrdered)

			// If this is an agent_message, accumulate the text.
			if event.Item.Type == "agent_message" && event.Item.Text != "" {
				narrative = append(narrative, event.Item.Text)
			}

		case "item.updated":
			// Partial update; only terminal states matter for tool tracking, so skip.

		default:
			// Unknown event type; ignore.
		}
	}

	_ = scanner.Err()

	env.Result = strings.Join(narrative, "\n")
	return env, nil
}

// codexStreamEvent is one NDJSON line from the codex --json stream.
type codexStreamEvent struct {
	Type     string            `json:"type"`
	ThreadID string            `json:"thread_id,omitempty"`
	Item     *codexStreamItem  `json:"item,omitempty"`
	Error    *codexStreamError `json:"error,omitempty"`
	Message  string            `json:"message,omitempty"`
	Usage    *codexStreamUsage `json:"usage,omitempty"`
}

// codexStreamItem represents a ThreadItem in the codex stream.
type codexStreamItem struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"` // "command_execution", "mcp_tool_call", "file_change", "agent_message", etc.
	Status           string                 `json:"status"`
	Command          string                 `json:"command,omitempty"`
	ExitCode         int                    `json:"exit_code"`
	AggregatedOutput string                 `json:"aggregated_output,omitempty"`
	Server           string                 `json:"server,omitempty"`
	Tool             string                 `json:"tool,omitempty"`
	Arguments        map[string]interface{} `json:"arguments,omitempty"`
	Result           *codexStreamResult     `json:"result,omitempty"`
	ErrorObj         *codexStreamError      `json:"error,omitempty"`
	Changes          []codexStreamChange    `json:"changes,omitempty"`
	Text             string                 `json:"text,omitempty"`
}

// codexStreamResult is the successful result of an MCP tool call.
type codexStreamResult struct {
	Content []interface{} `json:"content,omitempty"`
}

// codexStreamError represents an error in the stream.
type codexStreamError struct {
	Message string `json:"message,omitempty"`
}

// codexStreamChange represents a file change in a file_change item.
type codexStreamChange struct {
	Path string `json:"path,omitempty"`
	Kind string `json:"kind,omitempty"` // "add", "delete", "update"
}

// codexStreamUsage captures token accounting from turn.completed.
type codexStreamUsage struct {
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens,omitempty"`
}

// classifyCodexStreamItem examines a completed item and updates the tool aggregates.
func classifyCodexStreamItem(item *codexStreamItem, tools, denials map[string]int, ordered *[]OrderedToolCall) {
	switch item.Type {
	case "command_execution":
		// Shell command: success if exit_code==0 AND status=="completed", else denial.
		if item.ExitCode == 0 && item.Status == "completed" {
			name := item.Command
			if name == "" {
				return
			}
			tools[name]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    name,
				IsError: false,
			})
		} else if item.ExitCode != 0 || item.Status == "failed" {
			// Command failed or was denied.
			name := item.Command
			if name == "" {
				name = "command_execution"
			}
			denials[name]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    name,
				IsError: true,
			})
		}

	case "mcp_tool_call":
		// MCP tool: success if status=="completed" AND no error, else denial.
		if item.Status == "completed" && item.ErrorObj == nil {
			name := item.Server + "/" + item.Tool
			if name == "/" || item.Server == "" || item.Tool == "" {
				return
			}
			tools[name]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    name,
				IsError: false,
			})
		} else if item.Status == "failed" || item.ErrorObj != nil {
			// Tool call failed or was denied.
			name := item.Server + "/" + item.Tool
			if name == "/" || item.Server == "" || item.Tool == "" {
				name = "mcp_tool_call"
			}
			denials[name]++
			*ordered = append(*ordered, OrderedToolCall{
				Index:   len(*ordered) + 1,
				Name:    name,
				IsError: true,
			})
		}

	case "file_change":
		// File edits: record but do not count as tool uses. No per-file accounting in tools_used.

	default:
		// Other item types (reasoning, web_search, todo_list, error, agent_message, etc.) do not count as tool uses.
	}
}
