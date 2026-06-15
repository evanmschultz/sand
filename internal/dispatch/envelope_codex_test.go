package dispatch

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestParseCodexEnvelope exercises the canonical codex_stream parser cases
// enumerated in drop_005.drop.build_codex_envelope_parser acceptance:
//
//   - happy path with repeated `mcp: <server>/<tool> (completed)` lines
//     aggregating by the full server/tool token
//   - permission denials from both `(failed)` markers AND free-form
//     `permission_denial` lines
//   - narrative-only stream (no mcp lines) preserved verbatim in Result
//   - mixed stream interleaving tool events and narrative text
//   - empty input returning ErrEmptyEnvelope
//
// Fixtures live under testdata/codex-envelope-*.txt mirroring the
// claude-envelope-*.json convention so a future codex format drift can be
// reproduced by replacing a single fixture without touching the parser.
func TestParseCodexEnvelope(t *testing.T) {
	tests := []struct {
		name           string
		fixture        string
		wantErr        error
		wantTools      map[string]int
		wantDenials    map[string]int
		wantResultHas  []string // substrings that must appear in Result
		wantResultMiss []string // substrings that MUST NOT appear in Result
	}{
		{
			// Happy path: server/tool token preserved with `/` intact.
			// `ta/get` appears three times across the stream; aggregate
			// must reflect exactly three structured events. Narrative
			// lines ("Starting codex exec session.", "Working on the
			// task.", "Done. ...") must appear in Result but the mcp
			// lines themselves must NOT.
			name:    "happy-repeated-mcp-completed",
			fixture: "codex-envelope-happy.txt",
			wantTools: map[string]int{
				"ta/get":             3,
				"hylla/hylla_search": 1,
				"ta/update":          1,
			},
			wantDenials: map[string]int{},
			wantResultHas: []string{
				"Starting codex exec session.",
				"Working on the task.",
				"Done. The planner record has been amended.",
			},
			wantResultMiss: []string{
				"mcp: ta/get (completed)",
			},
		},
		{
			// Permission denials from BOTH markers:
			//   `mcp: shell/bash (failed)`  → denials["shell/bash"]++
			//   `permission_denial: Bash`   → denials["Bash"]++
			//   `permission_denial for Edit`→ denials["Edit"]++
			// Plus one successful ta/get to confirm classification
			// branches independently.
			name:    "permission-denials-mixed-markers",
			fixture: "codex-envelope-denials.txt",
			wantTools: map[string]int{
				"ta/get": 1,
			},
			wantDenials: map[string]int{
				"shell/bash": 1,
				"Bash":       1,
				"Edit":       1,
			},
			wantResultHas: []string{
				"Refused: out-of-allowlist Bash invocation.",
			},
			wantResultMiss: []string{
				"permission_denial",
				"(failed)",
			},
		},
		{
			// Narrative-only: no mcp lines at all. Tools/denials empty,
			// Result preserves every original line including the
			// narrative claim about `ta/get` (text-only claims must not
			// inflate aggregates — the codex equivalent of the claude
			// `narrative-lies` regression).
			name:        "narrative-only-no-mcp-lines",
			fixture:     "codex-envelope-narrative-only.txt",
			wantTools:   map[string]int{},
			wantDenials: map[string]int{},
			wantResultHas: []string{
				"The agent inspected the workspace",
				"ta/get and Bash were considered",
				"Done.",
			},
		},
		{
			// Mixed stream: interleaving tool events and narrative.
			// Aggregates must capture structured events; Result must
			// preserve narrative lines (in original order) but skip
			// mcp/permission_denial lines.
			name:    "mixed-stream-interleaved",
			fixture: "codex-envelope-mixed.txt",
			wantTools: map[string]int{
				"ta/get":             2,
				"ta/update":          1,
				"hylla/hylla_search": 1,
			},
			wantDenials: map[string]int{
				"shell/bash": 1,
				"Write":      1,
			},
			wantResultHas: []string{
				"narrative line one",
				"narrative line two",
				"Done. Summary of changes follows.",
			},
			wantResultMiss: []string{
				"mcp:",
				"permission_denial",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := readFixture(t, tc.fixture)

			got, err := ParseCodexEnvelope(stdout)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseCodexEnvelope err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
			}

			if !reflect.DeepEqual(got.ToolsUsed, tc.wantTools) {
				t.Errorf("ToolsUsed = %v, want %v", got.ToolsUsed, tc.wantTools)
			}
			if !reflect.DeepEqual(got.PermissionDenials, tc.wantDenials) {
				t.Errorf("PermissionDenials = %v, want %v", got.PermissionDenials, tc.wantDenials)
			}
			for _, sub := range tc.wantResultHas {
				if !strings.Contains(got.Result, sub) {
					t.Errorf("Result missing substring %q\nResult = %q", sub, got.Result)
				}
			}
			for _, sub := range tc.wantResultMiss {
				if strings.Contains(got.Result, sub) {
					t.Errorf("Result must NOT contain %q\nResult = %q", sub, got.Result)
				}
			}

			// ToolCalls is deferred to drop_007 per orchestrator
			// amendment B2 — codex_stream must NOT populate it today.
			// The Envelope struct doesn't surface ToolCalls directly;
			// that field lives on Response. This assertion is a
			// placeholder lock against accidental future drift if a
			// builder mistakenly adds an Envelope.ToolCalls slice in a
			// later drop without reading the amendment.
			//
			// Concretely: confirm the aggregates ARE populated (non-nil
			// maps) so downstream TOON rendering can rely on them.
			if got.ToolsUsed == nil {
				t.Errorf("ToolsUsed nil, want non-nil map (deferred-tool_calls amendment)")
			}
			if got.PermissionDenials == nil {
				t.Errorf("PermissionDenials nil, want non-nil map (deferred-tool_calls amendment)")
			}
		})
	}
}

// TestParseCodexEnvelopeEmptyInput covers the ErrEmptyEnvelope sentinel
// branch separately. Mirrors TestParseEnvelopeEmptyInput from the claude
// parser — sand reuses the same sentinel across both parsers so the
// dispatch layer can branch uniformly.
func TestParseCodexEnvelopeEmptyInput(t *testing.T) {
	got, err := ParseCodexEnvelope(nil)
	if !errors.Is(err, ErrEmptyEnvelope) {
		t.Fatalf("ParseCodexEnvelope(nil) err = %v, want ErrEmptyEnvelope", err)
	}
	if !reflect.DeepEqual(got, Envelope{}) {
		t.Fatalf("ParseCodexEnvelope(nil) returned non-zero Envelope: %+v", got)
	}

	got, err = ParseCodexEnvelope([]byte{})
	if !errors.Is(err, ErrEmptyEnvelope) {
		t.Fatalf("ParseCodexEnvelope(empty) err = %v, want ErrEmptyEnvelope", err)
	}
	if !reflect.DeepEqual(got, Envelope{}) {
		t.Fatalf("ParseCodexEnvelope(empty) returned non-zero Envelope: %+v", got)
	}
}

// TestParseCodexEnvelopeMissingTrailingNewline locks the bufio.Scanner
// contract: codex streams MAY end without a trailing `\n` (process exit
// flushes the buffer mid-line). The final mcp event must still be parsed.
// Regression for the EOF-handling acceptance bullet.
func TestParseCodexEnvelopeMissingTrailingNewline(t *testing.T) {
	stdout := []byte("mcp: ta/get (completed)\nmcp: ta/update (completed)")
	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
	}
	want := map[string]int{"ta/get": 1, "ta/update": 1}
	if !reflect.DeepEqual(got.ToolsUsed, want) {
		t.Errorf("ToolsUsed = %v, want %v", got.ToolsUsed, want)
	}
}

// TestParseCodexEnvelopePreservesTokenWithSlash locks the F4 deflection:
// the canonical codex tool identifier is the FULL post-`mcp: ` token
// including the `/`. ParseCodexEnvelope must NOT split on `/`.
func TestParseCodexEnvelopePreservesTokenWithSlash(t *testing.T) {
	stdout := []byte("mcp: hylla/hylla_search (completed)\nmcp: hylla/hylla_artifact_metadata (completed)\n")
	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
	}
	// Both keys must contain the literal `/` separator.
	for key := range got.ToolsUsed {
		if !strings.Contains(key, "/") {
			t.Errorf("ToolsUsed key %q missing `/` — parser split on separator", key)
		}
	}
	if got.ToolsUsed["hylla/hylla_search"] != 1 {
		t.Errorf("ToolsUsed[hylla/hylla_search] = %d, want 1", got.ToolsUsed["hylla/hylla_search"])
	}
	if got.ToolsUsed["hylla/hylla_artifact_metadata"] != 1 {
		t.Errorf("ToolsUsed[hylla/hylla_artifact_metadata] = %d, want 1", got.ToolsUsed["hylla/hylla_artifact_metadata"])
	}
}

// TestParseCodexEnvelope_OrderedToolCalls locks the drop_007a contract for
// the codex parser: each classified mcp `(completed)` appends an
// OrderedToolCall with IsError=false; each `(failed)` appends one with
// IsError=true; the 1-based Index advances across the combined sequence in
// the order the stream emitted the events. Aggregate behavior is locked by
// TestParseCodexEnvelope; this test asserts only the ordered slice.
func TestParseCodexEnvelope_OrderedToolCalls(t *testing.T) {
	stdout := []byte(
		"mcp: ta/get (completed)\n" +
			"mcp: shell/bash (failed)\n" +
			"mcp: ta/update (completed)\n",
	)

	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
	}

	want := []OrderedToolCall{
		{Index: 1, Name: "ta/get", IsError: false},
		{Index: 2, Name: "shell/bash", IsError: true},
		{Index: 3, Name: "ta/update", IsError: false},
	}
	if !reflect.DeepEqual(got.ToolCallsOrdered, want) {
		t.Fatalf("ToolCallsOrdered = %+v, want %+v", got.ToolCallsOrdered, want)
	}
}

// TestParseCodexEnvelopeMalformedMcpLine locks behavior for an `mcp:`
// prefix without a recognised `(completed)` / `(failed)` suffix. The line
// must NOT inflate aggregates and must be preserved in Result so an audit
// operator can see the unrecognised shape.
func TestParseCodexEnvelopeMalformedMcpLine(t *testing.T) {
	stdout := []byte("mcp: ta/get (in_progress)\nmcp: ta/get (completed)\nnarrative tail\n")
	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
	}
	if got.ToolsUsed["ta/get"] != 1 {
		t.Errorf("ToolsUsed[ta/get] = %d, want 1 (in_progress must not aggregate)", got.ToolsUsed["ta/get"])
	}
	if !strings.Contains(got.Result, "mcp: ta/get (in_progress)") {
		t.Errorf("Result missing unrecognised mcp line — operators lose audit signal\nResult = %q", got.Result)
	}
	if !strings.Contains(got.Result, "narrative tail") {
		t.Errorf("Result missing narrative tail\nResult = %q", got.Result)
	}
}

// TestParseCodexEnvelope_JSONStream exercises the new --json NDJSON stream format.
// Format auto-detection routes to parseCodexStream when input starts with `{"type":`.
func TestParseCodexEnvelope_JSONStream(t *testing.T) {
	tests := []struct {
		name           string
		fixture        string
		wantErr        error
		wantTools      map[string]int
		wantDenials    map[string]int
		wantResultHas  []string
		wantResultMiss []string
	}{
		{
			// Happy path: command_execution + mcp_tool_call + agent_message
			// extracted from item.completed events in the JSON stream.
			name:    "json-stream-happy-path",
			fixture: "codex-json-stream-schema.jsonl",
			wantTools: map[string]int{
				"echo hi": 1,
				"ta/get":  1,
			},
			wantDenials: map[string]int{},
			wantResultHas: []string{
				"Done. Ran echo hi and updated the record.",
			},
			wantResultMiss: []string{
				"item.started",
				"turn.started",
			},
		},
		{
			// Permission denials: command_execution with non-zero exit_code,
			// mcp_tool_call with error, and free-form error items.
			name:      "json-stream-failures",
			fixture:   "codex-json-stream-schema-denied.jsonl",
			wantTools: map[string]int{},
			wantDenials: map[string]int{
				"git commit -m x": 1,
				"ta/update":       1,
			},
			wantResultHas: []string{
				"I could not complete the write; the sandbox denied it.",
			},
		},
		{
			// Real capture: thread.started → error*6 → turn.failed
			// No tool calls, accumulated error messages in Result.
			name:        "json-stream-real-failure",
			fixture:     "codex-json-stream-lifecycle-real.jsonl",
			wantTools:   map[string]int{},
			wantDenials: map[string]int{},
			wantResultHas: []string{
				"turn.failed",
				"404 Not Found",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := readFixture(t, tc.fixture)

			got, err := ParseCodexEnvelope(stdout)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseCodexEnvelope err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
			}

			if !reflect.DeepEqual(got.ToolsUsed, tc.wantTools) {
				t.Errorf("ToolsUsed = %v, want %v", got.ToolsUsed, tc.wantTools)
			}
			if !reflect.DeepEqual(got.PermissionDenials, tc.wantDenials) {
				t.Errorf("PermissionDenials = %v, want %v", got.PermissionDenials, tc.wantDenials)
			}
			for _, sub := range tc.wantResultHas {
				if !strings.Contains(got.Result, sub) {
					t.Errorf("Result missing substring %q\nResult = %q", sub, got.Result)
				}
			}
			for _, sub := range tc.wantResultMiss {
				if strings.Contains(got.Result, sub) {
					t.Errorf("Result must NOT contain %q\nResult = %q", sub, got.Result)
				}
			}

			// Confirm aggregates are non-nil.
			if got.ToolsUsed == nil {
				t.Errorf("ToolsUsed nil, want non-nil map")
			}
			if got.PermissionDenials == nil {
				t.Errorf("PermissionDenials nil, want non-nil map")
			}
		})
	}
}

// TestParseCodexEnvelope_JSONStream_OrderedToolCalls validates the ordering
// guarantees for the JSON stream parser: tool calls are indexed 1-based in
// the order they appear in item.completed events.
func TestParseCodexEnvelope_JSONStream_OrderedToolCalls(t *testing.T) {
	stdout := readFixture(t, "codex-json-stream-schema.jsonl")
	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil", err)
	}

	if len(got.ToolCallsOrdered) != 2 {
		t.Fatalf("ToolCallsOrdered len = %d, want 2; got %+v", len(got.ToolCallsOrdered), got.ToolCallsOrdered)
	}

	// First: command_execution "echo hi" succeeds
	if got.ToolCallsOrdered[0].Index != 1 || got.ToolCallsOrdered[0].Name != "echo hi" || got.ToolCallsOrdered[0].IsError {
		t.Errorf("ToolCallsOrdered[0] = %+v, want Index=1 Name=echo hi IsError=false", got.ToolCallsOrdered[0])
	}

	// Second: mcp_tool_call ta/get succeeds
	if got.ToolCallsOrdered[1].Index != 2 || got.ToolCallsOrdered[1].Name != "ta/get" || got.ToolCallsOrdered[1].IsError {
		t.Errorf("ToolCallsOrdered[1] = %+v, want Index=2 Name=ta/get IsError=false", got.ToolCallsOrdered[1])
	}
}

// TestParseCodexEnvelope_JSONStream_MalformedJSON locks robustness:
// malformed JSON lines are accumulated as narrative and parsing continues.
func TestParseCodexEnvelope_JSONStream_MalformedJSON(t *testing.T) {
	stdout := []byte(
		`{"type":"thread.started","thread_id":"019ec948"}` + "\n" +
			`{"type":"turn.started"}` + "\n" +
			`{malformed json}` + "\n" +
			`{"type":"item.completed","item":{"type":"command_execution","command":"echo hi","exit_code":0,"status":"completed"}}` + "\n",
	)

	got, err := ParseCodexEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseCodexEnvelope err = %v, want nil (should tolerate malformed lines)", err)
	}

	if got.ToolsUsed["echo hi"] != 1 {
		t.Errorf("ToolsUsed[echo hi] = %d, want 1 (must recover after malformed line)", got.ToolsUsed["echo hi"])
	}

	if !strings.Contains(got.Result, "malformed json") {
		t.Errorf("Result missing malformed line for audit — Result = %q", got.Result)
	}
}
