package preflight

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/evanmschultz/sand/internal/chains"
)

// stubProbe is the deterministic Probe implementation used throughout this
// test file. Each field overrides one method; nil fields mean "the test
// did not configure this branch and should not exercise it".
//
// The stub records LookPath and HTTPGet inputs so tests can assert on which
// binaries / URLs Preflight actually probed. OllamaList intentionally does
// NOT record its input because the production call has no per-tier arg.
type stubProbe struct {
	lookPath   func(name string) (string, error)
	httpGet    func(ctx context.Context, url string) (*http.Response, error)
	ollamaList func(ctx context.Context) (string, error)

	lookPathCalls []string
	httpGetCalls  []string
}

func (s *stubProbe) LookPath(name string) (string, error) {
	s.lookPathCalls = append(s.lookPathCalls, name)
	if s.lookPath == nil {
		return "", errors.New("stubProbe.LookPath not configured")
	}
	return s.lookPath(name)
}

func (s *stubProbe) HTTPGet(ctx context.Context, url string) (*http.Response, error) {
	s.httpGetCalls = append(s.httpGetCalls, url)
	if s.httpGet == nil {
		return nil, errors.New("stubProbe.HTTPGet not configured")
	}
	return s.httpGet(ctx, url)
}

func (s *stubProbe) OllamaList(ctx context.Context) (string, error) {
	if s.ollamaList == nil {
		return "", errors.New("stubProbe.OllamaList not configured")
	}
	return s.ollamaList(ctx)
}

// httpRespOK returns a fake 200 response with a small JSON-like body. Used
// to simulate a live ollama daemon answering /api/version.
func httpRespOK(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// httpRespStatus returns a fake response with the given status code and an
// empty body. Used to simulate a daemon answering with a non-2xx.
func httpRespStatus(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
}

// TestPreflightHappyPath exercises the all-tiers-OK case for a builder-shaped
// chain: ollama-local (daemon up + model pulled), codex-exec, claude-native.
// It is the canonical positive control and protects against accidental
// regressions in the success-path Reason="" / OK=true wiring.
func TestPreflightHappyPath(t *testing.T) {
	t.Parallel()

	chain := []chains.Tier{
		{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
		{Backend: BackendCodexExec, Model: "gpt-5.5"},
		{Backend: BackendClaudeNative, Model: "haiku"},
	}

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: func(ctx context.Context, url string) (*http.Response, error) {
			return httpRespOK(`{"version":"0.1.34"}`), nil
		},
		ollamaList: func(ctx context.Context) (string, error) {
			return "NAME                        ID              SIZE\nqwen2.5-coder:7b            abc             4.7 GB\nllama3:8b                   def             4.7 GB\n", nil
		},
	}

	rep := Preflight(context.Background(), probe, "ta-go-builder", chain)

	if rep.Role != "ta-go-builder" {
		t.Errorf("Role = %q, want %q", rep.Role, "ta-go-builder")
	}
	if got, want := len(rep.Tiers), 3; got != want {
		t.Fatalf("len(Tiers) = %d, want %d", got, want)
	}
	for i, r := range rep.Tiers {
		if !r.OK {
			t.Errorf("Tiers[%d].OK = false, reason = %q; want OK=true", i, r.Reason)
		}
		if r.Reason != "" {
			t.Errorf("Tiers[%d].Reason = %q, want empty when OK", i, r.Reason)
		}
		if r.Tier != i+1 {
			t.Errorf("Tiers[%d].Tier = %d, want %d", i, r.Tier, i+1)
		}
	}
	if rep.Tiers[0].Backend != BackendOllamaLocal || rep.Tiers[0].Model != "qwen2.5-coder:7b" {
		t.Errorf("Tiers[0] = %+v, want ollama-local/qwen2.5-coder:7b", rep.Tiers[0])
	}
	if rep.Tiers[1].Backend != BackendCodexExec || rep.Tiers[1].Model != "gpt-5.5" {
		t.Errorf("Tiers[1] = %+v, want codex-exec/gpt-5.5", rep.Tiers[1])
	}
	if rep.Tiers[2].Backend != BackendClaudeNative || rep.Tiers[2].Model != "haiku" {
		t.Errorf("Tiers[2] = %+v, want claude-native/haiku", rep.Tiers[2])
	}

	// Verify the probe was asked about the right binaries and URL — guards
	// against accidental endpoint drift (e.g. /api/tags vs /api/version).
	if got := probe.httpGetCalls; len(got) != 1 || got[0] != ollamaVersionURL {
		t.Errorf("httpGet calls = %v, want exactly [%s]", got, ollamaVersionURL)
	}
	if got := probe.lookPathCalls; len(got) != 2 || got[0] != "codex" || got[1] != "claude" {
		t.Errorf("lookPath calls = %v, want [codex claude]", got)
	}
}

// TestPreflightTierFailures covers each failure mode the SAND-SPEC §3.2 row
// shape is supposed to surface: missing CLI, ollama daemon down, ollama
// model not pulled, ollama daemon non-2xx, unknown backend, empty model
// configured for an ollama-local tier.
//
// Each row is a one-tier chain so the failure reason is straightforward to
// assert against without index arithmetic.
func TestPreflightTierFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tier       chains.Tier
		probe      *stubProbe
		wantOK     bool
		wantSub    string // expected substring inside Reason
		wantBinary string // optional: expected LookPath call (single)
	}{
		{
			name: "claude-native missing CLI",
			tier: chains.Tier{Backend: BackendClaudeNative, Model: "opus"},
			probe: &stubProbe{
				lookPath: func(name string) (string, error) {
					return "", errors.New("executable file not found in $PATH")
				},
			},
			wantOK:     false,
			wantSub:    "claude CLI not on PATH",
			wantBinary: "claude",
		},
		{
			name: "codex-exec missing CLI",
			tier: chains.Tier{Backend: BackendCodexExec, Model: "gpt-5.5"},
			probe: &stubProbe{
				lookPath: func(name string) (string, error) {
					return "", errors.New("executable file not found in $PATH")
				},
			},
			wantOK:     false,
			wantSub:    "codex CLI not on PATH",
			wantBinary: "codex",
		},
		{
			name: "ollama daemon transport error",
			tier: chains.Tier{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
			probe: &stubProbe{
				httpGet: func(ctx context.Context, url string) (*http.Response, error) {
					return nil, errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
				},
			},
			wantOK:  false,
			wantSub: "ollama daemon unreachable",
		},
		{
			name: "ollama daemon non-2xx",
			tier: chains.Tier{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
			probe: &stubProbe{
				httpGet: func(ctx context.Context, url string) (*http.Response, error) {
					return httpRespStatus(503), nil
				},
			},
			wantOK:  false,
			wantSub: "status 503",
		},
		{
			name: "ollama model not pulled",
			tier: chains.Tier{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
			probe: &stubProbe{
				httpGet: func(ctx context.Context, url string) (*http.Response, error) {
					return httpRespOK(`{"version":"0.1.34"}`), nil
				},
				ollamaList: func(ctx context.Context) (string, error) {
					// Only some other model is present locally.
					return "NAME            ID      SIZE\nllama3:8b       def     4.7 GB\n", nil
				},
			},
			wantOK:  false,
			wantSub: `model "qwen2.5-coder:7b" not pulled locally`,
		},
		{
			name: "ollama list errored",
			tier: chains.Tier{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
			probe: &stubProbe{
				httpGet: func(ctx context.Context, url string) (*http.Response, error) {
					return httpRespOK(`{"version":"0.1.34"}`), nil
				},
				ollamaList: func(ctx context.Context) (string, error) {
					return "", errors.New("ollama binary not found")
				},
			},
			wantOK:  false,
			wantSub: "ollama list failed",
		},
		{
			name: "ollama-local empty model field",
			tier: chains.Tier{Backend: BackendOllamaLocal, Model: ""},
			probe: &stubProbe{
				httpGet: func(ctx context.Context, url string) (*http.Response, error) {
					return httpRespOK(`{"version":"0.1.34"}`), nil
				},
				// OllamaList must NOT be called once the empty-model
				// short-circuit fires; leaving it nil asserts that.
			},
			wantOK:  false,
			wantSub: "no model configured",
		},
		{
			name:  "unknown backend",
			tier:  chains.Tier{Backend: "azure-mystery", Model: "gpt-99"},
			probe: &stubProbe{
				// All probe fields nil — an unknown backend must NOT
				// touch any of them. If it does, the stub's "not
				// configured" errors will surface in Reason.
			},
			wantOK:  false,
			wantSub: `unknown backend "azure-mystery"`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rep := Preflight(context.Background(), tc.probe, "role-x", []chains.Tier{tc.tier})
			if rep.Role != "role-x" {
				t.Errorf("Role = %q, want %q", rep.Role, "role-x")
			}
			if got, want := len(rep.Tiers), 1; got != want {
				t.Fatalf("len(Tiers) = %d, want %d", got, want)
			}
			row := rep.Tiers[0]
			if row.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v; reason = %q", row.OK, tc.wantOK, row.Reason)
			}
			if !strings.Contains(row.Reason, tc.wantSub) {
				t.Errorf("Reason = %q, want substring %q", row.Reason, tc.wantSub)
			}
			if row.Tier != 1 {
				t.Errorf("Tier index = %d, want 1", row.Tier)
			}
			if row.Backend != tc.tier.Backend {
				t.Errorf("Backend = %q, want %q", row.Backend, tc.tier.Backend)
			}
			if row.Model != tc.tier.Model {
				t.Errorf("Model = %q, want %q", row.Model, tc.tier.Model)
			}
			if tc.wantBinary != "" {
				if got := tc.probe.lookPathCalls; len(got) != 1 || got[0] != tc.wantBinary {
					t.Errorf("lookPath calls = %v, want [%s]", got, tc.wantBinary)
				}
			}
		})
	}
}

// TestPreflightEmptyChain verifies the nil/empty-chain edge case: Preflight
// must return a populated Report with the role and an empty (non-nil) Tiers
// slice so downstream TOON encoding emits `tiers[0]{...}:`.
func TestPreflightEmptyChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		chain []chains.Tier
	}{
		{name: "nil chain", chain: nil},
		{name: "zero-length chain", chain: []chains.Tier{}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			probe := &stubProbe{} // no methods should be called
			rep := Preflight(context.Background(), probe, "ta-go-builder", tc.chain)

			if rep.Role != "ta-go-builder" {
				t.Errorf("Role = %q, want %q", rep.Role, "ta-go-builder")
			}
			if rep.Tiers == nil {
				t.Errorf("Tiers = nil, want empty non-nil slice")
			}
			if len(rep.Tiers) != 0 {
				t.Errorf("len(Tiers) = %d, want 0", len(rep.Tiers))
			}
			if len(probe.lookPathCalls) != 0 || len(probe.httpGetCalls) != 0 {
				t.Errorf("probe was called on empty chain: lookPath=%v httpGet=%v",
					probe.lookPathCalls, probe.httpGetCalls)
			}
		})
	}
}

// TestPreflightTiersIndependent guards against accidental short-circuit
// behavior: a failing earlier tier must NOT prevent later tiers from being
// probed. This is the inverse of dispatch's "advance on failure" — preflight
// reports EVERY tier, succeed or fail.
func TestPreflightTiersIndependent(t *testing.T) {
	t.Parallel()

	chain := []chains.Tier{
		// Tier 1: ollama daemon down.
		{Backend: BackendOllamaLocal, Model: "qwen2.5-coder:7b"},
		// Tier 2: codex CLI present.
		{Backend: BackendCodexExec, Model: "gpt-5.5"},
		// Tier 3: claude CLI present.
		{Backend: BackendClaudeNative, Model: "opus"},
	}

	probe := &stubProbe{
		lookPath: func(name string) (string, error) {
			return "/usr/local/bin/" + name, nil
		},
		httpGet: func(ctx context.Context, url string) (*http.Response, error) {
			return nil, errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
		},
	}

	rep := Preflight(context.Background(), probe, "ta-go-planning", chain)
	if got, want := len(rep.Tiers), 3; got != want {
		t.Fatalf("len(Tiers) = %d, want %d", got, want)
	}
	if rep.Tiers[0].OK {
		t.Errorf("Tier 1 OK = true, want false")
	}
	if !rep.Tiers[1].OK {
		t.Errorf("Tier 2 OK = false (reason %q), want true", rep.Tiers[1].Reason)
	}
	if !rep.Tiers[2].OK {
		t.Errorf("Tier 3 OK = false (reason %q), want true", rep.Tiers[2].Reason)
	}
}

// TestHasOllamaModelFirstColumnOnly protects the model-match logic from
// substring drift: `qwen2.5-coder:7b-instruct` must not match a chain entry
// for `qwen2.5-coder:7b`.
func TestHasOllamaModelFirstColumnOnly(t *testing.T) {
	t.Parallel()

	const list = `NAME                              ID      SIZE
qwen2.5-coder:7b-instruct         abc     4.7 GB
llama3:8b                         def     4.7 GB
`

	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "exact first-column hit", model: "qwen2.5-coder:7b-instruct", want: true},
		{name: "prefix-only no match", model: "qwen2.5-coder:7b", want: false},
		{name: "absent model no match", model: "phi3:14b", want: false},
		{name: "empty model never matches", model: "", want: false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasOllamaModel(list, tc.model); got != tc.want {
				t.Errorf("hasOllamaModel(.., %q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
