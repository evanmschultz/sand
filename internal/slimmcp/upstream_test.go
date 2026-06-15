//go:build unix

package slimmcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestMain re-execs this test binary as an echo MCP server when the sentinel
// env var is set, allowing hermetic subprocess testing without external binaries.
func TestMain(m *testing.M) {
	if os.Getenv("SAND_TEST_UPSTREAM") == "1" {
		runEchoUpstream()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runEchoUpstream builds and serves a minimal MCP server with an "echo" tool.
// It writes its PID to the file specified by SAND_TEST_UPSTREAM_PIDFILE if set,
// allowing the parent test to verify process reaping.
func runEchoUpstream() {
	// Create an MCP server with a single echo tool.
	srv := server.NewMCPServer("echo-upstream", "0.1.0")

	// Define the echo tool with a required message parameter using raw JSON schema.
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"message": {
				"type": "string",
				"description": "The message to echo"
			}
		},
		"required": ["message"]
	}`)

	tool := mcp.NewToolWithRawSchema("echo", "Echo the provided message", schema)

	// Register the tool with a handler that echoes back the message.
	srv.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		msg := request.GetString("message", "")
		if msg == "" {
			return &mcp.CallToolResult{IsError: true}, nil
		}
		return mcp.NewToolResultText(msg), nil
	})

	// Write PID to file if requested, so the parent test can verify process reaping.
	if pidFile := os.Getenv("SAND_TEST_UPSTREAM_PIDFILE"); pidFile != "" {
		pid := strconv.Itoa(os.Getpid())
		_ = os.WriteFile(pidFile, []byte(pid), 0o644) //nolint:errcheck // best-effort
	}

	// Serve stdio and block until the client closes.
	_ = server.ServeStdio(srv) //nolint:errcheck // expected to fail on client close
}

// TestDialUpstream verifies that DialUpstream spawns a child MCP server,
// probes its tool definitions, executes a round-trip call, and properly
// reaps the child process on close.
func TestDialUpstream(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for the PID file.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "pid")

	// Build the spec to re-exec this test binary as the echo server.
	spec := UpstreamSpec{
		Command: os.Args[0],
		Env: append(
			os.Environ(),
			"SAND_TEST_UPSTREAM=1",
			"SAND_TEST_UPSTREAM_PIDFILE="+pidFile,
		),
	}

	// Dial the upstream server.
	up, closer, err := DialUpstream(ctx, spec)
	if err != nil {
		t.Fatalf("DialUpstream failed: %v", err)
	}
	defer func() {
		if closer != nil {
			_ = closer() //nolint:errcheck // best-effort cleanup
		}
	}()

	// Verify tool definitions are present and properly formatted.
	var defs []map[string]any
	if err := json.Unmarshal(up.Defs, &defs); err != nil {
		t.Fatalf("unmarshal tool defs: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(defs))
	}

	// Check tool name.
	if name, ok := defs[0]["name"].(string); !ok || name != "echo" {
		t.Errorf("expected tool name 'echo', got %v", defs[0]["name"])
	}

	// Verify the input_schema is a valid JSON object (not a base64 string).
	// This guards against schema-mapping bugs.
	rawSchema, ok := defs[0]["input_schema"]
	if !ok {
		t.Fatalf("input_schema field missing from tool def")
	}

	// Unmarshal the schema to verify it's a valid object.
	schemaBytes, err := json.Marshal(rawSchema)
	if err != nil {
		t.Fatalf("marshal input_schema: %v", err)
	}

	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("unmarshal input_schema as object: %v", err)
	}

	if schema["type"] != "object" {
		t.Errorf("expected input_schema type=object, got %v", schema["type"])
	}

	// Test round-trip execution: call the echo tool with a message.
	result, err := up.Run(ctx, "echo", map[string]json.RawMessage{
		"message": json.RawMessage(`"hello-test"`),
	})
	if err != nil {
		t.Fatalf("Run echo tool: %v", err)
	}
	if result != "hello-test" {
		t.Errorf("expected 'hello-test', got %q", result)
	}

	// Read the child PID to verify process reaping.
	var pidStr string
	deadline := time.Now().Add(2 * time.Second)
	for {
		pidBytes, err := os.ReadFile(pidFile)
		if err == nil {
			pidStr = string(pidBytes)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for PID file: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("parse PID: %v", err)
	}

	// Close the upstream connection.
	if err := closer(); err != nil {
		// Closing may produce an error if the child already exited; that is acceptable.
		t.Logf("closer returned error (acceptable): %v", err)
	}

	// Poll for the child process to exit.
	// On unix, Kill(pid, 0) returns an error (ESRCH) once the process is dead.
	deadline = time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err != nil {
			// Process is dead; check that it's specifically ESRCH (no such process).
			if err == syscall.ESRCH {
				return // Process successfully reaped.
			}
		}
		if time.Now().After(deadline) {
			t.Errorf("timeout waiting for process %d to be reaped", pid)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
