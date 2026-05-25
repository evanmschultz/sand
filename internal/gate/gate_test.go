package gate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sbx = "/tmp/sbx"

var (
	allowedFile = filepath.Join(sbx, "allowed.txt")
	forbidFile  = filepath.Join(sbx, "forbidden.txt")
)

// builderAllow mirrors the builder allowlist used in the python head-to-head:
// may edit allowed.txt only; git mutation + dep mutation forbidden.
func builderAllow() *Allowlist {
	return &Allowlist{
		Edit:        []string{allowedFile},
		EditPresent: true,
		BashDeny:    []string{"git commit", "git push", "git add", "mage install", "go get", "go mod"},
	}
}

func bashEval(t *testing.T, command string, a *Allowlist) Decision {
	t.Helper()
	in := preToolUseInput{ToolName: "Bash", AgentID: "a1", AgentType: "ta-go-builder", CWD: sbx}
	in.ToolInput.Command = command
	return Evaluate(in, a)
}

func editEval(t *testing.T, fp string, a *Allowlist) Decision {
	t.Helper()
	in := preToolUseInput{ToolName: "Write", AgentID: "a1", AgentType: "ta-go-builder", CWD: sbx}
	in.ToolInput.FilePath = fp
	return Evaluate(in, a)
}

// TestParity_HeadToHead replays the exact 15 vectors from /tmp/gate_compare.py.
// The python gate scored 0 holes here; the Go gate must match every one (the
// bash gate had 6 holes on the git -C / shell-write vectors — those are the
// rows this asserts DENY on).
func TestParity_HeadToHead(t *testing.T) {
	a := builderAllow()
	cases := []struct {
		name    string
		command string // empty => edit case via file
		file    string // set for edit cases
		want    Action
	}{
		{"git commit literal", "git commit -m x", "", ActionDeny},
		{"git -C dir commit (bash-gate hole)", "git -C /tmp/sbx commit -m x", "", ActionDeny},
		{"FOO=1 git commit", "FOO=1 git commit -m x", "", ActionDeny},
		{"/usr/bin/git commit", "/usr/bin/git commit -m x", "", ActionDeny},
		{"git --git-dir commit", "git --git-dir=/tmp/sbx/.git commit -m x", "", ActionDeny},
		{"echo > forbidden (hole)", "echo pwned > /tmp/sbx/forbidden.txt", "", ActionDeny},
		{"python3 -c write (hole)", `python3 -c "open('/tmp/sbx/forbidden.txt','w').write('x')"`, "", ActionDeny},
		{"tee (hole)", "echo x | tee /tmp/sbx/forbidden.txt", "", ActionDeny},
		{"sed -i (hole)", "sed -i 's/a/b/' /tmp/sbx/forbidden.txt", "", ActionDeny},
		{"cp (hole)", "cp /tmp/sbx/allowed.txt /tmp/sbx/forbidden.txt", "", ActionDeny},
		{"cat read", "cat /tmp/sbx/allowed.txt", "", ActionAllow},
		{"mage testPkg", "mage testPkg ./internal/x", "", ActionAllow},
		{"git status read", "git status", "", ActionAllow},
		{"edit off-scope", "", forbidFile, ActionDeny},
		{"edit in-scope", "", allowedFile, ActionAllow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got Decision
			if c.file != "" {
				got = editEval(t, c.file, a)
			} else {
				got = bashEval(t, c.command, a)
			}
			if got.Action != c.want {
				t.Fatalf("got %s, want %s (reason: %s)", got.Action, c.want, got.Reason)
			}
		})
	}
}

// TestFalsification_ResidualHoles replays /tmp/gate_falsify.py. The python gate
// had 3 holes here (sh -c 'cp', gawk -i inplace, ed/ex). The Go gate must close
// all three while keeping the legit reads/builds ALLOW.
func TestFalsification_ResidualHoles(t *testing.T) {
	a := builderAllow()
	cases := []struct {
		name    string
		command string
		want    Action
	}{
		{"bash -c redirect", `bash -c "echo x > /tmp/sbx/forbidden.txt"`, ActionDeny},
		{"sh -c cp (python hole)", `sh -c 'cp /tmp/sbx/allowed.txt /tmp/sbx/forbidden.txt'`, ActionDeny},
		{"gawk -i inplace (python hole)", "gawk -i inplace '{print}' /tmp/sbx/forbidden.txt", ActionDeny},
		{"ed (python hole)", "printf '1d\\nw\\n' | ed /tmp/sbx/forbidden.txt", ActionDeny},
		{"ex (python hole)", "ex -sc 'wq' /tmp/sbx/forbidden.txt", ActionDeny},
		{"clobber redirect", "echo x >| /tmp/sbx/forbidden.txt", ActionDeny},
		{"2>/dev/null legit", "mage testPkg ./x 2>/dev/null", ActionAllow},
		{"> /dev/null legit", "ls /tmp/sbx > /dev/null", ActionAllow},
		{"git reset not in deny", "git reset --hard", ActionAllow},
		{"git -C dir push", "git -C /tmp/sbx push origin main", ActionDeny},
		{"chained git commit", "cat /tmp/sbx/allowed.txt && git commit -m x", ActionDeny},
		{"install binary", "install -m 755 /tmp/sbx/allowed.txt /tmp/sbx/forbidden.txt", ActionDeny},
		{"truncate", "truncate -s 0 /tmp/sbx/forbidden.txt", ActionDeny},
		{"xargs cp", "echo f | xargs -I{} cp /tmp/sbx/allowed.txt {}", ActionDeny},
		{"printf no-redirect", "printf 'hello'", ActionAllow},
		{"grep -i read (no false deny)", "grep -i pattern /tmp/sbx/allowed.txt", ActionAllow},
		{"go doc read", "go doc fmt.Println", ActionAllow},
		{"mage install denied", "mage install", ActionDeny},
		{"go get denied", "go get github.com/x/y", ActionDeny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bashEval(t, c.command, a)
			if got.Action != c.want {
				t.Fatalf("got %s, want %s (reason: %s)", got.Action, c.want, got.Reason)
			}
		})
	}
}

// TestQAEditsNothing: a QA role gets edit:[] (present, empty). Every edit is
// denied AND every shell-write vector is denied, but legit reads pass.
func TestQAEditsNothing(t *testing.T) {
	qa := &Allowlist{Edit: []string{}, EditPresent: true, BashDeny: []string{"git commit", "git push"}}
	if d := editEval(t, allowedFile, qa); d.Action != ActionDeny {
		t.Fatalf("QA edit should be denied, got %s", d.Action)
	}
	if d := bashEval(t, "echo x > /tmp/sbx/anything.go", qa); d.Action != ActionDeny {
		t.Fatalf("QA shell-write should be denied, got %s", d.Action)
	}
	if d := bashEval(t, "git diff HEAD", qa); d.Action != ActionAllow {
		t.Fatalf("QA git diff (read) should be allowed, got %s", d.Action)
	}
}

func TestGitSubcommand(t *testing.T) {
	cases := []struct {
		tokens []string
		want   string
		ok     bool
	}{
		{[]string{"git", "commit"}, "commit", true},
		{[]string{"git", "-C", "/d", "commit"}, "commit", true},
		{[]string{"FOO=1", "BAR=2", "git", "push"}, "push", true},
		{[]string{"/usr/bin/git", "commit"}, "commit", true},
		{[]string{"git", "--git-dir=/x", "commit"}, "commit", true},
		{[]string{"git", "-c", "user.name=x", "commit"}, "commit", true},
		{[]string{"git", "status"}, "status", true},
		{[]string{"cat", "file"}, "", false},
		{[]string{"git"}, "", false},
	}
	for _, c := range cases {
		got, ok := gitSubcommand(c.tokens)
		if got != c.want || ok != c.ok {
			t.Errorf("gitSubcommand(%v) = (%q,%v), want (%q,%v)", c.tokens, got, ok, c.want, c.ok)
		}
	}
}

func TestEditAllowed_Normalization(t *testing.T) {
	allowed := []string{"/tmp/sbx/allowed.txt"}
	// relative path joined onto cwd matches the absolute entry
	if !editAllowed("allowed.txt", allowed, "/tmp/sbx") {
		t.Error("relative path should normalize to the allowed absolute path")
	}
	// .. traversal that resolves to the allowed file matches
	if !editAllowed("/tmp/sbx/sub/../allowed.txt", allowed, "/tmp/sbx") {
		t.Error("normalized traversal to the allowed file should match")
	}
	if editAllowed("/tmp/sbx/forbidden.txt", allowed, "/tmp/sbx") {
		t.Error("a different file must not match")
	}
}

// TestRun_DeferUngated: no env allowlist + no agent_id => orchestrator/main
// session => defer (no output, exit 0).
func TestRun_DeferUngated(t *testing.T) {
	in := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git commit -m x"}}
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	code := Run(bytes.NewReader(b), &out, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("ungated main session must defer (no output), got %q", out.String())
	}
}

// TestRun_EnvAllowlistDeny: env-delivered allowlist gates a git commit to deny
// with a permissionDecision envelope.
func TestRun_EnvAllowlistDeny(t *testing.T) {
	in := map[string]any{
		"tool_name":  "Bash",
		"agent_id":   "a1",
		"agent_type": "ta-go-builder",
		"cwd":        sbx,
		"tool_input": map[string]any{"command": "git -C /tmp/sbx commit -m x"},
	}
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	env := func(k string) string {
		if k == "TA_GATE_ALLOWLIST" {
			return `{"edit":["/tmp/sbx/allowed.txt"],"bash_deny":["git commit"]}`
		}
		return ""
	}
	code := Run(bytes.NewReader(b), &out, env)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var got struct {
		HSO struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal output %q: %v", out.String(), err)
	}
	if got.HSO.Decision != "deny" {
		t.Fatalf("decision = %q, want deny", got.HSO.Decision)
	}
}

// TestResolveFromTranscript scans a synthetic parent transcript for the
// <TA_ALLOWLIST> block keyed by subagent_type, last-wins.
func TestResolveFromTranscript(t *testing.T) {
	dir := t.TempDir()
	tp := filepath.Join(dir, "transcript.jsonl")
	mkline := func(subagent, prompt string) string {
		evt := map[string]any{
			"message": map[string]any{
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"name":  "Agent",
						"input": map[string]any{"subagent_type": subagent, "prompt": prompt},
					},
				},
			},
		}
		b, _ := json.Marshal(evt)
		return string(b)
	}
	lines := []string{
		mkline("ta-go-builder", "do work <TA_ALLOWLIST>{\"edit\":[\"/tmp/a.go\"],\"bash_deny\":[\"git commit\"]}</TA_ALLOWLIST>"),
		mkline("ta-go-plan-qa-proof", "qa <TA_ALLOWLIST>{\"edit\":[]}</TA_ALLOWLIST>"),
		mkline("ta-go-builder", "newer work <TA_ALLOWLIST>{\"edit\":[\"/tmp/b.go\"],\"bash_deny\":[\"git push\"]}</TA_ALLOWLIST>"),
	}
	if err := os.WriteFile(tp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := ResolveFromTranscript(tp, "ta-go-builder")
	if a == nil {
		t.Fatal("expected an allowlist, got nil")
	}
	if len(a.Edit) != 1 || a.Edit[0] != "/tmp/b.go" {
		t.Fatalf("last-wins failed: edit = %v, want [/tmp/b.go]", a.Edit)
	}
	if ResolveFromTranscript(tp, "no-such-role") != nil {
		t.Error("unmatched agent_type must resolve to nil")
	}
}

func TestParseAllowlist_EditPresence(t *testing.T) {
	a, err := ParseAllowlist([]byte(`{"edit":[],"bash_deny":["git commit"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !a.EditPresent {
		t.Error("empty edit list must still mark EditPresent (QA shell-write block)")
	}
	a2, _ := ParseAllowlist([]byte(`{"bash_deny":["git commit"]}`))
	if a2.EditPresent {
		t.Error("absent edit key must leave EditPresent false")
	}
}
