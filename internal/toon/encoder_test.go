package toon

import (
	"strings"
	"testing"
)

// TestEncodeDispatchResponseGolden round-trips the SAND-SPEC §3.1 example
// (lines 92-113 of /Users/evanschultz/Documents/Code/hylla/sand/main/SAND-SPEC.md)
// end-to-end and asserts the encoder produces it byte-for-byte.
//
// This is the contract that motivates the encoder; sibling sand droplets
// build response structs that shape into this exact form.
func TestEncodeDispatchResponseGolden(t *testing.T) {
	input := Object{
		{Key: "result", Value: "The planner record has been amended and transitioned to complete+success."},
		{Key: "served_by", Value: "claude-native:opus"},
		{Key: "tier", Value: 3},
		{Key: "fallback", Value: true},
		{Key: "duration_ms", Value: 168793},
		{Key: "cost_usd", Value: 0.626},
		{Key: "tokens", Value: Object{
			{Key: "input", Value: 10},
			{Key: "output", Value: 13741},
			{Key: "cache_read", Value: 120482},
			{Key: "cache_creation", Value: 35481},
		}},
		{Key: "tools_used", Value: Tabular{
			Fields: []string{"name", "count"},
			Rows: [][]any{
				{"mcp__ta__get", 4},
				{"mcp__ta__update", 1},
				{"mcp__hylla__hylla_search_keyword", 6},
				{"Read", 8},
			},
		}},
		{Key: "permission_denials", Value: Tabular{
			Fields: []string{"tool", "count"},
			Rows:   [][]any{{"Bash", 0}},
		}},
		{Key: "log_path", Value: "/tmp/sand-dispatch/log/abc123.json"},
	}

	want := "" +
		"result: The planner record has been amended and transitioned to complete+success.\n" +
		"served_by: claude-native:opus\n" +
		"tier: 3\n" +
		"fallback: true\n" +
		"duration_ms: 168793\n" +
		"cost_usd: 0.626\n" +
		"tokens:\n" +
		"  input: 10\n" +
		"  output: 13741\n" +
		"  cache_read: 120482\n" +
		"  cache_creation: 35481\n" +
		"tools_used[4]{name,count}:\n" +
		"  mcp__ta__get,4\n" +
		"  mcp__ta__update,1\n" +
		"  mcp__hylla__hylla_search_keyword,6\n" +
		"  Read,8\n" +
		"permission_denials[1]{tool,count}:\n" +
		"  Bash,0\n" +
		"log_path: /tmp/sand-dispatch/log/abc123.json\n"

	got, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if string(got) != want {
		t.Errorf("encoded output mismatch.\n--- want ---\n%s\n--- got ---\n%s", want, string(got))
	}
}

// TestEncodeEmptyTabular verifies that empty tabular arrays still emit the
// `key[0]{f1,f2}:` length header with zero following rows, per SAND-SPEC
// §3.1 (line 117 note) and the TOON cheatsheet's "length is always
// required" rule.
func TestEncodeEmptyTabular(t *testing.T) {
	input := Object{
		{Key: "tools_used", Value: Tabular{
			Fields: []string{"name", "count"},
			Rows:   nil,
		}},
	}

	want := "tools_used[0]{name,count}:\n"

	got, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if string(got) != want {
		t.Errorf("empty-tabular output mismatch.\nwant: %q\n got: %q", want, string(got))
	}
}

// TestEncodeMultiLineResultBlockScalar verifies that a multi-line string
// value is auto-promoted to a `|` block scalar with content indented one
// level deeper than the parent key.
func TestEncodeMultiLineResultBlockScalar(t *testing.T) {
	input := Object{
		{Key: "result", Value: "line one\nline two\nline three"},
	}

	want := "" +
		"result: |\n" +
		"  line one\n" +
		"  line two\n" +
		"  line three\n"

	got, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if string(got) != want {
		t.Errorf("block-scalar output mismatch.\nwant: %q\n got: %q", want, string(got))
	}
}

// TestEncodeForcedBlockType verifies that the Block alias forces a block
// scalar even for single-line content (used when callers want the
// SAND-SPEC §3.3 `body: |` convention regardless of content shape).
func TestEncodeForcedBlockType(t *testing.T) {
	input := Object{
		{Key: "body", Value: Block("single line here")},
	}

	want := "body: |\n  single line here\n"

	got, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if string(got) != want {
		t.Errorf("forced-block output mismatch.\nwant: %q\n got: %q", want, string(got))
	}
}

// TestEncodeCSVQuoting exercises the CSV / scalar quoting rules: values
// containing the active delimiter (`,`), embedded quotes (`"`), or
// leading/trailing whitespace are quoted; embedded `"` is doubled; values
// without those characters pass through bare.
func TestEncodeCSVQuoting(t *testing.T) {
	tests := []struct {
		name  string
		input Object
		want  string
	}{
		{
			name: "comma in tabular cell triggers quoting",
			input: Object{
				{Key: "users", Value: Tabular{
					Fields: []string{"id", "name"},
					Rows:   [][]any{{1, "Bob, Smith"}},
				}},
			},
			want: "users[1]{id,name}:\n  1,\"Bob, Smith\"\n",
		},
		{
			name: "embedded quote is doubled in tabular cell",
			input: Object{
				{Key: "users", Value: Tabular{
					Fields: []string{"id", "note"},
					Rows:   [][]any{{1, `she said "hi"`}},
				}},
			},
			want: "users[1]{id,note}:\n  1,\"she said \"\"hi\"\"\"\n",
		},
		{
			name: "leading whitespace in value triggers quoting",
			input: Object{
				{Key: "message", Value: " padded "},
			},
			want: "message: \" padded \"\n",
		},
		{
			name: "empty string is quoted",
			input: Object{
				{Key: "note", Value: ""},
			},
			want: "note: \"\"\n",
		},
		{
			name: "bare colon in value does NOT trigger quoting (SAND-SPEC §3.1 golden form)",
			input: Object{
				{Key: "served_by", Value: "claude-native:opus"},
			},
			want: "served_by: claude-native:opus\n",
		},
		{
			name: "inline primitive array with one value needing quotes",
			input: Object{
				{Key: "tags", Value: Inline{"admin", "ops, dev"}},
			},
			want: "tags[2]: admin,\"ops, dev\"\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.input)
			if err != nil {
				t.Fatalf("Encode returned error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("output mismatch.\nwant: %q\n got: %q", tc.want, string(got))
			}
		})
	}
}

// TestEncodeTabularRowArityMismatch verifies that a row with a different
// number of values than declared in Fields produces a structured error
// rather than silently emitting a malformed row.
func TestEncodeTabularRowArityMismatch(t *testing.T) {
	input := Object{
		{Key: "users", Value: Tabular{
			Fields: []string{"id", "name", "role"},
			Rows:   [][]any{{1, "Alice"}},
		}},
	}

	_, err := Encode(input)
	if err == nil {
		t.Fatalf("expected error for row arity mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "row 0 has 2 values, want 3") {
		t.Errorf("error did not describe the arity mismatch: %v", err)
	}
}

// TestEncodeTopLevelTypeRejected verifies that passing a non-Object root
// fails fast — sand response shapes are always top-level objects per
// SAND-SPEC §3.1.
func TestEncodeTopLevelTypeRejected(t *testing.T) {
	_, err := Encode("not an object")
	if err == nil {
		t.Fatalf("expected error for non-Object root, got nil")
	}
	if !strings.Contains(err.Error(), "top-level value must be Object") {
		t.Errorf("error did not describe the type problem: %v", err)
	}
}
