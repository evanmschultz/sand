package slimmcp

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLoadProfile(t *testing.T) {
	tests := []struct {
		name     string
		jsonText string
		wantErr  bool
		check    func(t *testing.T, p *Profile)
	}{
		{
			name: "valid profile round-trip",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"command": "node",
					"args": ["server.js"],
					"env": ["FOO=bar"]
				},
				"policy": {"rules":[]}
			}`,
			wantErr: false,
			check: func(t *testing.T, p *Profile) {
				if p.ServerName != "sand" {
					t.Errorf("want ServerName=sand, got %q", p.ServerName)
				}
				if p.Upstream.Command != "node" {
					t.Errorf("want Command=node, got %q", p.Upstream.Command)
				}
				if len(p.Upstream.Args) != 1 || p.Upstream.Args[0] != "server.js" {
					t.Errorf("want Args=[server.js], got %v", p.Upstream.Args)
				}
				if len(p.Upstream.Env) != 1 || p.Upstream.Env[0] != "FOO=bar" {
					t.Errorf("want Env=[FOO=bar], got %v", p.Upstream.Env)
				}
				if !json.Valid(p.Policy) {
					t.Error("policy is not valid JSON")
				}
			},
		},
		{
			name: "missing server_name",
			jsonText: `{
				"upstream": {
					"command": "node",
					"args": [],
					"env": []
				},
				"policy": {}
			}`,
			wantErr: true,
		},
		{
			name: "empty server_name",
			jsonText: `{
				"server_name": "",
				"upstream": {
					"command": "node",
					"args": [],
					"env": []
				},
				"policy": {}
			}`,
			wantErr: true,
		},
		{
			name: "missing upstream.command",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"args": [],
					"env": []
				},
				"policy": {}
			}`,
			wantErr: true,
		},
		{
			name: "empty upstream.command",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"command": "",
					"args": [],
					"env": []
				},
				"policy": {}
			}`,
			wantErr: true,
		},
		{
			name: "missing policy",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"command": "node",
					"args": [],
					"env": []
				}
			}`,
			wantErr: true,
		},
		{
			name: "empty policy",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"command": "node",
					"args": [],
					"env": []
				},
				"policy": ""
			}`,
			wantErr: true,
		},
		{
			name: "malformed JSON policy",
			jsonText: `{
				"server_name": "sand",
				"upstream": {
					"command": "node",
					"args": [],
					"env": []
				},
				"policy": {invalid json}
			}`,
			wantErr: true,
		},
		{
			name:     "nonexistent path",
			jsonText: "", // will not write a file
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent path" {
				_, err := LoadProfile("/nonexistent/path/profile.json")
				if err == nil {
					t.Error("expected error for nonexistent path, got nil")
				}
				return
			}

			tmpDir := t.TempDir()
			profilePath := tmpDir + "/profile.json"

			if err := os.WriteFile(profilePath, []byte(tt.jsonText), 0o644); err != nil {
				t.Fatalf("failed to write profile file: %v", err)
			}

			p, err := LoadProfile(profilePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadProfile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}
