package dispatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseEnvelope exercises the canonical envelope cases enumerated in the
// drop_003 build_claude_envelope_l4 acceptance criteria:
//
//   - happy path with repeated tool_use events aggregated by name
//   - permission denials aggregated by tool
//   - narrative text falsely claiming extra tools (must NOT inflate counts)
//   - malformed JSON (must return wrapped decode error, zero Envelope)
//   - empty / no-event envelope (must return zero counts, no error other
//     than the explicit empty-input sentinel)
//
// Fixtures are loaded from testdata/claude-envelope-*.json so a future
// schema-drift discovery against live claude CLI output can be reproduced by
// replacing a single fixture without touching the parser code.
func TestParseEnvelope(t *testing.T) {
	tests := []struct {
		name              string
		fixture           string
		wantErr           error  // when set, errors.Is must match; aggregates not inspected
		wantErrContains   string // when set, err.Error() must contain this substring
		wantResult        string
		wantSessionID     string
		wantTools         map[string]int
		wantDenials       map[string]int
		wantTotalCostUSD  float64
		wantDurationMS    int
		wantInputTokens   int
		wantOutputTokens  int
		wantIterationsLen int
	}{
		{
			// Happy path: result text, session/cost/duration/usage block,
			// and a mix of repeated tool_use events. mcp__ta__get appears
			// four times across the iterations array; the aggregate must
			// reflect that exact count from structured events alone.
			name:    "happy-repeated-tool-use",
			fixture: "claude-envelope-happy.json",
			wantResult: "The planner record has been amended and transitioned " +
				"to complete+success.",
			wantSessionID:     "sess-abc-123",
			wantTotalCostUSD:  0.626,
			wantDurationMS:    168793,
			wantInputTokens:   10,
			wantOutputTokens:  13741,
			wantIterationsLen: 6,
			wantTools: map[string]int{
				"mcp__ta__get":             4,
				"mcp__ta__update":          1,
				"mcp__hylla__hylla_search": 1,
			},
			wantDenials: map[string]int{},
		},
		{
			// Permission denials aggregated by tool name. The fixture
			// includes both spellings ("permission_denial" and the
			// observed-in-wild synonym "permission_denied") to lock in
			// the parser's accepted alias set documented on Iteration.
			name:              "permission-denials",
			fixture:           "claude-envelope-denials.json",
			wantResult:        "Refused: out-of-allowlist Bash invocation.",
			wantIterationsLen: 4,
			wantTools: map[string]int{
				"Read": 1,
			},
			wantDenials: map[string]int{
				"Bash": 2,
				"Edit": 1,
			},
		},
		{
			// Narrative attack: the result text falsely claims the agent
			// used Bash and Write tools. The structured iterations array
			// contains ONLY a single Read event. Aggregates must reflect
			// the structured truth, not the narrative claim. This is the
			// regression for memory feedback_always_verify_tool_calls and
			// the explicit attack vector from the L4 acceptance criteria.
			name:              "narrative-falsely-claims-extra-tools",
			fixture:           "claude-envelope-narrative-lies.json",
			wantResult:        "I used Bash to inspect git status and Write to update three files.",
			wantIterationsLen: 1,
			wantTools: map[string]int{
				"Read": 1,
			},
			wantDenials: map[string]int{},
		},
		{
			// Malformed JSON: parser must return an error wrapping the
			// stdlib json decode failure. The returned Envelope is the
			// zero value (no partial aggregates leak out).
			name:            "malformed-json",
			fixture:         "claude-envelope-malformed.json",
			wantErrContains: "decode envelope",
		},
		{
			// Empty envelope: valid JSON, no iterations, no result. The
			// parser must succeed and return empty (non-nil) aggregate
			// maps so downstream TOON emission can render tools_used[0]
			// and permission_denials[0] without nil checks.
			name:              "empty-no-events",
			fixture:           "claude-envelope-empty.json",
			wantIterationsLen: 0,
			wantTools:         map[string]int{},
			wantDenials:       map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := readFixture(t, tc.fixture)

			got, err := ParseEnvelope(stdout)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseEnvelope err = %v, want errors.Is %v", err, tc.wantErr)
				}
				if !reflect.DeepEqual(got, Envelope{}) {
					t.Fatalf("ParseEnvelope returned non-zero Envelope on error: %+v", got)
				}
				return
			}
			if tc.wantErrContains != "" {
				if err == nil {
					t.Fatalf("ParseEnvelope err = nil, want substring %q", tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("ParseEnvelope err = %q, want substring %q", err.Error(), tc.wantErrContains)
				}
				if !reflect.DeepEqual(got, Envelope{}) {
					t.Fatalf("ParseEnvelope returned non-zero Envelope on error: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEnvelope err = %v, want nil", err)
			}

			if got.Result != tc.wantResult {
				t.Errorf("Result = %q, want %q", got.Result, tc.wantResult)
			}
			if tc.wantSessionID != "" && got.SessionID != tc.wantSessionID {
				t.Errorf("SessionID = %q, want %q", got.SessionID, tc.wantSessionID)
			}
			if tc.wantTotalCostUSD != 0 && got.TotalCostUSD != tc.wantTotalCostUSD {
				t.Errorf("TotalCostUSD = %v, want %v", got.TotalCostUSD, tc.wantTotalCostUSD)
			}
			if tc.wantDurationMS != 0 && got.DurationMS != tc.wantDurationMS {
				t.Errorf("DurationMS = %d, want %d", got.DurationMS, tc.wantDurationMS)
			}
			if tc.wantInputTokens != 0 && got.Usage.InputTokens != tc.wantInputTokens {
				t.Errorf("Usage.InputTokens = %d, want %d", got.Usage.InputTokens, tc.wantInputTokens)
			}
			if tc.wantOutputTokens != 0 && got.Usage.OutputTokens != tc.wantOutputTokens {
				t.Errorf("Usage.OutputTokens = %d, want %d", got.Usage.OutputTokens, tc.wantOutputTokens)
			}
			if len(got.Iterations) != tc.wantIterationsLen {
				t.Errorf("len(Iterations) = %d, want %d", len(got.Iterations), tc.wantIterationsLen)
			}
			if !reflect.DeepEqual(got.ToolsUsed, tc.wantTools) {
				t.Errorf("ToolsUsed = %v, want %v", got.ToolsUsed, tc.wantTools)
			}
			if !reflect.DeepEqual(got.PermissionDenials, tc.wantDenials) {
				t.Errorf("PermissionDenials = %v, want %v", got.PermissionDenials, tc.wantDenials)
			}
		})
	}
}

// TestParseEnvelopeEmptyInput covers the ErrEmptyEnvelope sentinel branch
// separately because the canonical fixture loader pre-populates a JSON file
// and an empty []byte cannot be expressed as a fixture without an empty file
// (which is permitted, but kept distinct for readability of the assertion).
func TestParseEnvelopeEmptyInput(t *testing.T) {
	got, err := ParseEnvelope(nil)
	if !errors.Is(err, ErrEmptyEnvelope) {
		t.Fatalf("ParseEnvelope(nil) err = %v, want ErrEmptyEnvelope", err)
	}
	if !reflect.DeepEqual(got, Envelope{}) {
		t.Fatalf("ParseEnvelope(nil) returned non-zero Envelope: %+v", got)
	}

	got, err = ParseEnvelope([]byte{})
	if !errors.Is(err, ErrEmptyEnvelope) {
		t.Fatalf("ParseEnvelope(empty) err = %v, want ErrEmptyEnvelope", err)
	}
	if !reflect.DeepEqual(got, Envelope{}) {
		t.Fatalf("ParseEnvelope(empty) returned non-zero Envelope: %+v", got)
	}
}

// TestParseEnvelopeIgnoresMalformedEventRows confirms that iteration rows
// missing the required key field (Name for tool_use, Tool for permission
// denial) are skipped rather than poisoning aggregate maps with an empty
// string key. This is a robustness lock against partial CLI envelopes
// where an event was truncated mid-emission.
func TestParseEnvelopeIgnoresMalformedEventRows(t *testing.T) {
	payload := map[string]any{
		"result": "ok",
		"iterations": []map[string]any{
			{"type": "tool_use"},                      // missing name
			{"type": "tool_use", "name": ""},          // empty name
			{"type": "tool_use", "name": "Read"},      // valid
			{"type": "permission_denial"},             // missing tool
			{"type": "permission_denial", "tool": ""}, // empty tool
			{"type": "permission_denial", "tool": "Bash"},
		},
	}
	stdout, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := ParseEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseEnvelope err = %v, want nil", err)
	}
	wantTools := map[string]int{"Read": 1}
	wantDenials := map[string]int{"Bash": 1}
	if !reflect.DeepEqual(got.ToolsUsed, wantTools) {
		t.Errorf("ToolsUsed = %v, want %v", got.ToolsUsed, wantTools)
	}
	if !reflect.DeepEqual(got.PermissionDenials, wantDenials) {
		t.Errorf("PermissionDenials = %v, want %v", got.PermissionDenials, wantDenials)
	}
}

// TestParseEnvelope_OrderedToolCalls locks the drop_007a contract: the
// preserved-order per-call breakdown captures every classified tool_use AND
// permission_denial event in iteration order, with 1-based Index, the
// event's Name/Tool string, and an IsError flag distinguishing the two
// families. The existing aggregate maps continue to count totals; this test
// asserts only the ordered slice (the aggregate behavior is locked by
// TestParseEnvelope + TestParseEnvelopeIgnoresMalformedEventRows).
func TestParseEnvelope_OrderedToolCalls(t *testing.T) {
	payload := map[string]any{
		"result": "ordered breakdown",
		"iterations": []map[string]any{
			{"type": "tool_use", "name": "Read"},
			{"type": "tool_use", "name": "Edit"},
			{"type": "permission_denial", "tool": "Bash"},
			{"type": "tool_use", "name": "Read"},
		},
	}
	stdout, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := ParseEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseEnvelope err = %v, want nil", err)
	}

	want := []OrderedToolCall{
		{Index: 1, Name: "Read", IsError: false},
		{Index: 2, Name: "Edit", IsError: false},
		{Index: 3, Name: "Bash", IsError: true},
		{Index: 4, Name: "Read", IsError: false},
	}
	if !reflect.DeepEqual(got.ToolCallsOrdered, want) {
		t.Fatalf("ToolCallsOrdered = %+v, want %+v", got.ToolCallsOrdered, want)
	}
}

// TestParseEnvelopeRealShape verifies that the real claude --output-format json
// result envelope shape (captured in drop_015 W4 spike) is decoded correctly.
// The top-level num_turns and permission_denials fields (distinct from the
// iterations-derived aggregate maps) must be populated by ParseEnvelope via
// plain encoding/json unmarshal with no parser-logic changes required.
func TestParseEnvelopeRealShape(t *testing.T) {
	tests := []struct {
		name             string
		fixture          string
		wantNumTurns     int
		wantDenialsCount int
		wantDenialTool   string // first entry's ToolName
	}{
		{
			name:             "real-shape-num-turns-and-denials",
			fixture:          "claude-json-real-tool-trace.json",
			wantNumTurns:     5,
			wantDenialsCount: 1,
			wantDenialTool:   "Bash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := readFixture(t, tc.fixture)

			got, err := ParseEnvelope(stdout)
			if err != nil {
				t.Fatalf("ParseEnvelope err = %v, want nil", err)
			}

			if got.NumTurns != tc.wantNumTurns {
				t.Errorf("NumTurns = %d, want %d", got.NumTurns, tc.wantNumTurns)
			}
			if len(got.PermissionDenialsRaw) != tc.wantDenialsCount {
				t.Fatalf("len(PermissionDenialsRaw) = %d, want %d",
					len(got.PermissionDenialsRaw), tc.wantDenialsCount)
			}
			if got.PermissionDenialsRaw[0].ToolName != tc.wantDenialTool {
				t.Errorf("PermissionDenialsRaw[0].ToolName = %q, want %q",
					got.PermissionDenialsRaw[0].ToolName, tc.wantDenialTool)
			}
		})
	}
}

// TestParseStreamJSONSuccess verifies stream-json parsing against the real
// sample fixture (15 lines, tool_use → tool_result success path).
func TestParseStreamJSONSuccess(t *testing.T) {
	stdout := readFixture(t, "claude-stream-json-sample.jsonl")

	got, err := ParseEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseEnvelope err = %v, want nil", err)
	}

	// Verify result text is preserved
	if got.Result != "Done." {
		t.Errorf("Result = %q, want %q", got.Result, "Done.")
	}

	// Verify tools_used contains Bash (successful case)
	if count, ok := got.ToolsUsed["Bash"]; !ok || count != 1 {
		t.Errorf("ToolsUsed[Bash] = %d, want 1", count)
	}

	// Verify no permission denials in success case
	if len(got.PermissionDenials) != 0 {
		t.Errorf("PermissionDenials = %v, want empty", got.PermissionDenials)
	}

	// Verify tool call is recorded
	if len(got.ToolCallsOrdered) != 1 {
		t.Fatalf("len(ToolCallsOrdered) = %d, want 1", len(got.ToolCallsOrdered))
	}
	if got.ToolCallsOrdered[0].Name != "Bash" || got.ToolCallsOrdered[0].IsError {
		t.Errorf("ToolCallsOrdered[0] = %+v, want Name=Bash IsError=false",
			got.ToolCallsOrdered[0])
	}

	// Verify ToolUseID is captured (for correlation with tool_result)
	if got.ToolCallsOrdered[0].ToolUseID == "" {
		t.Errorf("ToolCallsOrdered[0].ToolUseID is empty, want non-empty")
	}
}

// TestParseStreamJSONDenial verifies stream-json parsing against the real
// denial fixture (17 lines, tool_use → tool_result is_error:true → result.permission_denials).
// HOLE 2 FIX: denied tool_uses must NOT be counted in ToolsUsed, must have
// IsError=true in ToolCallsOrdered, but must still appear in PermissionDenials.
func TestParseStreamJSONDenial(t *testing.T) {
	stdout := readFixture(t, "claude-stream-json-denied.jsonl")

	got, err := ParseEnvelope(stdout)
	if err != nil {
		t.Fatalf("ParseEnvelope err = %v, want nil", err)
	}

	// Verify result text is the denial message
	if got.Result != "The `curl` command requires your approval to run. Please allow the tool execution in the permission prompt that should appear on your screen." {
		t.Errorf("Result = %q, got unexpected value", got.Result)
	}

	// Verify permission_denials array is populated from result event
	if len(got.PermissionDenialsRaw) != 1 {
		t.Fatalf("len(PermissionDenialsRaw) = %d, want 1", len(got.PermissionDenialsRaw))
	}
	if got.PermissionDenialsRaw[0].ToolName != "Bash" {
		t.Errorf("PermissionDenialsRaw[0].ToolName = %q, want Bash",
			got.PermissionDenialsRaw[0].ToolName)
	}

	// Verify PermissionDenials map is populated from the array
	if count, ok := got.PermissionDenials["Bash"]; !ok || count != 1 {
		t.Errorf("PermissionDenials[Bash] = %d, want 1", count)
	}

	// HOLE 2 FIX ASSERTION: denied Bash must NOT be in ToolsUsed
	if count, ok := got.ToolsUsed["Bash"]; ok {
		t.Errorf("ToolsUsed[Bash] = %d, want absent (denied tool must not be in ToolsUsed)", count)
	}
	if len(got.ToolsUsed) != 0 {
		t.Errorf("ToolsUsed = %v, want empty map (all denied)", got.ToolsUsed)
	}

	// HOLE 2 FIX ASSERTION: ToolCallsOrdered must have 1 entry with IsError=true
	if len(got.ToolCallsOrdered) != 1 {
		t.Fatalf("len(ToolCallsOrdered) = %d, want 1", len(got.ToolCallsOrdered))
	}
	if got.ToolCallsOrdered[0].Name != "Bash" {
		t.Errorf("ToolCallsOrdered[0].Name = %q, want Bash", got.ToolCallsOrdered[0].Name)
	}
	if !got.ToolCallsOrdered[0].IsError {
		t.Errorf("ToolCallsOrdered[0].IsError = %v, want true (denied)", got.ToolCallsOrdered[0].IsError)
	}
}

// readFixture loads a JSON or NDJSON fixture from testdata/ and fails the test
// if the file is missing or unreadable.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}
