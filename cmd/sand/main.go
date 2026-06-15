// Package main is the sand MCP server entrypoint.
//
// Registers the sand.dispatch MCP tool (SAND-SPEC §3.1) on a stdio MCP server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/server"

	"github.com/evanmschultz/sand/internal/debugtools"
	"github.com/evanmschultz/sand/internal/dispatch"
	"github.com/evanmschultz/sand/internal/gate"
	"github.com/evanmschultz/sand/internal/preflight"
	"github.com/evanmschultz/sand/internal/slimmcp"
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

	// `sand mcp` is the slim MCP server subcommand: spawns and wraps an upstream
	// MCP server (as defined in a profile) via lagom slimmcp, enforcing a strict
	// tool policy. Reaps the upstream process group on stdin EOF or signal.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		os.Exit(runMCP(os.Args[2:]))
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

// runMCP stands up a branded slim MCP server wrapping an upstream MCP server
// loaded from a profile. It parses --profile <path>, loads the profile via
// lagom slimmcp, dials the upstream, spawns a new branded server, and serves
// on stdio. The upstream process is reaped on stdin EOF (via defer) or on
// SIGINT/SIGTERM (via signal handler). Returns 0 on success, 1 on runtime
// error, 2 on flag parsing error.
func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	profilePath := fs.String("profile", "", "path to the slimmcp profile")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "sand mcp: flag parse error: %v\n", err)
		return 2
	}

	if *profilePath == "" {
		fmt.Fprintf(os.Stderr, "sand mcp: --profile is required\n")
		return 2
	}

	ctx := context.Background()

	profile, err := slimmcp.LoadProfile(*profilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sand mcp: load profile error: %v\n", err)
		return 1
	}

	up, closer, err := slimmcp.DialUpstream(ctx, profile.Upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sand mcp: dial upstream error: %v\n", err)
		return 1
	}
	defer closer()

	// Install signal handler to reap upstream on SIGINT/SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		_ = closer()
		os.Exit(0)
	}()

	srv, err := slimmcp.NewBrandedServer(ctx, profile.ServerName, *up, profile.Policy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sand mcp: new branded server error: %v\n", err)
		return 1
	}

	if err := server.ServeStdio(srv); err != nil {
		fmt.Fprintf(os.Stderr, "sand mcp: stdio server error: %v\n", err)
		return 1
	}

	return 0
}
