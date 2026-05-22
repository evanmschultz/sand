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
	stubResp := Response{
		Result:     "ok",
		ServedBy:   "claude-native:opus",
		Tier:       3,
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
	// Spot-check that the TOON encoding embeds the SAND-SPEC §3.1 known
	// keys. We deliberately do NOT pin the full byte-for-byte output here;
	// that is the encoder's responsibility (covered by the toon package
	// tests). What matters at this layer is that the handler ran the
	// Response through toon.Encode and surfaced the result as text.
	for _, want := range []string{
		"served_by: claude-native:opus",
		"tier: 3",
		"fallback: true",
		"duration_ms: 168793",
		"tools_used[2]{name,count}:",
		"permission_denials[1]{tool,count}:",
		"log_path: /tmp/sand-dispatch/log/abc123.json",
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
