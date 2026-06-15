package slimmcp

import (
	"encoding/json"
	"testing"
)

func TestMapUpstreamDefs(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantNames []string
		wantError bool
		check     func(t *testing.T, output []byte)
	}{
		{
			name: "single tool camelCase to snake_case",
			input: `[{
				"name": "tool1",
				"description": "A test tool",
				"inputSchema": {"type": "object", "properties": {}}
			}]`,
			wantNames: []string{"tool1"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(defs) != 1 {
					t.Fatalf("expected 1 def, got %d", len(defs))
				}
				if defs[0].Name != "tool1" {
					t.Errorf("want name tool1, got %s", defs[0].Name)
				}
				if defs[0].Description == nil || *defs[0].Description != "A test tool" {
					t.Errorf("want description 'A test tool', got %v", defs[0].Description)
				}
				// Verify input_schema is present and is an object.
				var schema map[string]any
				if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
					t.Fatalf("unmarshal input_schema: %v", err)
				}
				if schema["type"] != "object" {
					t.Errorf("want input_schema.type=object, got %v", schema["type"])
				}
			},
		},
		{
			name: "multiple tools preserve order",
			input: `[
				{"name": "first", "inputSchema": {"type": "object"}},
				{"name": "second", "inputSchema": {"type": "string"}},
				{"name": "third", "inputSchema": {"type": "array"}}
			]`,
			wantNames: []string{"first", "second", "third"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(defs) != 3 {
					t.Fatalf("expected 3 defs, got %d", len(defs))
				}
				for i, want := range []string{"first", "second", "third"} {
					if defs[i].Name != want {
						t.Errorf("def[%d]: want name %s, got %s", i, want, defs[i].Name)
					}
				}
			},
		},
		{
			name: "tool without description omits field",
			input: `[{
				"name": "nodesc",
				"inputSchema": {"type": "object"}
			}]`,
			wantNames: []string{"nodesc"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if defs[0].Description != nil {
					t.Errorf("want nil description, got %v", defs[0].Description)
				}
			},
		},
		{
			name: "tool with empty description omits field",
			input: `[{
				"name": "emptydesc",
				"description": "",
				"inputSchema": {"type": "object"}
			}]`,
			wantNames: []string{"emptydesc"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if defs[0].Description != nil {
					t.Errorf("want nil description for empty string, got %v", defs[0].Description)
				}
			},
		},
		{
			name: "inputSchema bytes preserved",
			input: `[{
				"name": "schema_test",
				"inputSchema": {"type": "object", "properties": {"key": {"type": "string"}}, "required": ["key"]}
			}]`,
			wantNames: []string{"schema_test"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				// Verify the schema round-trips cleanly and matches the structure.
				var schema map[string]any
				if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
					t.Fatalf("unmarshal input_schema: %v", err)
				}
				if schema["type"] != "object" {
					t.Errorf("want type=object, got %v", schema["type"])
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Errorf("want properties to be an object, got type %T", schema["properties"])
				}
				if len(props) != 1 {
					t.Errorf("want 1 property, got %d", len(props))
				}
				required, ok := schema["required"].([]any)
				if !ok {
					t.Errorf("want required to be an array, got type %T", schema["required"])
				}
				if len(required) != 1 {
					t.Errorf("want 1 required field, got %d", len(required))
				}
			},
		},
		{
			name:      "malformed input JSON",
			input:     `not valid json`,
			wantError: true,
		},
		{
			name:      "missing name field",
			input:     `[{"inputSchema": {"type": "object"}}]`,
			wantError: true,
		},
		{
			name: "inputSchema JSON null becomes empty object",
			input: `[{
				"name": "null_schema",
				"inputSchema": null
			}]`,
			wantNames: []string{"null_schema"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(defs) != 1 {
					t.Fatalf("expected 1 def, got %d", len(defs))
				}
				// Verify the schema is an empty object, not null.
				var schema map[string]any
				if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
					t.Fatalf("unmarshal input_schema: %v", err)
				}
				if len(schema) != 0 {
					t.Errorf("want empty object schema, got %v", schema)
				}
			},
		},
		{
			name: "missing inputSchema becomes empty object",
			input: `[{
				"name": "missing_schema"
			}]`,
			wantNames: []string{"missing_schema"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(defs) != 1 {
					t.Fatalf("expected 1 def, got %d", len(defs))
				}
				// Verify the schema is an empty object.
				var schema map[string]any
				if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
					t.Fatalf("unmarshal input_schema: %v", err)
				}
				if len(schema) != 0 {
					t.Errorf("want empty object schema, got %v", schema)
				}
			},
		},
		{
			name: "numeric schema value preserved verbatim",
			input: `[{
				"name": "numeric_schema",
				"inputSchema": {"type": "object", "properties": {"count": {"type": "integer", "maximum": 100}}}
			}]`,
			wantNames: []string{"numeric_schema"},
			check: func(t *testing.T, output []byte) {
				var defs []slimDef
				if err := json.Unmarshal(output, &defs); err != nil {
					t.Fatalf("unmarshal output: %v", err)
				}
				if len(defs) != 1 {
					t.Fatalf("expected 1 def, got %d", len(defs))
				}
				var schema map[string]any
				if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
					t.Fatalf("unmarshal input_schema: %v", err)
				}
				props, ok := schema["properties"].(map[string]any)
				if !ok {
					t.Fatalf("want properties to be an object, got type %T", schema["properties"])
				}
				countProp, ok := props["count"].(map[string]any)
				if !ok {
					t.Fatalf("want count property to be an object, got type %T", props["count"])
				}
				maximum, ok := countProp["maximum"].(float64)
				if !ok {
					t.Fatalf("want maximum to be a number, got type %T", countProp["maximum"])
				}
				if maximum != 100 {
					t.Errorf("want maximum=100, got %v", maximum)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := MapUpstreamDefs([]byte(tt.input))
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, output)
			}
		})
	}
}
