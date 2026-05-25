#!/usr/bin/env python3
"""ta_action_gate — PreToolUse gate that confines a dispatched subagent to the
allowlist the orchestrator passed AT DISPATCH TIME, regardless of what the
agent's prompt tells it to do.

How the allowlist reaches the hook
----------------------------------
A PreToolUse hook fired by a SUBAGENT receives:
  * agent_id      — unique per subagent instance (present ONLY for subagents)
  * agent_type    — the persona name, e.g. "ta-go-builder"
  * transcript_path — the PARENT (orchestrator) transcript, NOT the subagent's
  * tool_name / tool_input / cwd

(Empirically verified 2026-05-24: subagents have no separate transcript file;
transcript_path is always the parent.)

The orchestrator embeds, at the top of every scoped spawn prompt:

    <TA_ALLOWLIST>
    {"edit": ["/abs/fileA", "/abs/fileB"],
     "bash_deny": ["git commit", "git push", "git add", "mage install", ...]}
    </TA_ALLOWLIST>

That spawn prompt is recorded in the PARENT transcript as the `Agent`/`Task`
tool_use's `input.prompt`, with `input.subagent_type` == the persona name —
written at dispatch time, before the subagent runs. So the hook resolves the
allowlist by scanning the parent transcript for the most-recent dispatch whose
`subagent_type` matches this `agent_type` and whose prompt carries the block.

Enforcement
-----------
  * No agent_id            -> orchestrator / main session -> ALLOW (never gated).
  * agent_id but no block  -> un-scoped dispatch          -> ALLOW.
  * block present:
      - Edit/Write/MultiEdit/NotebookEdit: target file MUST be in `edit`,
        else DENY (+ reason so the agent reports the prompt-vs-allowlist
        contradiction).
      - Bash: command MUST NOT match any `bash_deny` pattern, else DENY.

Concurrency: same-role scoped dispatches MUST be serialized (the resolver keys
on agent_type and takes the most-recent matching dispatch). Different-role
parallel dispatches are disambiguated by agent_type and are safe.

Fails OPEN on any internal error (a hook bug must never brick a tool call).
"""

import json
import os
import re
import sys
from fnmatch import fnmatch
from typing import NoReturn, Optional

ALLOWLIST_RE = re.compile(r"<TA_ALLOWLIST>\s*(\{.*?\})\s*</TA_ALLOWLIST>", re.DOTALL)
EDIT_TOOLS = {"Edit", "Write", "MultiEdit", "NotebookEdit"}
DISPATCH_TOOLS = {"Agent", "Task"}


def _defer_and_exit() -> NoReturn:
    # exit 0, no output == defer to Claude Code's normal permission flow.
    # Used ONLY for ungated callers (orchestrator / un-scoped) so the dev keeps
    # normal control of their own session.
    sys.exit(0)


def _explicit_allow() -> NoReturn:
    # For a GATED subagent: explicitly approve an in-allowlist action so it runs
    # WITHOUT prompting the dev. The dispatch allowlist is the sole authority —
    # the dev never manages a scoped agent's permitted actions.
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "allow",
            "permissionDecisionReason": "ta-action-gate: within the dispatch allowlist",
        }
    }))
    sys.exit(0)


def _deny(reason) -> NoReturn:
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }))
    sys.exit(0)


def _env_allowlist() -> Optional[dict]:
    """Allowlist delivered via env var by the dispatcher (subprocess paths:
    `claude -p --bare` / ollama). Those sessions have their own transcript and
    no agent_id, so the parent-transcript correlation doesn't apply — the
    dispatcher exports TA_GATE_ALLOWLIST=<json> for the whole subprocess."""
    raw = os.environ.get("TA_GATE_ALLOWLIST", "")
    if not raw.strip():
        return None
    try:
        data = json.loads(raw)
    except Exception:
        return None
    return data if isinstance(data, dict) else None


def _resolve_allowlist(transcript_path, agent_type) -> Optional[dict]:
    """Return the allowlist dict for the most-recent dispatch of `agent_type`
    that carried a <TA_ALLOWLIST> block, scanning the parent transcript."""
    if not transcript_path or not agent_type or not os.path.exists(transcript_path):
        return None
    found = None
    try:
        with open(transcript_path, "r", encoding="utf-8", errors="replace") as fh:
            for line in fh:
                # cheap pre-filter: only parse lines that could carry the block
                if "TA_ALLOWLIST" not in line:
                    continue
                try:
                    evt = json.loads(line)
                except Exception:
                    continue
                msg = evt.get("message") if isinstance(evt, dict) else None
                content = msg.get("content") if isinstance(msg, dict) else None
                if not isinstance(content, list):
                    continue
                for blk in content:
                    if not isinstance(blk, dict) or blk.get("type") != "tool_use":
                        continue
                    if blk.get("name") not in DISPATCH_TOOLS:
                        continue
                    inp = blk.get("input", {}) or {}
                    if inp.get("subagent_type") != agent_type:
                        continue
                    prompt = inp.get("prompt", "")
                    if not isinstance(prompt, str):
                        continue
                    m = ALLOWLIST_RE.search(prompt)
                    if not m:
                        continue
                    try:
                        data = json.loads(m.group(1))
                    except Exception:
                        continue
                    if isinstance(data, dict):
                        found = data  # keep scanning → last (most recent) wins
    except Exception:
        return None
    return found


def _norm(p, cwd):
    if not p:
        return ""
    if not os.path.isabs(p):
        p = os.path.join(cwd, p)
    return os.path.normpath(p)


def _edit_allowed(file_path, allowed, cwd):
    target = _norm(file_path, cwd)
    for entry in allowed:
        if not isinstance(entry, str):
            continue
        norm_entry = _norm(entry, cwd)
        if target == norm_entry:
            return True
        if fnmatch(target, norm_entry) or fnmatch(file_path, entry):
            return True
    return False


_GIT_GLOBAL_OPT_WITH_ARG = {
    "-C", "--git-dir", "--work-tree", "--namespace", "-c", "--exec-path",
    "--super-prefix",
}


def _git_subcommand(seg_tokens):
    """If a shell segment invokes git (possibly path-prefixed, behind env
    assignments, and behind git global options like -C / -c / --git-dir),
    return the git subcommand verb; else None. Defeats `git -C dir commit`,
    `/usr/bin/git commit`, `FOO=1 git commit`, `git --git-dir=x commit`."""
    n = len(seg_tokens)
    i = 0
    while i < n and re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", seg_tokens[i]):
        i += 1  # skip leading VAR=val env assignments
    while i < n:
        if seg_tokens[i].rsplit("/", 1)[-1] == "git":
            j = i + 1
            while j < n:
                tk = seg_tokens[j]
                if tk in _GIT_GLOBAL_OPT_WITH_ARG:
                    j += 2  # global option consumes its argument
                    continue
                if tk.startswith("-"):
                    j += 1  # other global flag (incl. --git-dir=… inline)
                    continue
                return tk  # first non-flag token is the subcommand
            return None
        i += 1
    return None


def _bash_forbidden(command, deny_patterns):
    cmd = command or ""
    # Derive the git verbs the gate forbids (e.g. "git commit" -> "commit") so we
    # can catch them past intervening global flags, not just as a literal
    # "git commit" substring (the `git -C dir commit` evasion).
    git_verbs = set()
    for pat in deny_patterns:
        if isinstance(pat, str):
            m = re.match(r"^git\s+(\S+)$", pat.strip())
            if m:
                git_verbs.add(m.group(1))
    if git_verbs:
        for seg in re.split(r"[;&|\n]+", cmd):
            sub = _git_subcommand(seg.split())
            if sub is not None and sub in git_verbs:
                return "git " + sub
    # Generic word-boundary pass for the remaining (non-git) patterns:
    # "mage install", "go get", "go mod", etc.
    for pat in deny_patterns:
        if not isinstance(pat, str) or not pat.strip():
            continue
        if re.search(r"(?<![\w-])" + re.escape(pat) + r"(?![\w-])", cmd):
            return pat
    return None


# Shell file-write / file-mutation vectors. A SCOPED agent edits ONLY via the
# Edit/Write tools (per-file gated above); it must NOT mutate files through the
# shell — the cat>/python/sed -i/tee/cp bypass. Reads (cat/grep/ls) and build
# commands (mage/go doc/git-read) carry none of these, so they pass.
# Command names are matched basename-aware: a name counts if it sits at the
# start, or right after whitespace / a path slash / a segment separator
# (| ; & ( ` $( ), so `/usr/bin/python3`, `foo && cp`, `a|tee` are all caught.
_CMD = r"(?:^|[\s/|;&(`])"
_WRITE_VECTORS = [
    (re.compile(r">>?\s*(?!&|/dev/null\b)\S"), "output redirection (> / >>)"),
    (re.compile(_CMD + r"tee(?![\w-])"), "tee"),
    (re.compile(_CMD + r"sed(?![\w-])[^|;&\n]*\s-i"), "sed -i (in-place edit)"),
    (re.compile(_CMD + r"(?:perl|ruby)(?![\w-])[^|;&\n]*\s-i"), "perl/ruby -i (in-place edit)"),
    (re.compile(_CMD + r"dd(?![\w-])[^|;&\n]*\bof="), "dd of="),
    (re.compile(_CMD + r"(?:python3?|node|deno|bun|ruby|perl|osascript|php)(?![\w-])"), "interpreter (can write files)"),
    (re.compile(_CMD + r"(?:cp|mv|install|ln|truncate|touch|mkdir|rmdir|rm|chmod|chown|dd)(?![\w-])"), "file-mutating command"),
]


def _bash_write_vector(command):
    """Return a description of the first shell write/mutation vector in the
    command, else None. Used to block a scoped agent from editing files via the
    shell instead of the per-file-gated Edit/Write tools."""
    cmd = command or ""
    for rx, desc in _WRITE_VECTORS:
        if rx.search(cmd):
            return desc
    return None


def _log(rec):
    try:
        with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "ta_gate_debug.log"), "a", encoding="utf-8") as fh:
            fh.write(json.dumps(rec) + "\n")
    except Exception:
        pass


def main():
    try:
        data = json.load(sys.stdin)
    except Exception:
        _defer_and_exit()

    tool = data.get("tool_name", "")
    tinput = data.get("tool_input", {}) or {}
    cwd = data.get("cwd", "") or os.getcwd()
    agent_id = data.get("agent_id", "")
    agent_type = data.get("agent_type", "")
    transcript = data.get("transcript_path", "")

    # Allowlist delivery, in precedence order:
    #   1. TA_GATE_ALLOWLIST env var — subprocess paths (`claude -p` / ollama),
    #      set by the dispatcher for the whole subprocess.
    #   2. parent transcript by agent_type — built-in Agent-tool subagents.
    #   3. neither -> orchestrator / un-scoped -> defer (dev keeps normal control).
    allowlist = _env_allowlist()
    if allowlist is None:
        if not agent_id:
            _defer_and_exit()  # orchestrator / main session
        allowlist = _resolve_allowlist(transcript, agent_type)

    if allowlist is None:
        _log({
            "agent_id": agent_id, "agent_type": agent_type, "tool": tool,
            "block_found": False, "decision": "defer",
            "target": tinput.get("file_path") or tinput.get("command") or "",
        })
        _defer_and_exit()

    # Gated subagent: the dispatch allowlist is the SOLE authority. Every action
    # is explicitly allowed or denied here so the dev is NEVER prompted.
    decision = "allow"
    reason = ""
    if tool in EDIT_TOOLS:
        fp = tinput.get("file_path") or tinput.get("notebook_path") or ""
        allowed = allowlist.get("edit", [])
        if not isinstance(allowed, list):
            allowed = []
        if not _edit_allowed(fp, allowed, cwd):
            decision = "deny"
            reason = (
                "BLOCKED by the dispatch allowlist passed at call time: this "
                f"agent may only edit {allowed}, but the prompt directed an edit "
                f"to '{fp}', which is NOT on the allowed list. This is a "
                "prompt-vs-allowlist contradiction. Do NOT edit this file. STOP "
                "and report the contradiction to the orchestrator."
            )
    elif tool == "Bash":
        cmd = tinput.get("command", "")
        deny = allowlist.get("bash_deny", [])
        if not isinstance(deny, list):
            deny = []
        hit = _bash_forbidden(cmd, deny)
        if hit:
            decision = "deny"
            reason = (
                f"BLOCKED by the dispatch allowlist passed at call time: the "
                f"command matches the forbidden pattern '{hit}' (e.g. git "
                "mutation / mage install / dependency mutation), which this "
                "agent is not permitted to run. This is a prompt-vs-allowlist "
                "contradiction. Do NOT run it. STOP and report the contradiction "
                "to the orchestrator."
            )
        elif "edit" in allowlist:
            # An edit-scoped agent may mutate files ONLY through the per-file
            # gated Edit/Write/MultiEdit tools — NEVER through the shell. Block
            # the cat>/python/sed -i/tee/cp bypass so the per-file scope cannot
            # be circumvented with a Bash file-write.
            wv = _bash_write_vector(cmd)
            if wv is not None:
                decision = "deny"
                reason = (
                    "BLOCKED by the dispatch allowlist passed at call time: this agent's file edits "
                    f"are confined to {allowlist.get('edit', [])} via the Edit/Write tools, but this "
                    f"Bash command uses a shell file-write/mutation vector ({wv}) — the "
                    "cat>/python/sed -i/tee bypass. Shell-based file mutation is NOT permitted for an "
                    "edit-scoped agent. Do NOT run it. STOP and report the contradiction to the "
                    "orchestrator."
                )
    # All other tools the persona is allowed to call (Read/Grep/Glob/mcp_*/...)
    # fall through to explicit allow below — gated agents run their permitted
    # tools without prompting the dev.

    _log({
        "agent_id": agent_id,
        "agent_type": agent_type,
        "tool": tool,
        "block_found": True,
        "decision": decision,
        "target": tinput.get("file_path") or tinput.get("command") or "",
    })

    if decision == "deny":
        _deny(reason)
    _explicit_allow()


if __name__ == "__main__":
    try:
        main()
    except Exception:
        sys.exit(0)
