package dispatch

// envelope_codex.go implements ParseCodexEnvelope: a line-oriented parser
// for codex exec stdout, mirroring the ParseEnvelope signature from
// envelope.go (the claude_json baseline).
//
// Codex emits a streaming text format rather than a single JSON envelope.
// Per SAND-V02-SPEC §7.3, each MCP tool invocation surfaces as one log line
// of the form:
//
//	mcp: <server>/<tool> (completed)
//	mcp: <server>/<tool> (failed)
//
// The token following `mcp: ` (server/tool joined by `/`) IS the canonical
// codex tool identifier — sand does NOT split on `/` so that downstream
// audit + TOON rendering preserve the codex-native shape. Lines lacking the
// `mcp: ` prefix are treated as narrative output and accumulated into the
// Envelope.Result field.
//
// Permission denials surface either as `(failed)` markers on an `mcp:` line
// OR as free-form lines containing the substring `permission_denial`. Both
// patterns are accepted because codex variants observed in the wild use
// either form.
//
// Per drop_005 orchestrator amendment B2, Response.ToolCalls (the ordered
// per-call breakdown) is DEFERRED to drop_007. ParseCodexEnvelope populates
// only the aggregate maps (ToolsUsed + PermissionDenials) to match the
// claude_json baseline today.

import (
	"bufio"
	"bytes"
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
// with structured aggregates populated.
//
// The function:
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
// ToolCalls (the ordered per-call breakdown described in SAND-V02-SPEC §4)
// is intentionally left empty: drop_005 orchestrator amendment B2 defers
// that field to drop_007 polish so the codex_stream parser matches
// claude_json baseline behavior today.
func ParseCodexEnvelope(stdout []byte) (Envelope, error) {
	if len(stdout) == 0 {
		return Envelope{}, ErrEmptyEnvelope
	}

	env := Envelope{
		ToolsUsed:         make(map[string]int),
		PermissionDenials: make(map[string]int),
	}

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), codexScannerBufSize)

	var narrative []string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t\r")

		if classified := classifyCodexLine(trimmed, env.ToolsUsed, env.PermissionDenials); classified {
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
func classifyCodexLine(line string, tools, denials map[string]int) bool {
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
			return true

		case strings.HasSuffix(body, " "+codexFailedSuffix):
			tool := strings.TrimSuffix(body, " "+codexFailedSuffix)
			tool = strings.TrimSpace(tool)
			if tool == "" {
				return true
			}
			denials[tool]++
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
