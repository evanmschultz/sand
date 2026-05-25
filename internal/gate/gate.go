// Package gate implements the PreToolUse permission gate that confines a
// dispatched subagent to the allowlist the orchestrator passed AT DISPATCH
// TIME, regardless of what the agent's prompt tells it to do.
//
// It is the Go, cross-OS end-state of the proven bin/sh reference hook
// (`.claude/hooks/ta_action_gate.py`, canonical in hylla/polyglot-foundation —
// see HYLLA_BIN.md §0/§5.1). The hook is wired as the `sand gate` subcommand
// (see cmd/sand/main.go): Claude Code invokes `sand gate` on every PreToolUse
// event, this package reads the event JSON on stdin and emits the
// permissionDecision envelope on stdout.
//
// Two invariants are enforced harness-level (prompt-proof):
//
//  1. GIT IS ORCHESTRATOR-ONLY — deny any git-mutation (and other bash_deny)
//     command, defeating the `git -C dir commit`, `FOO=1 git commit`,
//     `/usr/bin/git commit`, `git --git-dir=x commit` evasions a naive
//     substring match misses.
//  2. EDIT-SCOPE — an agent may Edit/Write/MultiEdit/NotebookEdit ONLY the
//     files in its granted `edit` list. QA roles get `edit:[]` => ALL edits
//     denied (QA NEVER edits files; it updates ta only, an MCP tool not gated
//     here). An edit-scoped agent additionally may NOT mutate files through the
//     shell (the `cat>`/`python -c`/`sed -i`/`tee`/`cp`/`sh -c 'cp …'` bypass).
//
// Improvements over the python reference (empirically falsified 2026-05-25,
// see internal/gate/gate_test.go):
//
//   - Shell-spawns (`sh`/`bash`/`zsh`/`dash`/`ksh`/`fish`/`env`/`xargs`) are
//     treated as interpreters that can write, closing the `sh -c 'cp a forbidden'`
//     hole (the python list omitted shells, and a quoted inner command sat
//     behind a `'` the python separator class did not recognize).
//   - The command-position separator class includes quote chars (`'` `"`) so a
//     quoted mutating sub-command is caught even without the shell-spawn rule.
//   - Stream/line editors with in-place flags (`gawk -i inplace`) and the
//     interactive editors `ed`/`ex` are covered.
//
// Fails OPEN on any internal error: a hook bug must never brick a tool call.
package gate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// editTools are the tool names whose target file must be in the edit allowlist.
var editTools = map[string]bool{
	"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true,
}

// dispatchTools are the tool names that carry a scoped-spawn prompt in the
// parent transcript (the allowlist-delivery channel for built-in subagents).
var dispatchTools = map[string]bool{"Agent": true, "Task": true}

// Action is the gate's verdict for one tool call.
type Action string

const (
	// ActionAllow emits an explicit allow so the dev is never prompted for a
	// gated agent's in-allowlist action.
	ActionAllow Action = "allow"
	// ActionDeny emits a deny with a reason the agent surfaces as a
	// prompt-vs-allowlist contradiction.
	ActionDeny Action = "deny"
	// ActionDefer emits NO output (exit 0) — Claude Code's normal permission
	// flow applies. Used only for ungated callers (the orchestrator / main
	// session, or an un-scoped dispatch).
	ActionDefer Action = "defer"
)

// Decision pairs an Action with its human/agent-facing reason.
type Decision struct {
	Action Action
	Reason string
}

// Allowlist is the dispatch-time grant the orchestrator passes. It mirrors the
// `--gate` contract: {"edit":[…],"writable_dirs":[…],"bash_deny":[…],"network":bool}.
//
// EditPresent records whether the "edit" key was present at all (distinct from
// an empty list): its presence — even as [] — marks the caller as an
// edit-scoped agent, which additionally forbids shell-based file mutation.
type Allowlist struct {
	Edit         []string
	EditPresent  bool
	BashDeny     []string
	WritableDirs []string
	Network      *bool
}

// ParseAllowlist decodes the gate JSON, tracking edit-key presence.
func ParseAllowlist(raw []byte) (*Allowlist, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil, err
	}
	a := &Allowlist{}
	if e, ok := m["edit"]; ok {
		a.EditPresent = true
		_ = json.Unmarshal(e, &a.Edit)
	}
	if b, ok := m["bash_deny"]; ok {
		_ = json.Unmarshal(b, &a.BashDeny)
	}
	if w, ok := m["writable_dirs"]; ok {
		_ = json.Unmarshal(w, &a.WritableDirs)
	}
	if n, ok := m["network"]; ok {
		_ = json.Unmarshal(n, &a.Network)
	}
	return a, nil
}

// preToolUseInput is the subset of the PreToolUse event JSON the gate consumes.
type preToolUseInput struct {
	ToolName       string `json:"tool_name"`
	AgentID        string `json:"agent_id"`
	AgentType      string `json:"agent_type"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	ToolInput      struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
	} `json:"tool_input"`
}

// gitGlobalOptWithArg are the git global options that consume the following
// token as their argument (so the subcommand verb sits one token further on).
var gitGlobalOptWithArg = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"-c": true, "--exec-path": true, "--super-prefix": true,
}

// gitSubcommand returns the git subcommand verb for a shell segment that
// invokes git — possibly path-prefixed (`/usr/bin/git`), behind env
// assignments (`FOO=1 git …`), and behind git global options (`-C`, `-c`,
// `--git-dir`, …) — else ("", false). Defeats the `git -C dir commit` family of
// substring-match evasions.
func gitSubcommand(tokens []string) (string, bool) {
	i := 0
	envAssign := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	for i < len(tokens) && envAssign.MatchString(tokens[i]) {
		i++ // skip leading VAR=val env assignments
	}
	for i < len(tokens) {
		if basename(tokens[i]) == "git" {
			j := i + 1
			for j < len(tokens) {
				tk := tokens[j]
				if gitGlobalOptWithArg[tk] {
					j += 2 // global option consumes its argument
					continue
				}
				if strings.HasPrefix(tk, "-") {
					j++ // other global flag (incl. --git-dir=… inline form)
					continue
				}
				return tk, true // first non-flag token is the subcommand
			}
			return "", false
		}
		i++
	}
	return "", false
}

func basename(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

var (
	gitDenyPat = regexp.MustCompile(`^git\s+(\S+)$`)
	segSplit   = regexp.MustCompile(`[;&|\n]+`)
)

// bashForbidden returns the matched deny pattern if the command is forbidden by
// the allowlist's bash_deny list, else ("", false). Git verbs are matched past
// intervening global flags (via gitSubcommand); remaining patterns ("mage
// install", "go get", …) match on word boundaries.
func bashForbidden(command string, denyPatterns []string) (string, bool) {
	gitVerbs := map[string]bool{}
	for _, pat := range denyPatterns {
		if m := gitDenyPat.FindStringSubmatch(strings.TrimSpace(pat)); m != nil {
			gitVerbs[m[1]] = true
		}
	}
	if len(gitVerbs) > 0 {
		for _, seg := range segSplit.Split(command, -1) {
			if sub, ok := gitSubcommand(strings.Fields(seg)); ok && gitVerbs[sub] {
				return "git " + sub, true
			}
		}
	}
	// Generic word-boundary pass for the remaining (non-git) patterns.
	for _, pat := range denyPatterns {
		if strings.TrimSpace(pat) == "" {
			continue
		}
		if wordBoundaryContains(command, pat) {
			return pat, true
		}
	}
	return "", false
}

// wordBoundaryContains reports whether needle appears in haystack delimited on
// both sides by a non-[\w-] boundary (or string edge). Emulates the python
// `(?<![\w-])needle(?![\w-])` without lookaround.
func wordBoundaryContains(haystack, needle string) bool {
	from := 0
	for {
		idx := strings.Index(haystack[from:], needle)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(needle)
		leftOK := start == 0 || !isWordChar(haystack[start-1])
		rightOK := end == len(haystack) || !isWordChar(haystack[end])
		if leftOK && rightOK {
			return true
		}
		from = start + 1
		if from >= len(haystack) {
			return false
		}
	}
}

func isWordChar(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Command-position separator class: start, whitespace, path slash, pipe,
// semicolon, ampersand, open-paren, backtick, OR a quote (`'`/`"`). The quotes
// are the improvement over the python reference, which omitted them and so
// missed `sh -c 'cp a forbidden'` (the `'cp` token).
const sepClass = "(?:^|[\\s/|;&(\x60'\"])"

// boundaryTail consumes one non-word char or end-of-string after a command name.
const boundaryTail = `(?:[^\w-]|$)`

func cmdNameRe(names ...string) *regexp.Regexp {
	return regexp.MustCompile(sepClass + "(?:" + strings.Join(names, "|") + ")" + boundaryTail)
}

var (
	// Output redirection: `>`/`>>` to a real target. `>&…` (fd dup) and
	// `/dev/null` (legit discard) are excluded in code (RE2 has no lookahead).
	reRedirect = regexp.MustCompile(`(>>?)[ \t]*(\S+)`)
	// Interpreters and shells that can write files. python3 before python so
	// the alternation does not short-match `python` before `python3`'s digit.
	reInterpreter = cmdNameRe(
		"python3", "python", "node", "deno", "bun", "ruby", "perl",
		"osascript", "php", "bash", "zsh", "dash", "ksh", "fish", "sh",
		"env", "xargs",
	)
	// File-mutating commands.
	reFileMutate = cmdNameRe(
		"cp", "mv", "install", "ln", "truncate", "touch", "mkdir", "rmdir",
		"rm", "chmod", "chown", "dd", "tee", "patch", "rsync",
	)
	// Interactive editors — always a write vector.
	reEditors = cmdNameRe("ed", "ex")
	// Stream/line editors that write only with an in-place flag.
	reInplaceCmd  = cmdNameRe("sed", "awk", "gawk", "perl", "ruby")
	reInplaceFlag = regexp.MustCompile(`(?:^|\s)-i(?:\b|nplace)`)
	reDDof        = regexp.MustCompile(sepClass + `dd` + boundaryTail + `[^|;&\n]*\bof=`)
)

// bashWriteVector returns a description of the first shell file-write/mutation
// vector in the command, else ("", false). Used to stop an edit-scoped agent
// from editing files via the shell instead of the per-file-gated Edit/Write
// tools. Reads (cat/grep/ls) and build commands (mage/go doc/git-read) carry
// none of these and pass.
func bashWriteVector(command string) (string, bool) {
	// Output redirection, excluding `>&…` and `…/dev/null`.
	for _, m := range reRedirect.FindAllStringSubmatch(command, -1) {
		target := m[2]
		if strings.HasPrefix(target, "&") {
			continue // fd duplication, e.g. 2>&1
		}
		if target == "/dev/null" || strings.HasPrefix(target, "/dev/null") {
			continue // legit discard
		}
		return "output redirection (> / >>)", true
	}
	if reDDof.MatchString(command) {
		return "dd of=", true
	}
	if reEditors.MatchString(command) {
		return "interactive editor (ed/ex)", true
	}
	if reInplaceCmd.MatchString(command) && reInplaceFlag.MatchString(command) {
		return "in-place editor (sed/awk/perl -i)", true
	}
	if reFileMutate.MatchString(command) {
		return "file-mutating command", true
	}
	if reInterpreter.MatchString(command) {
		return "interpreter/shell (can write files)", true
	}
	return "", false
}

// editAllowed reports whether file_path is within the edit allowlist. Both
// sides are normalized to absolute, cleaned paths (relative paths joined onto
// cwd); a glob entry is matched via filepath.Match against the normalized
// target and, as a fallback, the raw inputs.
func editAllowed(filePath string, allowed []string, cwd string) bool {
	target := normPath(filePath, cwd)
	for _, entry := range allowed {
		if entry == "" {
			continue
		}
		normEntry := normPath(entry, cwd)
		if target == normEntry {
			return true
		}
		if ok, _ := filepath.Match(normEntry, target); ok {
			return true
		}
		if ok, _ := filepath.Match(entry, filePath); ok {
			return true
		}
	}
	return false
}

func normPath(p, cwd string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// Evaluate is the pure decision function: given a parsed event and a resolved
// allowlist (non-nil — a nil allowlist is a defer decided by the caller), it
// returns the gate verdict. Every gated action is explicitly allowed or denied
// so the dev is never prompted.
func Evaluate(in preToolUseInput, a *Allowlist) Decision {
	switch {
	case editTools[in.ToolName]:
		fp := in.ToolInput.FilePath
		if fp == "" {
			fp = in.ToolInput.NotebookPath
		}
		if !editAllowed(fp, a.Edit, in.CWD) {
			return Decision{ActionDeny, fmt.Sprintf(
				"BLOCKED by the dispatch allowlist passed at call time: this agent may only edit %v, "+
					"but the prompt directed an edit to %q, which is NOT on the allowed list. This is a "+
					"prompt-vs-allowlist contradiction. Do NOT edit this file. STOP and report the "+
					"contradiction to the orchestrator.", a.Edit, fp,
			)}
		}
		return Decision{ActionAllow, "ta-action-gate: within the dispatch allowlist (edit)"}

	case in.ToolName == "Bash":
		cmd := in.ToolInput.Command
		if hit, ok := bashForbidden(cmd, a.BashDeny); ok {
			return Decision{ActionDeny, fmt.Sprintf(
				"BLOCKED by the dispatch allowlist passed at call time: the command matches the "+
					"forbidden pattern %q (e.g. git mutation / mage install / dependency mutation), which "+
					"this agent is not permitted to run. This is a prompt-vs-allowlist contradiction. Do "+
					"NOT run it. STOP and report the contradiction to the orchestrator.", hit,
			)}
		}
		// An edit-scoped agent (the "edit" key is present, even as []) may
		// mutate files ONLY through the per-file gated Edit/Write tools, never
		// through the shell — block the cat>/python/sed -i/tee/cp bypass.
		if a.EditPresent {
			if wv, ok := bashWriteVector(cmd); ok {
				return Decision{ActionDeny, fmt.Sprintf(
					"BLOCKED by the dispatch allowlist passed at call time: this agent's file edits are "+
						"confined to %v via the Edit/Write tools, but this Bash command uses a shell "+
						"file-write/mutation vector (%s) — the cat>/python/sed -i/tee bypass. Shell-based "+
						"file mutation is NOT permitted for an edit-scoped agent. Do NOT run it. STOP and "+
						"report the contradiction to the orchestrator.", a.Edit, wv,
				)}
			}
		}
		return Decision{ActionAllow, "ta-action-gate: within the dispatch allowlist (bash)"}

	default:
		// Reads / MCP / non-write tools the persona is allowed to call.
		return Decision{ActionAllow, "ta-action-gate: within the dispatch allowlist"}
	}
}

var allowlistRe = regexp.MustCompile(`(?s)<TA_ALLOWLIST>\s*(\{.*?\})\s*</TA_ALLOWLIST>`)

// EnvAllowlist returns the allowlist delivered via the TA_GATE_ALLOWLIST env
// var (the subprocess path: `claude -p --bare` / ollama, where the dispatcher
// exports it for the whole subprocess), or nil if unset/invalid.
func EnvAllowlist(getenv func(string) string) *Allowlist {
	raw := getenv("TA_GATE_ALLOWLIST")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	a, err := ParseAllowlist([]byte(raw))
	if err != nil {
		return nil
	}
	return a
}

// transcriptEvent is the subset of a transcript line the resolver navigates.
type transcriptEvent struct {
	Message struct {
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				SubagentType string `json:"subagent_type"`
				Prompt       string `json:"prompt"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// ResolveFromTranscript returns the allowlist for the most-recent dispatch of
// agentType that carried a <TA_ALLOWLIST> block in its spawn prompt, scanning
// the parent (orchestrator) transcript. This is the built-in Agent-tool
// delivery channel: a scoped subagent has no separate transcript, so its
// allowlist is recovered from the parent's record of the dispatch that spawned
// it. Returns nil when no matching block is found.
func ResolveFromTranscript(transcriptPath, agentType string) *Allowlist {
	if transcriptPath == "" || agentType == "" {
		return nil
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var found *Allowlist
	scanner := bufio.NewScanner(f)
	// Transcript lines carry full spawn prompts; raise the token limit well
	// above bufio's 64KB default so long dispatch prompts are not truncated.
	scanner.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !strings.Contains(string(line), "TA_ALLOWLIST") {
			continue // cheap pre-filter
		}
		var evt transcriptEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		for _, blk := range evt.Message.Content {
			if blk.Type != "tool_use" || !dispatchTools[blk.Name] {
				continue
			}
			if blk.Input.SubagentType != agentType {
				continue
			}
			m := allowlistRe.FindStringSubmatch(blk.Input.Prompt)
			if m == nil {
				continue
			}
			if a, err := ParseAllowlist([]byte(m[1])); err == nil && a != nil {
				found = a // keep scanning → last (most recent) wins
			}
		}
	}
	_ = scanner.Err() // fail open: a scan error yields whatever was found so far
	return found
}

// Run reads one PreToolUse event from r, resolves the allowlist (env →
// parent-transcript-by-agent-type → defer), evaluates it, and writes the
// permissionDecision envelope to w. Returns the process exit code (always 0:
// allow/deny carry the decision in the JSON; defer emits nothing). Fails OPEN
// on any error so a hook bug never bricks a tool call.
func Run(r io.Reader, w io.Writer, getenv func(string) string) (code int) {
	defer func() {
		if recover() != nil {
			code = 0 // fail open
		}
	}()

	raw, err := io.ReadAll(r)
	if err != nil {
		return 0 // defer
	}
	var in preToolUseInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0 // defer
	}

	// Allowlist delivery precedence: env var → parent transcript by agent_type
	// → neither (orchestrator / un-scoped → defer; dev keeps normal control).
	a := EnvAllowlist(getenv)
	if a == nil {
		if in.AgentID == "" {
			return 0 // orchestrator / main session — never gated
		}
		a = ResolveFromTranscript(in.TranscriptPath, in.AgentType)
	}
	if a == nil {
		return 0 // un-scoped dispatch — defer
	}

	d := Evaluate(in, a)
	if d.Action == ActionDefer {
		return 0
	}
	emit(w, d)
	return 0
}

func emit(w io.Writer, d Decision) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       string(d.Action),
			"permissionDecisionReason": d.Reason,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	fmt.Fprintln(w, string(b))
}
