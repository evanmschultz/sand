// Package backends — codex MCP server probe + inline-TOML rendering.
//
// This file owns the JSON-RPC probe seam that codex-exec uses to discover
// canonical, server-registered tool names from each MCP server declared in
// the caller's `.mcp.json`. The discovered names are then rendered into a
// codex `-c "mcp_servers.<name>={...}"` inline-TOML override per the
// canonical conversion recipe.
//
// Doc caveat per drop_005 L3 amendment A7: `~/.claude/codex-mcp-dispatch-
// tool-conversion.md` is the canonical conversion authority; tool-name
// discovery here is via JSON-RPC `tools/list` at dispatch time, NOT a
// hardcoded conversion table. Tests subprocess-stub the MCP server side
// rather than reach for that doc directly.
//
// MANDATORY orchestrator amendments folded into this file:
//
//   - A1 ctx.WithTimeout default 5s per server probe (DefaultProbeTimeout).
//   - A2 defer subprocess cleanup (cancel + Process.Kill + Wait) on every
//     error path. Stdin/stdout/stderr pipes explicitly closed.
//   - A3 stderr capture into bytes.Buffer; surfaced in SkipReason on
//     crash so operators see the FATAL diagnostic.
//   - A4 ALWAYS-QUOTE rule: every rendered tool key goes through
//     strconv.Quote, which produces TOML-basic-string-compatible escapes
//     for ASCII names (the only character class MCP tool names use in
//     practice; non-ASCII would still encode safely via \uXXXX escapes).
//   - A5 transport detection precedence: prefer stdio when both `command`
//     and `url` are populated; skip with a logged warning when neither /
//     url=="" alongside missing command / malformed args. HTTP probe is
//     explicitly out of scope.
//   - A6 tests use shell-script subprocess stubs under
//     internal/backends/testdata/fake-mcp-*/server.sh, mirroring the
//     fake-claude-* fixture pattern.
//   - A7 caveat above.
package backends

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultProbeTimeout is the per-server probe deadline applied by
// ProbeMCPServer when the caller-provided context has no deadline of its
// own. 5s is enough for a healthy MCP server to complete the three-step
// JSON-RPC handshake and short enough that a stuck server does not stall
// codex dispatch end-to-end. The value is unexported-knob territory for
// now; a BackendConfig field can override it in a later drop if
// production traffic shows the default is too tight.
const DefaultProbeTimeout = 5 * time.Second

// MCPServerEntry is the subset of a caller `.mcp.json` MCP-server entry
// that ProbeMCPServer needs. The shape mirrors Claude Code's stdio MCP
// schema (`command` + `args` + optional `env`) plus the HTTP-MCP fields
// (`url` + `headers`) so the transport-detection switch in ProbeMCPServer
// can inspect both. Fields the probe does not consume (e.g. `type`) are
// intentionally absent — callers decoding `.mcp.json` may map any
// superset they wish onto this struct.
type MCPServerEntry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ProbeResult is the per-server outcome returned by ProbeMCPServer.
//
// Field semantics:
//
//   - InlineTOML is the rendered `mcp_servers.<name>={...}` payload that
//     callers pair with a leading `-c ` codex flag. Empty when Skipped.
//   - ToolNames is the canonical, server-registered tool-name list as
//     returned by the JSON-RPC `tools/list` response. Empty when the
//     server registered no tools OR when Skipped.
//   - Skipped reports a non-fatal failure: the caller should log the
//     SkipReason + omit this server's `-c` flag but continue dispatching
//     the rest of the chain. Probe failures are NEVER hard errors at
//     this layer — they are diagnostic outputs.
//   - SkipReason carries the human-readable reason (timeout, crash,
//     malformed transport, ...) including any captured stderr.
type ProbeResult struct {
	InlineTOML string
	ToolNames  []string
	Skipped    bool
	SkipReason string
}

// ProbeMCPServer spawns the MCP server described by entry, performs the
// JSON-RPC three-step handshake (initialize → notifications/initialized →
// tools/list), parses the canonical tool-name list from the response,
// and renders the per-server inline-TOML payload codex consumes.
//
// Returned Go error is reserved for plumbing bugs (e.g. JSON marshal of
// the request fails — should never happen in practice). EVERY operational
// failure path — transport mismatch, subprocess start failure, handshake
// timeout, malformed JSON-RPC response, stderr crash — surfaces as a
// ProbeResult with Skipped=true + a populated SkipReason. The caller
// logs the reason and continues with the next server. This contract is
// codified by drop_005 L3 acceptance: "probe failures are non-fatal
// outputs, not hard dispatch errors."
//
// Context handling: if ctx already carries a deadline, ProbeMCPServer
// uses it as-is. Otherwise a 5s timeout is layered on top via
// context.WithTimeout (A1). The local cancel is always deferred so the
// subprocess gets reaped on every return path (A2).
//
// Transport detection precedence (A5):
//
//   - command non-empty + url non-empty → stdio wins; url ignored with a
//     captured warning in SkipReason only if probe fails downstream.
//   - command non-empty + url empty → stdio.
//   - command empty + url non-empty → skip (HTTP MCP probe out of
//     scope — codex upstream cannot pre-approve raw HTTP MCP servers
//     under --ephemeral).
//   - command empty + url empty → skip (malformed entry).
//   - command non-empty + args nil → stdio with empty args, legal (some
//     MCP servers self-configure entirely via env).
func ProbeMCPServer(ctx context.Context, name string, entry MCPServerEntry) (ProbeResult, error) {
	// A5: transport detection.
	if entry.Command == "" && entry.URL == "" {
		return ProbeResult{
			Skipped:    true,
			SkipReason: fmt.Sprintf("mcp server %q: malformed entry (neither command nor url set)", name),
		}, nil
	}
	if entry.Command == "" && entry.URL != "" {
		return ProbeResult{
			Skipped:    true,
			SkipReason: fmt.Sprintf("mcp server %q: HTTP url=%q not supported (codex requires stdio transport)", name, entry.URL),
		}, nil
	}
	// command non-empty: stdio path. url presence is ignored — A5 picks
	// stdio when both are populated. A diagnostic note rides along inside
	// SkipReason only on downstream failure.
	hadAmbiguousURL := entry.URL != ""

	// A1: layer a default timeout on top of any caller deadline.
	probeCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, DefaultProbeTimeout)
		defer cancel()
	}

	// A2: spawn under exec.CommandContext so ctx cancellation delivers
	// SIGKILL automatically. SysProcAttr.Setpgid=true puts the child in
	// its own process group so the deferred cleanup can kill the entire
	// group (the shell stub spawns `sleep N` as a child of the script
	// and we need to terminate both, not just the shell parent, or the
	// orphaned sleep will keep our cmd.Wait() blocked draining its open
	// stdout fd).
	cmd := exec.CommandContext(probeCtx, entry.Command, entry.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for k, v := range entry.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "stdin pipe", err, &bytes.Buffer{}, hadAmbiguousURL, entry.URL),
		}, nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "stdout pipe", err, &bytes.Buffer{}, hadAmbiguousURL, entry.URL),
		}, nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if startErr := cmd.Start(); startErr != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "start", startErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	// A2: guarantee subprocess reaping on every return path. Order:
	//
	//  1. Close stdin so the child sees EOF (well-behaved servers exit
	//     cleanly on EOF without needing the kill).
	//  2. SIGKILL the entire process group via the negative-PID
	//     convention so any grandchild (e.g. `sleep` spawned by the
	//     shell stub) dies with the parent — otherwise an orphaned
	//     grandchild's open stdout fd keeps cmd.Wait() blocked.
	//  3. Close stdout so any leaked reader goroutine unblocks
	//     immediately (its ReadBytes returns once the underlying fd
	//     closes).
	//  4. Wait to drain zombie state.
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			// Negative pid → process-group kill on unix.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
		_ = stdout.(io.Closer).Close()
		_ = cmd.Wait()
	}()

	// JSON-RPC handshake — three messages per MCP stdio transport spec.
	// Errors here are all non-fatal: classify as Skipped with diagnostic.
	if writeErr := writeJSONRPC(stdin, jsonRPCInitialize()); writeErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "write initialize", writeErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	reader := bufio.NewReader(stdout)
	// Increase buffer for large tools/list responses; default 64KB is fine
	// for the initialize response but a server with many tools can exceed
	// that. Switch to bufio.Reader.ReadString('\n') so we can grow without
	// scanner's ceiling.

	// Read initialize response (we don't need its content beyond
	// confirming a line came back — failure to read it means the server
	// crashed or never spoke the protocol).
	if _, readErr := readJSONRPCLine(probeCtx, reader); readErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "read initialize response", readErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	if writeErr := writeJSONRPC(stdin, jsonRPCInitialized()); writeErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "write initialized notification", writeErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	if writeErr := writeJSONRPC(stdin, jsonRPCToolsList()); writeErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "write tools/list", writeErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	toolsLine, readErr := readJSONRPCLine(probeCtx, reader)
	if readErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "read tools/list response", readErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	toolNames, parseErr := parseToolsListResponse(toolsLine)
	if parseErr != nil {
		return ProbeResult{
			Skipped:    true,
			SkipReason: probeFailureReason(name, "parse tools/list response", parseErr, &stderr, hadAmbiguousURL, entry.URL),
		}, nil
	}

	inline := RenderMCPInlineTOML(name, entry, toolNames)

	return ProbeResult{
		InlineTOML: inline,
		ToolNames:  toolNames,
	}, nil
}

// probeFailureReason composes a diagnostic SkipReason string covering
// stage + error + captured stderr + the A5 dual-transport warning. Kept
// in one place so every failure-path branch produces identically shaped
// diagnostics.
func probeFailureReason(name, stage string, err error, stderr *bytes.Buffer, hadAmbiguousURL bool, url string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mcp server %q: %s: %v", name, stage, err)
	if hadAmbiguousURL {
		fmt.Fprintf(&b, " (warning: entry also declared url=%q; stdio was preferred per A5)", url)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		b.WriteString(" (probe timed out)")
	}
	if stderrTxt := strings.TrimSpace(stderr.String()); stderrTxt != "" {
		fmt.Fprintf(&b, " | stderr: %s", stderrTxt)
	}
	return b.String()
}

// writeJSONRPC marshals one JSON-RPC message and writes it to w followed
// by a newline (MCP stdio transport is line-delimited JSON).
func writeJSONRPC(w io.Writer, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	payload = append(payload, '\n')
	if _, writeErr := w.Write(payload); writeErr != nil {
		return fmt.Errorf("write: %w", writeErr)
	}
	return nil
}

// readJSONRPCLine reads one line of JSON-RPC output, honouring ctx for
// cancellation. bufio.Reader has no native ctx integration, so we run the
// read in a goroutine and select on ctx.Done. The goroutine survives a
// context cancel — it will exit when the subprocess is reaped by the
// deferred cleanup chain in the caller.
func readJSONRPCLine(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	type lineResult struct {
		line []byte
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		ch <- lineResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil && len(res.line) == 0 {
			return nil, res.err
		}
		return res.line, nil
	}
}

// jsonRPCInitialize returns the initialize request payload per MCP spec.
// The MCP server should respond with a single line of JSON containing
// its server capabilities. We don't inspect the response — we only
// confirm a line came back so we know the server is alive.
func jsonRPCInitialize() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "sand-codex-mcp-probe",
				"version": "0.2",
			},
		},
	}
}

// jsonRPCInitialized returns the initialized notification per MCP spec.
// Notifications have no id field and expect no response.
func jsonRPCInitialized() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}
}

// jsonRPCToolsList returns the tools/list request payload. The response
// contains a `result.tools` array of objects each carrying a `name`
// field.
func jsonRPCToolsList() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
}

// parseToolsListResponse extracts the canonical tool-name list from the
// tools/list JSON-RPC response line. Returns an error when the line is
// not valid JSON, when the server reported a JSON-RPC error object, or
// when the expected `result.tools[].name` shape is absent.
func parseToolsListResponse(line []byte) ([]string, error) {
	type toolEntry struct {
		Name string `json:"name"`
	}
	type rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	type rpcResponse struct {
		Result *struct {
			Tools []toolEntry `json:"tools"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, errors.New("empty response line")
	}

	var resp rpcResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("server returned error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	if resp.Result == nil {
		return nil, errors.New("response missing result")
	}

	names := make([]string, 0, len(resp.Result.Tools))
	for _, t := range resp.Result.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// RenderMCPInlineTOML renders the per-server `mcp_servers.<name>={...}`
// payload codex consumes via `-c "<rendered>"` flags. The returned string
// is the VALUE portion (the `mcp_servers.<name>={...}` form); callers
// pair it with a leading `-c ` flag when assembling codex argv.
//
// A4 ALWAYS-QUOTE rule: every tool key is rendered through strconv.Quote
// so dotted (`hylla.search.vector`), hyphenated (`my-tool`), uppercase,
// numeric-starting, TOML-reserved, and even empty names all serialize as
// valid TOML basic-string keys. strconv.Quote uses the same escape set
// TOML basic strings define for ASCII (\\, \", \n, \r, \t, \uXXXX), so
// the rendered output round-trips cleanly through BurntSushi/toml.
//
// Output shape:
//
//	mcp_servers.<name>={command="<cmd>", args=["<arg1>", "<arg2>"], tools={"<tool1>"={approval_mode="approve"}, "<tool2>"={approval_mode="approve"}}}
//
// `args` is emitted as an empty array `[]` when entry.Args is empty/nil.
// `tools` is emitted as an empty inline table `{}` when toolNames is
// empty/nil — codex accepts both.
//
// The function never returns an error: every input shape produces a
// valid (if possibly empty-list) inline-TOML string. Validation belongs
// at the probe layer above.
func RenderMCPInlineTOML(name string, entry MCPServerEntry, toolNames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mcp_servers.%s={command=%s, args=[", name, strconv.Quote(entry.Command))
	for i, a := range entry.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(a))
	}
	b.WriteString("], tools={")
	for i, t := range toolNames {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s={approval_mode=\"approve\"}", strconv.Quote(t))
	}
	b.WriteString("}}")
	return b.String()
}
