// Package backends owns the user-configurable backend template surface
// described in SAND-SPEC §5. This file is the templating engine that
// drives backend command rendering from `~/.config/sand/backends.toml`
// entries.
//
// The engine wraps Go's stdlib `text/template` with one load-bearing
// configuration choice: `tmpl.Option("missingkey=error")` is set
// EXPLICITLY so a typo in a user's backends.toml fails loudly with a
// `template.ExecError` instead of silently rendering `<no value>` into
// an os/exec argv. Per drop_011 L3 planner amendment (L2 T3), this
// fail-fast posture is the safety inference the planner imposed on top
// of the spec.
//
// The custom `{{env "VAR"}}` func takes a dependency-injected env lookup
// (NOT direct `os.Getenv`) so tests can verify the injection seam stays
// clean. Production callers pass `os.Getenv`; tests pass a fake.
package backends

import (
	"bytes"
	"fmt"
	"text/template"
)

// TemplateData is the substitution surface every backend template renders
// against. Fields cover the SAND-SPEC §5.4 inputs needed by the
// committed claude-native spawn behavior (model, cwd, persona body,
// persona tools CSV, mcp config path, role).
//
// All fields are strings; empty string is a VALID value (it is not a
// "missing key" — the `missingkey=error` Option only fires when the
// template references a field name that does not exist on this struct).
type TemplateData struct {
	// Model is the chain tier's model identifier (e.g. "haiku", "opus").
	Model string

	// CWD is the caller project's absolute path — passed to the spawned
	// subprocess as its working directory and exposed to templates for
	// backends like codex that take `-C <cwd>` as a flag.
	CWD string

	// PersonaBody is the loaded persona's system-prompt body (markdown
	// after the YAML frontmatter), typically injected via
	// `--append-system-prompt`.
	PersonaBody string

	// PersonaToolsCSV is the persona's Tools slice joined with `,`,
	// typically injected via `--allowedTools`.
	PersonaToolsCSV string

	// McpConfigPath is the caller project's `.mcp.json` path when it
	// exists, otherwise empty. Backends that wire MCP via a `--mcp-config`
	// flag use this; backends that inject MCP inline (codex) ignore it.
	McpConfigPath string

	// Role is the persona role identifier (e.g. "ta-go-builder"). Useful
	// for backends that tag spawns by role in logs or metrics.
	Role string

	// PersonaToolNamesCSV is the persona's Tools slice joined with `,
	// but with the scoped patterns stripped to bare names (e.g. "Read",
	// "Edit", "Bash" instead of "Bash(mage *)" to support future
	// --tools CSV rendering without leaking scoped patterns).
	PersonaToolNamesCSV string
}

// Substitute renders a single template string against `data` with the
// custom `{{env "VAR"}}` func wired to `env`. Returns the rendered
// string or a non-nil error.
//
// Missing-key behavior: any reference to a field name that does not
// exist on TemplateData (e.g. `{{.Bogus}}`) returns a
// `*template.ExecError` wrapped in the returned error — surface via
// `errors.As`. This is enforced by `tmpl.Option("missingkey=error")`
// set explicitly on the compiled template.
//
// Env injection: the `env` argument MUST be the lookup function used
// by `{{env "VAR"}}`. If `env` is nil, lookups return the empty string
// (so `{{env "OPTIONAL_VAR"}}` is safe in a default-empty configuration
// without a panicking nil-deref). Production callers pass `os.Getenv`.
func Substitute(tmplStr string, data TemplateData, env func(string) string) (string, error) {
	tmpl, err := compile(tmplStr, env)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("backends: execute template: %w", err)
	}
	return buf.String(), nil
}

// SubstituteSlice applies Substitute to every element of `args` and
// returns the resulting slice. Element-wise rendering means each arg
// is treated as an independent template; a missing-key failure in any
// element aborts the call and surfaces the underlying ExecError.
//
// Returns a freshly allocated slice on success (never aliasing the
// input). On error the returned slice is nil.
func SubstituteSlice(args []string, data TemplateData, env func(string) string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		rendered, err := Substitute(a, data, env)
		if err != nil {
			return nil, fmt.Errorf("backends: substitute arg %d: %w", i, err)
		}
		out[i] = rendered
	}
	return out, nil
}

// compile parses `tmplStr` with the `env` FuncMap and the missing-key
// fail-fast Option both wired BEFORE the parse so the parser sees the
// custom func and the executor honors the strict missing-key policy.
//
// Order matters: `Option` and `Funcs` MUST be called on the *Template
// before Parse — otherwise Parse rejects unknown function names like
// `env`. This is the load-bearing reason compile exists as a shared
// helper between Substitute and SubstituteSlice.
func compile(tmplStr string, env func(string) string) (*template.Template, error) {
	if env == nil {
		env = func(string) string { return "" }
	}

	tmpl := template.New("backend-template").
		Option("missingkey=error").
		Funcs(template.FuncMap{
			"env": env,
		})

	parsed, err := tmpl.Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("backends: parse template: %w", err)
	}
	return parsed, nil
}
