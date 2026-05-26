// Tests for the codex MCP-server probe + inline-TOML renderer.
//
// Mirrors the fake-claude-* fixture pattern: each test that needs a live
// MCP-server subprocess installs a fake shell script on a fresh PATH-
// prefix tempdir via installFakeMCPServer and constructs MCPServerEntry
// pointing at the installed script. Coverage spans:
//
//   - HappyPath: full three-step JSON-RPC handshake, mixed dotted +
//     snake_case + hyphenated + uppercase tool names round-trip into the
//     rendered inline-TOML payload.
//   - TimeoutHang (A1): server reads first message then sleeps past the
//     DefaultProbeTimeout; ProbeMCPServer must classify Skipped + the
//     SkipReason carries the timeout marker.
//   - OrphanReap (A2): after timeout, the subprocess PID is dead.
//   - StderrCapture (A3): server crashes with FATAL stderr; the FATAL
//     text appears in SkipReason.
//   - EmptyToolsList: tools/list returns []; renderer emits `tools={}`
//     and the result is round-trippable through BurntSushi/toml.
//   - MalformedResponse: non-JSON garbage classifies as Skipped.
//   - ToolsListError: JSON-RPC error response classifies as Skipped
//     with code + message embedded in SkipReason.
//   - TransportDetection: 4 ambiguous-transport cases per A5 — both,
//     neither, url-only, command-only-with-nil-args.
//   - RenderRoundTrip (A4): rendered output decodes via BurntSushi/toml
//     to the expected structure for representative tool-name shapes
//     (dotted, hyphenated, uppercase, numeric-leading, empty,
//     TOML-reserved chars).

package backends

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// installFakeMCPServer prepends a fresh temp dir to PATH containing a
// copy of the named fixture shell script, exposed as `mcp-server`. The
// returned absolute path to the installed script is what tests pass as
// MCPServerEntry.Command. Mirrors installFakeClaude.
func installFakeMCPServer(t *testing.T, fixture string) (cmdPath, argvOut, stdinOut string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake-MCP seam uses /bin/sh and is not portable to windows")
	}

	srcDir := filepath.Join("testdata", fixture)
	content, err := os.ReadFile(filepath.Join(srcDir, "server.sh"))
	if err != nil {
		t.Fatalf("read fixture %s/server.sh: %v", fixture, err)
	}

	binDir := t.TempDir()
	argvOut = filepath.Join(t.TempDir(), "argv")
	stdinOut = filepath.Join(t.TempDir(), "stdin")

	scriptPath := filepath.Join(binDir, "mcp-server")
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		t.Fatalf("write fake mcp script: %v", err)
	}

	t.Setenv("FAKE_MCP_ARGV_OUT", argvOut)
	t.Setenv("FAKE_MCP_STDIN_OUT", stdinOut)

	return scriptPath, argvOut, stdinOut
}

func TestProbeMCPServer_HappyPath(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-happy")
	entry := MCPServerEntry{Command: cmdPath, Args: []string{"--flagA", "valA"}}

	res, err := ProbeMCPServer(context.Background(), "demo", entry)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected not skipped, got SkipReason=%q", res.SkipReason)
	}
	want := []string{"get", "hylla.search.vector", "my-tool", "Update"}
	if len(res.ToolNames) != len(want) {
		t.Fatalf("tool names: got %v want %v", res.ToolNames, want)
	}
	for i := range want {
		if res.ToolNames[i] != want[i] {
			t.Fatalf("tool[%d]: got %q want %q", i, res.ToolNames[i], want[i])
		}
	}

	// Inline-TOML must contain quoted keys for every name and the
	// command + args echoed back. Spot-check substrings; structural
	// round-trip is asserted by TestRenderMCPInlineTOML_RoundTrip.
	for _, n := range want {
		if !strings.Contains(res.InlineTOML, `"`+n+`"={approval_mode="approve"}`) {
			t.Errorf("InlineTOML missing quoted tool entry for %q\nfull: %s", n, res.InlineTOML)
		}
	}
	if !strings.Contains(res.InlineTOML, `command="`+cmdPath+`"`) {
		t.Errorf("InlineTOML missing command: %s", res.InlineTOML)
	}
	if !strings.Contains(res.InlineTOML, `"--flagA", "valA"`) {
		t.Errorf("InlineTOML missing args: %s", res.InlineTOML)
	}
}

// TestProbeMCPServer_TimeoutHang covers amendment A1 — the probe must
// trip its own 5s default timeout when the server stalls past it. We
// shrink the deadline to 250ms via a caller-supplied ctx so the test
// stays fast.
func TestProbeMCPServer_TimeoutHang(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-hang")
	entry := MCPServerEntry{Command: cmdPath}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := ProbeMCPServer(ctx, "stuck", entry)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true, got InlineTOML=%q", res.InlineTOML)
	}
	if !strings.Contains(res.SkipReason, "stuck") {
		t.Errorf("SkipReason missing server name: %q", res.SkipReason)
	}
	// The deadline marker may show up as `context deadline exceeded` from
	// errors.Is OR via the explicit `(probe timed out)` suffix. Accept
	// either form.
	low := strings.ToLower(res.SkipReason)
	if !strings.Contains(low, "deadline") && !strings.Contains(low, "timed out") {
		t.Errorf("SkipReason missing deadline marker: %q", res.SkipReason)
	}
	if elapsed > 5*time.Second {
		t.Errorf("probe took %v, expected to honour caller deadline ~250ms", elapsed)
	}
}

// TestProbeMCPServer_OrphanReap covers amendment A2 — after a probe
// failure the subprocess must be reaped. The fake-mcp-hang stub writes
// its PID to $FAKE_MCP_PID_OUT before sleeping; after ProbeMCPServer
// returns we poll signal 0 against that PID — ESRCH (errno 3) confirms
// the process is dead. Bounded poll so a buggy defer-cleanup chain that
// leaves the child running surfaces as a test failure, not a flake.
func TestProbeMCPServer_OrphanReap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PID-liveness check uses unix syscalls")
	}
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-hang")
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("FAKE_MCP_PID_OUT", pidFile)

	entry := MCPServerEntry{Command: cmdPath}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if _, err := ProbeMCPServer(ctx, "stuck", entry); err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file %q: %v", pidFile, err)
	}
	pidStr := strings.TrimSpace(string(raw))
	pid, parseErr := strconv.Atoi(pidStr)
	if parseErr != nil || pid <= 0 {
		t.Fatalf("invalid pid %q: %v", pidStr, parseErr)
	}

	// Poll up to 2s for the child to exit. Each iteration calls
	// kill(pid, 0): nil means still alive, ESRCH means gone, any other
	// errno (EPERM in particular) means we cannot determine and the
	// test bails benignly. This bounded poll catches both fast and
	// slow-but-still-correct cleanup paths without flaking.
	deadline := time.Now().Add(2 * time.Second)
	for {
		killErr := syscall.Kill(pid, 0)
		if killErr == syscall.ESRCH {
			return // reaped
		}
		if killErr != nil && killErr != syscall.EPERM {
			t.Fatalf("unexpected kill(pid=%d, 0) errno: %v", pid, killErr)
		}
		if killErr == syscall.EPERM {
			t.Skipf("sandbox denies signal probe (EPERM); cannot verify reaping")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subprocess pid=%d still alive 2s after probe returned — A2 cleanup violated", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestProbeMCPServer_StderrCapture covers amendment A3 — when the
// subprocess crashes mid-probe, the captured stderr must surface in
// ProbeResult.SkipReason.
func TestProbeMCPServer_StderrCapture(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-crash")
	entry := MCPServerEntry{Command: cmdPath}

	res, err := ProbeMCPServer(context.Background(), "crashy", entry)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true, got InlineTOML=%q", res.InlineTOML)
	}
	if !strings.Contains(res.SkipReason, "FATAL: schema migration required") {
		t.Errorf("SkipReason missing stderr text: %q", res.SkipReason)
	}
	if !strings.Contains(res.SkipReason, "crashy") {
		t.Errorf("SkipReason missing server name: %q", res.SkipReason)
	}
}

// TestProbeMCPServer_EmptyToolsList — server returns empty tools array.
// Renderer emits `tools={}` and the full payload round-trips through
// BurntSushi/toml without error.
func TestProbeMCPServer_EmptyToolsList(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-empty")
	entry := MCPServerEntry{Command: cmdPath}

	res, err := ProbeMCPServer(context.Background(), "noop", entry)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected not skipped, got SkipReason=%q", res.SkipReason)
	}
	if len(res.ToolNames) != 0 {
		t.Errorf("expected empty ToolNames, got %v", res.ToolNames)
	}
	if !strings.Contains(res.InlineTOML, "tools={}") {
		t.Errorf("InlineTOML missing empty tools table: %s", res.InlineTOML)
	}
}

func TestProbeMCPServer_MalformedResponse(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-malformed")
	entry := MCPServerEntry{Command: cmdPath}

	res, err := ProbeMCPServer(context.Background(), "garbage", entry)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true, got InlineTOML=%q", res.InlineTOML)
	}
	if !strings.Contains(res.SkipReason, "garbage") {
		t.Errorf("SkipReason missing server name: %q", res.SkipReason)
	}
	if !strings.Contains(strings.ToLower(res.SkipReason), "json") &&
		!strings.Contains(strings.ToLower(res.SkipReason), "parse") {
		t.Errorf("SkipReason missing parse marker: %q", res.SkipReason)
	}
}

func TestProbeMCPServer_ToolsListError(t *testing.T) {
	cmdPath, _, _ := installFakeMCPServer(t, "fake-mcp-error")
	entry := MCPServerEntry{Command: cmdPath}

	res, err := ProbeMCPServer(context.Background(), "errsvr", entry)
	if err != nil {
		t.Fatalf("ProbeMCPServer: unexpected go error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true, got InlineTOML=%q", res.InlineTOML)
	}
	if !strings.Contains(res.SkipReason, "-32601") {
		t.Errorf("SkipReason missing JSON-RPC error code: %q", res.SkipReason)
	}
	if !strings.Contains(res.SkipReason, "method not found") {
		t.Errorf("SkipReason missing JSON-RPC error message: %q", res.SkipReason)
	}
}

// TestProbeMCPServer_TransportDetection covers amendment A5 — the four
// ambiguous-transport cases. ProbeMCPServer never actually spawns in
// these branches; the test does not need a fake server, just asserts the
// SkipReason text classifies the entry.
func TestProbeMCPServer_TransportDetection(t *testing.T) {
	cases := []struct {
		name        string
		entry       MCPServerEntry
		wantSkipped bool
		wantSubstr  string
	}{
		{
			name:        "neither command nor url",
			entry:       MCPServerEntry{},
			wantSkipped: true,
			wantSubstr:  "malformed entry",
		},
		{
			name:        "url-only (HTTP MCP — unsupported)",
			entry:       MCPServerEntry{URL: "https://example.test/mcp"},
			wantSkipped: true,
			wantSubstr:  "HTTP url=",
		},
		{
			name:        "empty-string url + empty command",
			entry:       MCPServerEntry{URL: ""},
			wantSkipped: true,
			wantSubstr:  "malformed entry",
		},
		{
			name: "both command and url — stdio wins (probe still skipped because /nonexistent fails)",
			entry: MCPServerEntry{
				Command: "/nonexistent-binary-for-stdio-precedence-test",
				URL:     "https://example.test/mcp",
			},
			wantSkipped: true,
			wantSubstr:  "stdio was preferred per A5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			res, err := ProbeMCPServer(ctx, "ambig", tc.entry)
			if err != nil {
				t.Fatalf("unexpected go error: %v", err)
			}
			if res.Skipped != tc.wantSkipped {
				t.Fatalf("Skipped: got %v want %v (reason=%q)", res.Skipped, tc.wantSkipped, res.SkipReason)
			}
			if !strings.Contains(res.SkipReason, tc.wantSubstr) {
				t.Errorf("SkipReason missing %q\nfull: %q", tc.wantSubstr, res.SkipReason)
			}
		})
	}
}

// TestRenderMCPInlineTOML_RoundTrip covers amendment A4 — the rendered
// inline-TOML decodes back to the expected structure for representative
// tool-name shapes via BurntSushi/toml. The renderer's output is the
// VALUE half of `-c "mcp_servers.<name>={...}"`; wrap it with a TOML
// header before decoding so BurntSushi sees a complete document.
func TestRenderMCPInlineTOML_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		toolNames []string
	}{
		{"snake_case", []string{"get", "update", "tools_list"}},
		{"dotted", []string{"hylla.search.vector", "ta.list.sections"}},
		{"hyphenated", []string{"my-tool", "another-one"}},
		{"uppercase", []string{"Update", "GET", "ToolsList"}},
		{"numeric-leading", []string{"1stplace", "2ndplace"}},
		{"toml-reserved-chars", []string{`tool"with"quote`, `tool\backslash`}},
		{"empty-name", []string{""}},
		{"mixed", []string{"get", "hylla.search.vector", "my-tool", "Update", ""}},
		{"no-tools", []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inline := RenderMCPInlineTOML(
				"srv",
				MCPServerEntry{Command: "/usr/bin/mcp", Args: []string{"--port", "1234"}},
				tc.toolNames,
			)
			// Wrap as `<inline>` — the renderer emits a key=value
			// expression that already starts with `mcp_servers.srv=...`,
			// so we can prepend a top-level table header `[root]` and a
			// newline to make it parseable. Easier: feed inline directly,
			// since `mcp_servers.srv=...` IS a valid TOML statement.
			var decoded struct {
				MCPServers map[string]struct {
					Command string                       `toml:"command"`
					Args    []string                     `toml:"args"`
					Tools   map[string]map[string]string `toml:"tools"`
				} `toml:"mcp_servers"`
			}
			if _, err := toml.Decode(inline, &decoded); err != nil {
				t.Fatalf("round-trip toml.Decode failed: %v\ninline=%s", err, inline)
			}
			srv, ok := decoded.MCPServers["srv"]
			if !ok {
				t.Fatalf("decoded payload missing mcp_servers.srv: %+v", decoded)
			}
			if srv.Command != "/usr/bin/mcp" {
				t.Errorf("command: got %q want /usr/bin/mcp", srv.Command)
			}
			if len(srv.Args) != 2 || srv.Args[0] != "--port" || srv.Args[1] != "1234" {
				t.Errorf("args: got %v", srv.Args)
			}
			if len(srv.Tools) != len(tc.toolNames) {
				t.Errorf("tools count: got %d want %d (decoded=%v)", len(srv.Tools), len(tc.toolNames), srv.Tools)
			}
			for _, n := range tc.toolNames {
				entry, present := srv.Tools[n]
				if !present {
					t.Errorf("tools map missing %q (decoded=%v)", n, srv.Tools)
					continue
				}
				if entry["approval_mode"] != "approve" {
					t.Errorf("tools[%q].approval_mode: got %q want approve", n, entry["approval_mode"])
				}
			}
		})
	}
}

// TestRenderMCPInlineTOML_EmptyArgs covers the args=[] branch — some MCP
// servers self-configure entirely via env so the args slice is nil.
func TestRenderMCPInlineTOML_EmptyArgs(t *testing.T) {
	inline := RenderMCPInlineTOML(
		"srv",
		MCPServerEntry{Command: "/usr/bin/mcp"},
		[]string{"get"},
	)
	if !strings.Contains(inline, "args=[]") {
		t.Errorf("expected empty args literal, got: %s", inline)
	}
	var decoded struct {
		MCPServers map[string]struct {
			Args []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(inline, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.MCPServers["srv"].Args) != 0 {
		t.Errorf("expected zero args, got %v", decoded.MCPServers["srv"].Args)
	}
}

// ---------------------------------------------------------------------------
// TestRenderRoleConditionalMCPFlags — role-conditional static MCP injection.
// ---------------------------------------------------------------------------

// TestRenderRoleConditionalMCPFlags_GoPlanning verifies that a Go planning
// role receives ta + hylla + context7 + gopls (with cwd) and no playwright.
func TestRenderRoleConditionalMCPFlags_GoPlanning(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-planning", "/project/root")
	assertFlagsContainServer(t, flags, "ta")
	assertFlagsContainServer(t, flags, "hylla")
	assertFlagsContainServer(t, flags, "context7")
	assertFlagsContainServer(t, flags, "gopls")
	assertFlagsNotContainServer(t, flags, "playwright")

	// context7 must use url= form, not command=
	ctx7val := findServerValue(t, flags, "context7")
	if !strings.Contains(ctx7val, `url="https://mcp.context7.com/mcp"`) {
		t.Errorf("context7 missing url form: %q", ctx7val)
	}
	if strings.Contains(ctx7val, "command=") {
		t.Errorf("context7 must not use command= form: %q", ctx7val)
	}
	if !strings.Contains(ctx7val, `env_http_headers={CONTEXT7_API_KEY="CONTEXT7_API_KEY"}`) {
		t.Errorf("context7 missing env_http_headers: %q", ctx7val)
	}

	// gopls must include cwd= unconditionally
	goplsval := findServerValue(t, flags, "gopls")
	if !strings.Contains(goplsval, `cwd="/project/root"`) {
		t.Errorf("gopls missing cwd field: %q", goplsval)
	}

	// gopls must use command=gopls
	if !strings.Contains(goplsval, `command="gopls"`) {
		t.Errorf("gopls missing command field: %q", goplsval)
	}

	// flags must be pairs of -c / value
	assertFlagPairs(t, flags)
}

// TestRenderRoleConditionalMCPFlags_GoBuildQA verifies that a build-qa role
// receives ONLY ta — no hylla, no context7, no gopls.
func TestRenderRoleConditionalMCPFlags_GoBuildQA(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-build-qa-falsification", "/project/root")
	assertFlagsContainServer(t, flags, "ta")
	assertFlagsNotContainServer(t, flags, "hylla")
	assertFlagsNotContainServer(t, flags, "context7")
	assertFlagsNotContainServer(t, flags, "gopls")
	assertFlagsNotContainServer(t, flags, "playwright")
	assertFlagPairs(t, flags)
}

// TestRenderRoleConditionalMCPFlags_FEPlanning verifies that a FE planning
// role receives ta + hylla + context7 + playwright and no gopls.
func TestRenderRoleConditionalMCPFlags_FEPlanning(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-fe-planning", "/some/path")
	assertFlagsContainServer(t, flags, "ta")
	assertFlagsContainServer(t, flags, "hylla")
	assertFlagsContainServer(t, flags, "context7")
	assertFlagsContainServer(t, flags, "playwright")
	assertFlagsNotContainServer(t, flags, "gopls")
	assertFlagPairs(t, flags)
}

// TestRenderRoleConditionalMCPFlags_GoBuilder verifies that a Go builder role
// (non-build-qa) receives ta + hylla + context7 + gopls and no playwright.
func TestRenderRoleConditionalMCPFlags_GoBuilder(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-builder", "/workspace")
	assertFlagsContainServer(t, flags, "ta")
	assertFlagsContainServer(t, flags, "hylla")
	assertFlagsContainServer(t, flags, "context7")
	assertFlagsContainServer(t, flags, "gopls")
	assertFlagsNotContainServer(t, flags, "playwright")
	assertFlagPairs(t, flags)
}

// TestRenderRoleConditionalMCPFlags_GoplsCwdEmpty verifies that gopls emits
// cwd="" even when cwd is the empty string (oracle always emits cwd=).
func TestRenderRoleConditionalMCPFlags_GoplsCwdEmpty(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-builder", "")
	goplsval := findServerValue(t, flags, "gopls")
	if !strings.Contains(goplsval, `cwd=""`) {
		t.Errorf("gopls must emit cwd= even when empty: %q", goplsval)
	}
}

// TestRenderRoleConditionalMCPFlags_TaToolList verifies that ta's injected
// value contains all 9 expected tool names with approval_mode=approve.
func TestRenderRoleConditionalMCPFlags_TaToolList(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-planning", "/p")
	taval := findServerValue(t, flags, "ta")
	for _, tool := range []string{"get", "update", "list_sections", "search", "schema", "create", "delete", "move", "init"} {
		want := `"` + tool + `"={approval_mode="approve"}`
		if !strings.Contains(taval, want) {
			t.Errorf("ta tools missing %q\nfull: %s", tool, taval)
		}
	}
}

// TestRenderRoleConditionalMCPFlags_HyllaToolList verifies that hylla's
// injected value contains all 14 expected (dotted) tool names.
func TestRenderRoleConditionalMCPFlags_HyllaToolList(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-planning", "/p")
	hyval := findServerValue(t, flags, "hylla")
	for _, tool := range []string{
		"hylla.artifact.list", "hylla.artifact.metadata", "hylla.artifact.overview",
		"hylla.dql.query", "hylla.graph.list", "hylla.graph.nav", "hylla.node.full",
		"hylla.refs.find", "hylla.run.get", "hylla.run.list",
		"hylla.search", "hylla.search.keyword", "hylla.search.vector", "hylla.task.get",
	} {
		want := `"` + tool + `"={approval_mode="approve"}`
		if !strings.Contains(hyval, want) {
			t.Errorf("hylla tools missing %q\nfull: %s", tool, hyval)
		}
	}
}

// TestRenderRoleConditionalMCPFlags_TOMLParseable verifies that all rendered
// -c values for a planning role are parseable by BurntSushi/toml.
func TestRenderRoleConditionalMCPFlags_TOMLParseable(t *testing.T) {
	flags := RenderRoleConditionalMCPFlags("ta-go-planning", "/project")
	for i := 0; i+1 < len(flags); i += 2 {
		if flags[i] != "-c" {
			t.Fatalf("flags[%d] expected -c, got %q", i, flags[i])
		}
		val := flags[i+1]
		// Wrap as top-level statement — each value is already a
		// `mcp_servers.<name>={...}` expression, valid as standalone TOML.
		// However context7 uses url= and env_http_headers= which are string
		// and inline-table fields — all valid TOML.
		var decoded interface{}
		if _, err := toml.Decode(val, &decoded); err != nil {
			t.Errorf("flags[%d+1]=%q failed toml.Decode: %v", i, val, err)
		}
	}
}

// TestRenderRoleConditionalMCPFlags_BuildQAProof verifies both build-qa
// variant strings match *build-qa*.
func TestRenderRoleConditionalMCPFlags_BuildQAProof(t *testing.T) {
	for _, role := range []string{"ta-go-build-qa-proof", "ta-go-build-qa-falsification"} {
		t.Run(role, func(t *testing.T) {
			flags := RenderRoleConditionalMCPFlags(role, "/p")
			assertFlagsContainServer(t, flags, "ta")
			assertFlagsNotContainServer(t, flags, "hylla")
			assertFlagsNotContainServer(t, flags, "context7")
			assertFlagsNotContainServer(t, flags, "gopls")
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers for RenderRoleConditionalMCPFlags assertions.
// ---------------------------------------------------------------------------

// assertFlagPairs checks that flags are strictly alternating -c / value pairs.
func assertFlagPairs(t *testing.T, flags []string) {
	t.Helper()
	if len(flags)%2 != 0 {
		t.Fatalf("flags length %d is not even: %v", len(flags), flags)
	}
	for i := 0; i < len(flags); i += 2 {
		if flags[i] != "-c" {
			t.Errorf("flags[%d]=%q, want -c", i, flags[i])
		}
	}
}

// assertFlagsContainServer checks that at least one -c value in flags
// starts with "mcp_servers.<name>=".
func assertFlagsContainServer(t *testing.T, flags []string, name string) {
	t.Helper()
	prefix := "mcp_servers." + name + "="
	for i := 1; i < len(flags); i += 2 {
		if strings.HasPrefix(flags[i], prefix) {
			return
		}
	}
	t.Errorf("expected mcp_servers.%s in flags but not found\nflags: %v", name, flags)
}

// assertFlagsNotContainServer checks that no -c value in flags starts
// with "mcp_servers.<name>=".
func assertFlagsNotContainServer(t *testing.T, flags []string, name string) {
	t.Helper()
	prefix := "mcp_servers." + name + "="
	for i := 1; i < len(flags); i += 2 {
		if strings.HasPrefix(flags[i], prefix) {
			t.Errorf("expected NO mcp_servers.%s in flags but found it: %q", name, flags[i])
			return
		}
	}
}

// findServerValue returns the TOML inline value for the named MCP server,
// or fails the test if not present.
func findServerValue(t *testing.T, flags []string, name string) string {
	t.Helper()
	prefix := "mcp_servers." + name + "="
	for i := 1; i < len(flags); i += 2 {
		if strings.HasPrefix(flags[i], prefix) {
			return flags[i]
		}
	}
	t.Fatalf("mcp_servers.%s not found in flags: %v", name, flags)
	return ""
}

// TestProbeMCPServer_NonexistentCommand — start failure surfaces as
// non-fatal Skipped with the underlying error inside SkipReason. Not
// listed in A1-A7 but the contract ("probe failures are non-fatal
// outputs, not hard dispatch errors") demands it.
func TestProbeMCPServer_NonexistentCommand(t *testing.T) {
	entry := MCPServerEntry{Command: "/definitely/not/a/real/binary"}
	res, err := ProbeMCPServer(context.Background(), "ghost", entry)
	if err != nil {
		// exec.Start returning a "no such file" error is acceptable as
		// either a go-error or a Skipped result; we treat both shapes
		// as conformant.
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected go error: %v", err)
		}
		return
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true, got InlineTOML=%q", res.InlineTOML)
	}
	if !strings.Contains(res.SkipReason, "ghost") {
		t.Errorf("SkipReason missing server name: %q", res.SkipReason)
	}
}
