// Tests for the sand.dispatch MCP tool definition + handler.
//
// The handler is unit-tested via the swappable package-level dispatchFn
// variable so no real claude CLI is spawned. Test cases cover:
//
//   - missing required `role` returns IsError=true (validation surfaced via
//     NewToolResultError per mcp-go handler convention)
//   - missing required `prompt` returns IsError=true
//   - happy path: dispatchFn returns a populated Response; handler encodes
//     it as TOON and embeds known keys (served_by, tier) in the result text
//   - dry_run + cwd + model_override propagate from MCP args into the
//     Params struct dispatchFn receives
package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// withDispatchFn swaps the package-level dispatchFn for the duration of the
// test and restores it via t.Cleanup. Tests use this to assert against the
// Params dispatchFn receives without spawning any backend.
func withDispatchFn(t *testing.T, fn func(ctx context.Context, p Params) (Response, error)) {
	t.Helper()
	orig := dispatchFn
	dispatchFn = fn
	t.Cleanup(func() { dispatchFn = orig })
}

// callToolRequest builds a CallToolRequest with the given arguments map.
// Sand's MCP tests construct requests directly rather than going through
// the in-process client because the handler does not depend on transport
// behavior — only on the request's typed accessors.
func callToolRequest(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "dispatch",
			Arguments: args,
		},
	}
}

// resultText extracts the text content of a CallToolResult for assertion.
// Returns the empty string when no text content is present so callers can
// still meaningfully assert IsError behavior.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil {
		t.Fatalf("result is nil; expected a CallToolResult")
	}
	if len(r.Content) == 0 {
		return ""
	}
	tc, ok := mcp.AsTextContent(r.Content[0])
	if !ok {
		t.Fatalf("result content[0] is not text: %#v", r.Content[0])
	}
	return tc.Text
}

func TestNewDispatchToolSchema(t *testing.T) {
	t.Parallel()

	tool := NewDispatchTool()
	if tool.Name != "dispatch" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "dispatch")
	}

	// Required fields must appear in the InputSchema.Required slice; optional
	// fields must NOT appear there. This catches accidental reshuffles of
	// mcp.Required() across the WithString/WithBoolean calls.
	wantRequired := map[string]bool{"role": true, "prompt": true}
	gotRequired := make(map[string]bool, len(tool.InputSchema.Required))
	for _, name := range tool.InputSchema.Required {
		gotRequired[name] = true
	}
	for name := range wantRequired {
		if !gotRequired[name] {
			t.Errorf("InputSchema.Required missing %q; got %v", name, tool.InputSchema.Required)
		}
	}
	for _, opt := range []string{"cwd", "model_override", "dry_run"} {
		if gotRequired[opt] {
			t.Errorf("InputSchema.Required contains optional field %q; got %v", opt, tool.InputSchema.Required)
		}
	}

	// Every documented schema field must appear in InputSchema.Properties so
	// schema-driven clients see all the inputs sand actually consumes.
	for _, field := range []string{"role", "prompt", "cwd", "model_override", "dry_run"} {
		if _, ok := tool.InputSchema.Properties[field]; !ok {
			t.Errorf("InputSchema.Properties missing field %q", field)
		}
	}
}

func TestDispatchHandlerMissingRole(t *testing.T) {
	// No t.Parallel(): these tests mutate the package-level dispatchFn.
	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		t.Errorf("dispatchFn should not be called when role is missing; got Params=%+v", p)
		return Response{}, nil
	})

	req := callToolRequest(map[string]any{
		"prompt": "do the thing",
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil with IsError=true", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError=true result; got %#v", result)
	}
}

func TestDispatchHandlerMissingPrompt(t *testing.T) {
	// No t.Parallel(): mutates package-level dispatchFn.
	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		t.Errorf("dispatchFn should not be called when prompt is missing; got Params=%+v", p)
		return Response{}, nil
	})

	req := callToolRequest(map[string]any{
		"role": "ta-go-builder",
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil with IsError=true", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError=true result; got %#v", result)
	}
}

func TestDispatchHandlerHappyPath(t *testing.T) {
	// No t.Parallel(): mutates package-level dispatchFn.
	t1 := time.Date(2026, 5, 22, 1, 30, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 22, 1, 30, 10, 0, time.UTC)
	stubResp := Response{
		Result:     "ok",
		ServedBy:   "claude-native:opus",
		Tier:       2,
		Fallback:   true,
		DurationMs: 168793,
		CostUSD:    0.626,
		Tokens: Tokens{
			Input:         10,
			Output:        13741,
			CacheRead:     120482,
			CacheCreation: 35481,
		},
		ToolsUsed: []ToolUse{
			{Name: "mcp__ta__get", Count: 4},
			{Name: "Read", Count: 8},
		},
		PermissionDenials: []PermissionDenial{
			{Tool: "Bash", Count: 0},
		},
		FallbackChain: []Attempt{
			{
				Tier:        1,
				Backend:     "ollama-cloud",
				Model:       "qwen3-coder-cloud-235b",
				AttemptedAt: t1,
				Outcome:     "rate_limit",
				Reason:      "HTTP 429 from daemon",
			},
			{
				Tier:        2,
				Backend:     "claude-native",
				Model:       "opus",
				AttemptedAt: t2,
				Outcome:     "success",
			},
		},
		ToolCalls: []ToolCall{
			{Index: 1, Name: "Read", DurationMs: 12, IsError: false},
			{Index: 2, Name: "mcp__ta__get", DurationMs: 89, IsError: false},
			{Index: 3, Name: "Bash", DurationMs: 234, IsError: true},
		},
		LogPath: "/tmp/sand-dispatch/log/abc123.json",
	}

	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		if p.Role != "ta-go-builder" {
			t.Errorf("Params.Role = %q, want %q", p.Role, "ta-go-builder")
		}
		if p.Prompt != "do the thing" {
			t.Errorf("Params.Prompt = %q, want %q", p.Prompt, "do the thing")
		}
		return stubResp, nil
	})

	req := callToolRequest(map[string]any{
		"role":   "ta-go-builder",
		"prompt": "do the thing",
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil", err)
	}
	if result == nil {
		t.Fatalf("result is nil; expected populated CallToolResult")
	}
	if result.IsError {
		t.Fatalf("result.IsError = true; want false on happy path; text=%q", resultText(t, result))
	}

	text := resultText(t, result)
	// Spot-check that the TOON encoding embeds the SAND-SPEC §3.1 +
	// SAND-V02-SPEC §4 known keys. We deliberately do NOT pin the full
	// byte-for-byte output here; that is the encoder's responsibility
	// (covered by the toon package tests). What matters at this layer is
	// that the handler ran the Response through toon.Encode and surfaced
	// the result as text with the spec-pinned field set and order.
	for _, want := range []string{
		"served_by: claude-native:opus",
		"tier: 2",
		"fallback: true",
		"duration_ms: 168793",
		"fallback_chain[2]{tier,backend,model,attempted_at,outcome,reason}:",
		`1,ollama-cloud,qwen3-coder-cloud-235b,2026-05-22T01:30:00Z,rate_limit,HTTP 429 from daemon`,
		`2,claude-native,opus,2026-05-22T01:30:10Z,success,""`,
		"tools_used[2]{name,count}:",
		"permission_denials[1]{tool,count}:",
		"tool_calls[3]{idx,name,duration_ms,is_error}:",
		"1,Read,12,false",
		"2,mcp__ta__get,89,false",
		"3,Bash,234,true",
		"log_path: /tmp/sand-dispatch/log/abc123.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("TOON output missing %q; got:\n%s", want, text)
		}
	}

	// Pin field order per SAND-V02-SPEC §4: result, served_by, tier,
	// fallback, duration_ms, cost_usd, tokens, fallback_chain[N],
	// tools_used[N], permission_denials[N], tool_calls[N], log_path.
	wantOrder := []string{
		"served_by:",
		"tier:",
		"fallback:",
		"duration_ms:",
		"cost_usd:",
		"tokens:",
		"fallback_chain[",
		"tools_used[",
		"permission_denials[",
		"tool_calls[",
		"log_path:",
	}
	prev := 0
	prevKey := ""
	for _, key := range wantOrder {
		idx := strings.Index(text, key)
		if idx < 0 {
			t.Errorf("TOON output missing key %q; got:\n%s", key, text)
			continue
		}
		if idx < prev {
			t.Errorf("TOON field %q at offset %d appears BEFORE prior field %q at offset %d; want strict order per SAND-V02-SPEC §4. Got:\n%s",
				key, idx, prevKey, prev, text)
		}
		prev = idx
		prevKey = key
	}
}

// TestDispatchHandlerHappyPathEmptyAuditArrays pins the empty-array TOON shape
// per SAND-V02-SPEC §4: empty FallbackChain still emits the
// `fallback_chain[0]{...}:` header with no rows; empty ToolCalls still emits
// the `tool_calls[0]{...}:` header. This is the contract callers rely on for
// schema-stable parsing regardless of whether any tier failed or any tools
// were invoked.
func TestDispatchHandlerHappyPathEmptyAuditArrays(t *testing.T) {
	// No t.Parallel(): mutates package-level dispatchFn.
	t1 := time.Date(2026, 5, 22, 1, 30, 15, 0, time.UTC)
	stubResp := Response{
		Result:   "ok",
		ServedBy: "claude-native:haiku",
		Tier:     1,
		FallbackChain: []Attempt{
			{
				Tier:        1,
				Backend:     "claude-native",
				Model:       "haiku",
				AttemptedAt: t1,
				Outcome:     "success",
			},
		},
		// ToolCalls deliberately empty.
		LogPath: "/tmp/sand-dispatch/log/def456.json",
	}

	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		return stubResp, nil
	})

	req := callToolRequest(map[string]any{
		"role":   "ta-go-builder",
		"prompt": "do the thing",
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("expected non-error result; got %#v", result)
	}

	text := resultText(t, result)
	for _, want := range []string{
		"fallback_chain[1]{tier,backend,model,attempted_at,outcome,reason}:",
		`1,claude-native,haiku,2026-05-22T01:30:15Z,success,""`,
		"tool_calls[0]{idx,name,duration_ms,is_error}:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("TOON output missing %q; got:\n%s", want, text)
		}
	}
}

func TestDispatchHandlerOptionalParamsPassthrough(t *testing.T) {
	// No t.Parallel(): mutates package-level dispatchFn.
	var captured Params
	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		captured = p
		return Response{}, nil
	})

	req := callToolRequest(map[string]any{
		"role":           "ta-go-planning",
		"prompt":         "plan the thing",
		"cwd":            "/abs/path",
		"model_override": "haiku",
		"dry_run":        true,
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("expected non-error result; got %#v", result)
	}

	want := Params{
		Role:          "ta-go-planning",
		Prompt:        "plan the thing",
		CWD:           "/abs/path",
		ModelOverride: "haiku",
		DryRun:        true,
	}
	if captured != want {
		t.Errorf("dispatchFn received Params = %+v\nwant %+v", captured, want)
	}
}

func TestDispatchHandlerDispatchError(t *testing.T) {
	// No t.Parallel(): mutates package-level dispatchFn.
	withDispatchFn(t, func(ctx context.Context, p Params) (Response, error) {
		return Response{}, ErrUnsupportedBackend
	})

	req := callToolRequest(map[string]any{
		"role":   "ta-go-builder",
		"prompt": "do the thing",
	})

	result, err := DispatchHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("DispatchHandler returned Go error = %v; want nil with IsError=true", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError=true result on dispatch error; got %#v", result)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "unsupported backend") {
		t.Errorf("dispatch error text = %q; want it to contain %q", text, "unsupported backend")
	}
}
