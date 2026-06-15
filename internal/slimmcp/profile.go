package slimmcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// UpstreamSpec describes the launch parameters for the real upstream MCP server
// that sand will wrap. It captures the executable command, any arguments, and
// environment variables needed to spawn the process.
type UpstreamSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
}

// Profile is the ephemeral per-agent profile sand loads when invoked with
// `sand mcp --profile <path>`. It specifies sand's own server identity,
// the upstream MCP server to wrap, and the lagom policy that gates the
// upstream tools.
//
// The optional exercise and grant capability fields are reserved for the
// dynamic-mint layer and are not required here.
type Profile struct {
	ServerName string          `json:"server_name"`
	Upstream   UpstreamSpec    `json:"upstream"`
	Policy     json.RawMessage `json:"policy"`
}

// LoadProfile reads and parses a Profile from a JSON file at path. It validates
// that ServerName and Upstream.Command are non-empty, that Policy is present and
// valid JSON. It returns a wrapped error on any validation failure or I/O error.
func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("slimmcp: load profile %q: %w", path, err)
	}

	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("slimmcp: load profile %q: %w", path, err)
	}

	if p.ServerName == "" {
		return nil, fmt.Errorf("slimmcp: load profile %q: server_name is required and must not be empty", path)
	}
	if p.Upstream.Command == "" {
		return nil, fmt.Errorf("slimmcp: load profile %q: upstream.command is required and must not be empty", path)
	}
	if len(p.Policy) == 0 {
		return nil, fmt.Errorf("slimmcp: load profile %q: policy is required and must not be empty", path)
	}
	if !json.Valid(p.Policy) {
		return nil, fmt.Errorf("slimmcp: load profile %q: policy is not valid JSON", path)
	}
	// Reject non-object/non-array policies (strings, numbers, booleans, null).
	var val any
	if err := json.Unmarshal(p.Policy, &val); err == nil {
		switch val.(type) {
		case map[string]any:
			// valid object
		case []any:
			// valid array
		default:
			return nil, fmt.Errorf("slimmcp: load profile %q: policy must be a JSON object or array", path)
		}
	}

	return &p, nil
}
