// Tests for the backends template engine. Pins three load-bearing
// properties documented in drop_011 L3 planner amendments:
//
//  1. Happy-path substitution renders every TemplateData field plus the
//     custom {{env "VAR"}} func against a known input/output pair.
//  2. A reference to a field name absent from TemplateData triggers a
//     *template.ExecError (NOT silent "<no value>" output) because
//     compile() sets Option("missingkey=error") explicitly.
//  3. The env lookup is dependency-injected: the func argument receives
//     the lookup, NOT os.Getenv. The injection test sets a real OS env
//     var to one value and a fake env func to a different value and
//     asserts the fake's value reached the rendered output.
//
// Plus slice form coverage and a nil-env defaulting check.
package backends

import (
	"errors"
	"strings"
	"testing"
	"text/template"
)

// fakeEnv returns a func(string) string that records every key looked up
// and answers from a fixed map. Used to assert dependency injection.
func fakeEnv(answers map[string]string) (func(string) string, *[]string) {
	var seen []string
	lookup := func(k string) string {
		seen = append(seen, k)
		return answers[k]
	}
	return lookup, &seen
}

// TestSubstituteHappyPath renders every TemplateData field plus an env
// lookup and verifies the resulting string is byte-for-byte the
// expected value. This is the primary success-path certificate.
func TestSubstituteHappyPath(t *testing.T) {
	t.Parallel()

	data := TemplateData{
		Model:               "haiku",
		CWD:                 "/Users/ev/code/sand",
		PersonaBody:         "PERSONA_BODY_PLACEHOLDER",
		PersonaToolsCSV:     "Read,Edit,Bash",
		PersonaToolNamesCSV: "Read,Edit,Bash",
		McpConfigPath:       "/Users/ev/code/sand/.mcp.json",
		Role:                "ta-go-builder",
	}
	env, _ := fakeEnv(map[string]string{"OLLAMA_API_KEY": "secret-token"})

	tmplStr := `model={{.Model}} cwd={{.CWD}} body={{.PersonaBody}} ` +
		`tools={{.PersonaToolsCSV}} toolnames={{.PersonaToolNamesCSV}} mcp={{.McpConfigPath}} role={{.Role}} ` +
		`key={{env "OLLAMA_API_KEY"}}`

	got, err := Substitute(tmplStr, data, env)
	if err != nil {
		t.Fatalf("Substitute returned unexpected error: %v", err)
	}

	want := `model=haiku cwd=/Users/ev/code/sand body=PERSONA_BODY_PLACEHOLDER ` +
		`tools=Read,Edit,Bash toolnames=Read,Edit,Bash mcp=/Users/ev/code/sand/.mcp.json role=ta-go-builder ` +
		`key=secret-token`
	if got != want {
		t.Errorf("Substitute output mismatch:\n got:  %q\n want: %q", got, want)
	}
}

// TestSubstituteMissingKeyTriggersExecError verifies that the
// Option("missingkey=error") policy is wired EXPLICITLY: a template
// referencing a field absent from TemplateData must fail with a
// *template.ExecError, not silently render "<no value>". This is the
// planner's L2-T3 amendment in test form.
func TestSubstituteMissingKeyTriggersExecError(t *testing.T) {
	t.Parallel()

	data := TemplateData{Model: "haiku"}
	tmplStr := `model={{.Model}} bogus={{.Bogus}}`

	got, err := Substitute(tmplStr, data, nil)
	if err == nil {
		t.Fatalf("Substitute with bogus key returned no error; got %q", got)
	}
	if strings.Contains(got, "<no value>") {
		t.Errorf("Substitute fell back to <no value> sentinel; got %q", got)
	}

	var execErr template.ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("errors.As(err, *template.ExecError) = false; err = %v (type %T)", err, err)
	}
	if execErr.Name == "" {
		t.Errorf("ExecError.Name is empty; want the template's registered name")
	}
}

// TestSubstituteEnvIsDependencyInjected proves the env arg is the
// lookup func actually called, NOT os.Getenv. The OS env var is set
// to a sentinel value the test never expects to see; the fake env
// func returns a different value that MUST appear in the rendered
// output.
func TestSubstituteEnvIsDependencyInjected(t *testing.T) {
	const (
		envKey     = "SAND_BACKENDS_TEMPLATE_TEST_VAR"
		osValue    = "FROM_OS_GETENV_DO_NOT_USE"
		fakeValue  = "FROM_INJECTED_FAKE_USE_THIS"
		bogusValue = "<no value>"
	)

	t.Setenv(envKey, osValue)

	env, seen := fakeEnv(map[string]string{envKey: fakeValue})
	tmplStr := `value={{env "` + envKey + `"}}`

	got, err := Substitute(tmplStr, TemplateData{}, env)
	if err != nil {
		t.Fatalf("Substitute returned unexpected error: %v", err)
	}

	if !strings.Contains(got, fakeValue) {
		t.Errorf("rendered output missing injected fake value: got=%q want substring %q", got, fakeValue)
	}
	if strings.Contains(got, osValue) {
		t.Errorf("rendered output contains os.Getenv value, proving direct os.Getenv use: got=%q", got)
	}
	if strings.Contains(got, bogusValue) {
		t.Errorf("rendered output contains <no value> sentinel: got=%q", got)
	}

	if len(*seen) != 1 || (*seen)[0] != envKey {
		t.Errorf("fake env func saw keys=%v; want exactly [%q]", *seen, envKey)
	}
}

// TestSubstituteNilEnvDefaultsToEmpty pins the nil-env contract: when
// the caller passes a nil env func, {{env "X"}} renders the empty
// string instead of panicking on a nil-func call.
func TestSubstituteNilEnvDefaultsToEmpty(t *testing.T) {
	t.Parallel()

	got, err := Substitute(`prefix={{env "ANY"}}suffix`, TemplateData{}, nil)
	if err != nil {
		t.Fatalf("Substitute returned unexpected error: %v", err)
	}
	if got != "prefix=suffix" {
		t.Errorf("nil-env default mismatch: got=%q want=%q", got, "prefix=suffix")
	}
}

// TestSubstituteSliceHappyPath renders every element of an args slice
// independently through Substitute and verifies the output preserves
// order, length, and per-element substitution.
func TestSubstituteSliceHappyPath(t *testing.T) {
	t.Parallel()

	data := TemplateData{
		Model: "opus",
		CWD:   "/tmp/proj",
		Role:  "ta-go-qa-proof",
	}
	env, _ := fakeEnv(map[string]string{"TOK": "abc123"})

	args := []string{
		"--model",
		"{{.Model}}",
		"-C",
		"{{.CWD}}",
		"--role={{.Role}}",
		`--token={{env "TOK"}}`,
		"plain-literal",
	}

	got, err := SubstituteSlice(args, data, env)
	if err != nil {
		t.Fatalf("SubstituteSlice returned unexpected error: %v", err)
	}

	want := []string{
		"--model",
		"opus",
		"-C",
		"/tmp/proj",
		"--role=ta-go-qa-proof",
		"--token=abc123",
		"plain-literal",
	}

	if len(got) != len(want) {
		t.Fatalf("SubstituteSlice len = %d; want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SubstituteSlice[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// TestSubstituteSliceMissingKey verifies a missing-key failure in any
// one slice element aborts the whole call and surfaces a
// *template.ExecError. The returned slice MUST be nil so callers
// cannot accidentally consume a partial render.
func TestSubstituteSliceMissingKey(t *testing.T) {
	t.Parallel()

	args := []string{
		"{{.Model}}",
		"{{.NotARealField}}",
		"never-reached",
	}

	got, err := SubstituteSlice(args, TemplateData{Model: "haiku"}, nil)
	if err == nil {
		t.Fatalf("SubstituteSlice with bogus key returned no error; got %v", got)
	}
	if got != nil {
		t.Errorf("SubstituteSlice on error returned non-nil slice: %v", got)
	}

	var execErr template.ExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("errors.As(err, *template.ExecError) = false; err = %v (type %T)", err, err)
	}
}

// TestSubstituteEmptyInputs ensures empty template strings and empty
// data render to empty output without spurious errors — the engine
// must tolerate the "no-op" case that arises when a backend omits a
// field entirely from its template.
func TestSubstituteEmptyInputs(t *testing.T) {
	t.Parallel()

	got, err := Substitute("", TemplateData{}, nil)
	if err != nil {
		t.Fatalf("Substitute(empty) returned error: %v", err)
	}
	if got != "" {
		t.Errorf("Substitute(empty) = %q; want empty string", got)
	}

	gotSlice, err := SubstituteSlice(nil, TemplateData{}, nil)
	if err != nil {
		t.Fatalf("SubstituteSlice(nil) returned error: %v", err)
	}
	if len(gotSlice) != 0 {
		t.Errorf("SubstituteSlice(nil) = %v; want zero-length slice", gotSlice)
	}
}
