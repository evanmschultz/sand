package chains

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParse exercises Parse against inline TOML fixtures covering the
// acceptance criteria for drop_002.drop.droplet_chains_parse:
//
//   - happy path: a valid inline config with two roles + multiple tiers
//     decodes into a populated Config.
//   - malformed TOML returns an error.
//   - unknown TOML fields (top-level keys not on Config / Tier) return an
//     error sourced from MetaData.Undecoded().
//
// Fixture-backed round-trip against testdata/chains.toml is deferred to the
// lookup droplet per the planner's acceptance criteria.
func TestParse(t *testing.T) {
	t.Parallel()

	const happyConfig = `
[chains]
"ta-go-builder" = [
  { backend = "ollama-local",  model = "qwen2.5-coder:7b", opts = "",                                                        wait_max = 20, slots = 4 },
  { backend = "codex-exec",    model = "gpt-5.5",          opts = "--sandbox workspace-write -c model_reasoning_effort=low", wait_max = 0,  slots = 0 },
  { backend = "claude-native", model = "haiku",            opts = "",                                                        wait_max = 0,  slots = 0 },
]
"ta-go-planning" = [
  { backend = "codex-exec",    model = "gpt-5.5", opts = "--sandbox read-only -c model_reasoning_effort=medium", wait_max = 0, slots = 0 },
  { backend = "claude-native", model = "opus",    opts = "",                                                     wait_max = 0, slots = 0 },
]
`

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		errSubstr string
		check     func(t *testing.T, cfg Config)
	}{
		{
			name:    "happy path two roles",
			input:   happyConfig,
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				t.Helper()

				if got, want := len(cfg.Roles), 2; got != want {
					t.Fatalf("len(Roles) = %d, want %d", got, want)
				}

				builder, ok := cfg.Roles["ta-go-builder"]
				if !ok {
					t.Fatalf("Roles missing key ta-go-builder; have %v", keysOf(cfg.Roles))
				}
				if got, want := len(builder), 3; got != want {
					t.Fatalf("ta-go-builder tier count = %d, want %d", got, want)
				}
				if got, want := builder[0].Backend, "ollama-local"; got != want {
					t.Errorf("builder[0].Backend = %q, want %q", got, want)
				}
				if got, want := builder[0].Model, "qwen2.5-coder:7b"; got != want {
					t.Errorf("builder[0].Model = %q, want %q", got, want)
				}
				if got, want := builder[0].WaitMax, 20; got != want {
					t.Errorf("builder[0].WaitMax = %d, want %d", got, want)
				}
				if got, want := builder[0].Slots, 4; got != want {
					t.Errorf("builder[0].Slots = %d, want %d", got, want)
				}
				if got, want := builder[1].Opts, "--sandbox workspace-write -c model_reasoning_effort=low"; got != want {
					t.Errorf("builder[1].Opts = %q, want %q", got, want)
				}
				if got, want := builder[2].Backend, "claude-native"; got != want {
					t.Errorf("builder[2].Backend = %q, want %q", got, want)
				}

				planning, ok := cfg.Roles["ta-go-planning"]
				if !ok {
					t.Fatalf("Roles missing key ta-go-planning; have %v", keysOf(cfg.Roles))
				}
				if got, want := len(planning), 2; got != want {
					t.Fatalf("ta-go-planning tier count = %d, want %d", got, want)
				}
				if got, want := planning[0].Backend, "codex-exec"; got != want {
					t.Errorf("planning[0].Backend = %q, want %q", got, want)
				}
				if got, want := planning[1].Model, "opus"; got != want {
					t.Errorf("planning[1].Model = %q, want %q", got, want)
				}
			},
		},
		{
			name: "malformed toml",
			input: `
[chains
"ta-go-builder" = [ { backend = "ollama-local" } ]
`,
			wantErr:   true,
			errSubstr: "decode toml",
		},
		{
			name: "unknown top level field rejected",
			input: `
unexpected_root_key = "nope"

[chains]
"ta-go-builder" = [
  { backend = "ollama-local", model = "qwen2.5-coder:7b", opts = "", wait_max = 0, slots = 0 },
]
`,
			wantErr:   true,
			errSubstr: "unknown fields",
		},
		{
			name: "unknown tier field rejected",
			input: `
[chains]
"ta-go-builder" = [
  { backend = "ollama-local", model = "qwen2.5-coder:7b", opts = "", wait_max = 0, slots = 0, mystery = "x" },
]
`,
			wantErr:   true,
			errSubstr: "unknown fields",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse() error = nil, want error containing %q", tc.errSubstr)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("Parse() error = %q, want substring %q", err.Error(), tc.errSubstr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Parse() unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

// keysOf returns the keys of a map[string][]Tier as a sorted-ish slice for
// diagnostic output. Order is not stable; this is only used inside t.Fatalf.
func keysOf(m map[string][]Tier) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestConfigChain exercises Config.Chain across the acceptance criteria for
// drop_002.drop.droplet_chains_lookup:
//
//   - known role returns the configured tier slice in order.
//   - unknown role returns an error satisfying errors.Is(err, ErrUnknownRole).
//   - empty-string role is treated as unknown.
//   - nil-Roles receiver returns ErrUnknownRole rather than panicking.
//
// The lookup-against-fixture case lives in TestParseFixture so the parse +
// lookup integration path has its own dedicated test name.
func TestConfigChain(t *testing.T) {
	t.Parallel()

	builderTiers := []Tier{
		{Backend: "ollama-local", Model: "qwen2.5-coder:7b", Opts: "", WaitMax: 20, Slots: 4},
		{Backend: "codex-exec", Model: "gpt-5.5", Opts: "--sandbox workspace-write", WaitMax: 0, Slots: 0},
	}
	planningTiers := []Tier{
		{Backend: "codex-exec", Model: "gpt-5.5", Opts: "--sandbox read-only", WaitMax: 0, Slots: 0},
	}
	cfg := Config{Roles: map[string][]Tier{
		"ta-go-builder":  builderTiers,
		"ta-go-planning": planningTiers,
	}}

	tests := []struct {
		name      string
		cfg       Config
		role      string
		wantTiers []Tier
		wantErrIs error
	}{
		{
			name:      "known role builder",
			cfg:       cfg,
			role:      "ta-go-builder",
			wantTiers: builderTiers,
		},
		{
			name:      "known role planning",
			cfg:       cfg,
			role:      "ta-go-planning",
			wantTiers: planningTiers,
		},
		{
			name:      "unknown role returns sentinel",
			cfg:       cfg,
			role:      "ta-go-mystery",
			wantErrIs: ErrUnknownRole,
		},
		{
			name:      "empty role string is unknown",
			cfg:       cfg,
			role:      "",
			wantErrIs: ErrUnknownRole,
		},
		{
			name:      "nil roles map returns sentinel",
			cfg:       Config{},
			role:      "ta-go-builder",
			wantErrIs: ErrUnknownRole,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.cfg.Chain(tc.role)

			if tc.wantErrIs != nil {
				if err == nil {
					t.Fatalf("Chain(%q) error = nil, want errors.Is %v", tc.role, tc.wantErrIs)
				}
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("Chain(%q) error = %v, want errors.Is %v", tc.role, err, tc.wantErrIs)
				}
				if got != nil {
					t.Errorf("Chain(%q) tiers = %v on error, want nil", tc.role, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("Chain(%q) unexpected error: %v", tc.role, err)
			}
			if len(got) != len(tc.wantTiers) {
				t.Fatalf("Chain(%q) tier count = %d, want %d", tc.role, len(got), len(tc.wantTiers))
			}
			for i := range tc.wantTiers {
				if got[i] != tc.wantTiers[i] {
					t.Errorf("Chain(%q) tier[%d] = %+v, want %+v", tc.role, i, got[i], tc.wantTiers[i])
				}
			}
		})
	}
}

// TestParseFixture round-trips internal/chains/testdata/chains.toml through
// Parse and then exercises Config.Chain against the fixture roles. This is
// the fixture-backed integration path explicitly deferred from TestParse to
// the lookup droplet per drop_002.drop.droplet_chains_lookup acceptance.
func TestParseFixture(t *testing.T) {
	t.Parallel()

	path := filepath.Join("testdata", "chains.toml")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })

	cfg, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse(%s) unexpected error: %v", path, err)
	}

	wantRoles := []string{"ta-go-builder", "ta-go-planning", "ta-go-qa-falsification"}
	if got, want := len(cfg.Roles), len(wantRoles); got != want {
		t.Fatalf("fixture role count = %d (%v), want %d (%v)", got, keysOf(cfg.Roles), want, wantRoles)
	}

	for _, role := range wantRoles {
		tiers, err := cfg.Chain(role)
		if err != nil {
			t.Errorf("Chain(%q) unexpected error: %v", role, err)
			continue
		}
		if len(tiers) == 0 {
			t.Errorf("Chain(%q) returned empty tier slice", role)
		}
	}

	builderTiers, err := cfg.Chain("ta-go-builder")
	if err != nil {
		t.Fatalf("Chain(ta-go-builder) unexpected error: %v", err)
	}
	if got, want := len(builderTiers), 3; got != want {
		t.Fatalf("fixture ta-go-builder tier count = %d, want %d", got, want)
	}
	if got, want := builderTiers[0].Backend, "ollama-local"; got != want {
		t.Errorf("fixture ta-go-builder[0].Backend = %q, want %q", got, want)
	}
	if got, want := builderTiers[0].Model, "qwen2.5-coder:7b"; got != want {
		t.Errorf("fixture ta-go-builder[0].Model = %q, want %q", got, want)
	}
	if got, want := builderTiers[0].WaitMax, 20; got != want {
		t.Errorf("fixture ta-go-builder[0].WaitMax = %d, want %d", got, want)
	}
	if got, want := builderTiers[0].Slots, 4; got != want {
		t.Errorf("fixture ta-go-builder[0].Slots = %d, want %d", got, want)
	}
	if got, want := builderTiers[2].Backend, "claude-native"; got != want {
		t.Errorf("fixture ta-go-builder[2].Backend = %q, want %q", got, want)
	}

	if _, err := cfg.Chain("ta-go-nonexistent"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("Chain(unknown) error = %v, want errors.Is ErrUnknownRole", err)
	}
}
