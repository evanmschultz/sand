// Package main is the sand MCP server entrypoint.
//
// v0.1 stub: registers zero tools. The server completes the MCP stdio
// handshake and then exits cleanly when stdin is closed. Tool registration
// (dispatch, preflight, persona_get, chains_list) lands in subsequent
// droplets — see SAND-SPEC.md §2.
package main

import (
	"log"

	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "sand"
	serverVersion = "0.1.0-dev"
)

func main() {
	s := server.NewMCPServer(serverName, serverVersion)

	// Stdio transport blocks until stdin EOF, at which point ServeStdio
	// returns nil and the process exits cleanly.
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("sand: stdio server error: %v", err)
	}
}
