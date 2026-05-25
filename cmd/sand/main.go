// Package main is the sand MCP server entrypoint.
//
// Registers the sand.dispatch MCP tool (SAND-SPEC §3.1) on a stdio MCP server.
package main

import (
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"github.com/evanmschultz/sand/internal/debugtools"
	"github.com/evanmschultz/sand/internal/dispatch"
	"github.com/evanmschultz/sand/internal/gate"
	"github.com/evanmschultz/sand/internal/preflight"
)

const (
	serverName    = "sand"
	serverVersion = "0.1.0-dev"
)

func main() {
	// `sand gate` is the PreToolUse hook subcommand: Claude Code invokes it on
	// every PreToolUse event, piping the event JSON on stdin; it emits the
	// permissionDecision envelope on stdout and exits. It is NOT the MCP server.
	// This is the Go, cross-OS end-state of the bin/sh reference hook
	// (.claude/hooks/ta_action_gate.py; HYLLA_BIN.md §5.1).
	if len(os.Args) > 1 && os.Args[1] == "gate" {
		os.Exit(gate.Run(os.Stdin, os.Stdout, os.Getenv))
	}

	s := server.NewMCPServer(serverName, serverVersion)

	// projectDir source for tool wiring. Currently derives from cwd; the
	// `--project <abs-path>` flag (mirroring ta's MCP convention) is the
	// planned upgrade. Tools that accept a project root (persona_get,
	// chains_list, preflight) close over this single value at registration
	// time.
	projectDir, _ := os.Getwd()

	// drop_006: sand.persona_get
	personaGetTool, personaGetHandler := debugtools.PersonaGetTool(projectDir)
	s.AddTool(personaGetTool, personaGetHandler)

	// drop_006: sand.chains_list
	chainsListTool, chainsListHandler := debugtools.ChainsListTool(projectDir)
	s.AddTool(chainsListTool, chainsListHandler)

	// drop_006: sand.preflight
	preflightTool, preflightHandler := preflight.PreflightTool(projectDir)
	s.AddTool(preflightTool, preflightHandler)

	// --- sand.dispatch registration (drop_003) ---
	s.AddTool(dispatch.NewDispatchTool(), dispatch.DispatchHandler)
	// --- end sand.dispatch registration ---

	// Stdio transport blocks until stdin EOF, at which point ServeStdio
	// returns nil and the process exits cleanly.
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("sand: stdio server error: %v", err)
	}
}
