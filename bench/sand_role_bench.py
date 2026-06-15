#!/usr/bin/env python3
"""sand per-role token-savings benchmark on the REAL upstreams sand pays for.

For each real upstream MCP server an agent loads (ta, hylla) this drives the real
`sand mcp --profile` binary to capture the FULL tools/list, then for each cascade
role (planner / builder / qa / closeout) writes a sealed allowlist profile keeping
ONLY that role's tools and re-captures the SLIM list. Tokens are measured with
Anthropic's free `count_tokens`. The per-role COMBINED row (ta+hylla summed) is
the headline: what a real agent of that role pays in tool-surface tokens on EVERY
turn, full vs slimmed.

Self-correcting: each role's wishlist is intersected with the tools actually
present upstream, so a renamed/absent tool is skipped (and logged), never errors.

Run:  python3 bench/sand_role_bench.py
Env:  ANTHROPIC_API_KEY (free count_tokens); SAND (optional sand path);
      SAND_PROJECT (ta --project path; defaults to this repo).
"""
import json, os, select, shutil, subprocess, time, pathlib, urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "bench"
RAW = OUT / "raw"
(RAW / "roles").mkdir(parents=True, exist_ok=True)
MODEL = "claude-haiku-4-5-20251001"
BASE = os.environ.get("ANTHROPIC_BASE_URL", "https://api.anthropic.com").rstrip("/")
KEY = os.environ["ANTHROPIC_API_KEY"]
SAND = os.environ.get("SAND") or shutil.which("sand")
HYLLA = shutil.which("hylla") or "/Users/evanschultz/go/bin/hylla"
PROJECT = os.environ.get("SAND_PROJECT", str(ROOT))

# Real upstreams an agent loads (command, args) — the surfaces billed every turn.
UPSTREAMS = {
    "ta": ["ta", "--project", PROJECT],
    "hylla": [HYLLA, "mcp"],
}

# Each cascade role's tool wishlist per upstream (from the persona allowlists in
# CLAUDE.md). Intersected with the captured full surface, so guesses are safe.
ROLES = {
    "planner": {
        "ta": ["create", "update", "get", "search", "schema", "list_sections"],
        "hylla": ["hylla_search", "hylla_search_keyword", "hylla_search_vector",
                  "hylla_node_full", "hylla_refs_find", "hylla_graph_nav",
                  "hylla_artifact_overview", "hylla_artifact_metadata"],
    },
    "builder": {  # builders edit code Hylla hasn't ingested yet -> no hylla
        "ta": ["get", "search", "create", "update", "delete", "move"],
        "hylla": [],
    },
    "qa": {
        "ta": ["get", "search"],
        "hylla": ["hylla_search", "hylla_search_keyword", "hylla_node_full"],
    },
    "closeout": {
        "ta": ["get", "search", "schema", "list_sections"],
        "hylla": ["hylla_search", "hylla_search_keyword", "hylla_node_full"],
    },
}


def write_profile(path, command, args, policy):
    pathlib.Path(path).write_text(json.dumps({
        "server_name": "bench", "upstream": {"command": command, "args": args}, "policy": policy}))


def capture_tools(profile_path, timeout=90):
    proc = subprocess.Popen([SAND, "mcp", "--profile", profile_path],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    try:
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                       "clientInfo": {"name": "bench", "version": "0"}}}) + "\n")
        proc.stdin.flush()
        deadline = time.time() + timeout
        initialized = False
        while time.time() < deadline:
            if not select.select([proc.stdout], [], [], max(0.1, deadline - time.time()))[0]:
                break
            line = proc.stdout.readline()
            if not line:
                break
            try:
                m = json.loads(line)
            except Exception:
                continue
            if m.get("id") == 1 and "result" in m and not initialized:
                initialized = True
                proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
                proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}) + "\n")
                proc.stdin.flush()
            elif m.get("id") == 2 and "result" in m:
                return m["result"].get("tools", [])
        return None
    finally:
        proc.kill()


def sanitize(name):
    # Anthropic's tools API requires ^[a-zA-Z0-9_-]{1,64}$; upstream names like
    # hylla's "hylla.artifact.list" use dots. Map dots->underscores for the token
    # count only (token cost is unchanged; the policy still uses the raw name).
    return name.replace(".", "_")[:64]


def to_anthropic(tools):
    return [{"name": sanitize(t.get("name", "x")), "description": t.get("description", "") or "",
             "input_schema": t.get("inputSchema") or {"type": "object", "properties": {}}} for t in tools]


def count_tokens(tools):
    body = json.dumps({"model": MODEL, "messages": [{"role": "user", "content": "."}], "tools": tools}).encode()
    req = urllib.request.Request(f"{BASE}/v1/messages/count_tokens", data=body,
        headers={"x-api-key": KEY, "anthropic-version": "2023-06-01", "content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)["input_tokens"]


def main():
    if not SAND:
        raise SystemExit("sand not found (set SAND or run mage install)")
    base = count_tokens([])
    pol = RAW / "roles"

    # 1. Capture each upstream's FULL surface once.
    full = {}
    for up, cmd in UPSTREAMS.items():
        print(f"[{up}] capturing full surface…", flush=True)
        p = str(pol / f"{up}.full.json")
        write_profile(p, cmd[0], cmd[1:], {"default_presence": "keep"})
        tools = capture_tools(p)
        if not tools:
            print(f"[{up}] SKIP (unreachable)", flush=True)
            continue
        full[up] = tools
        (RAW / f"{up}.full.tools.json").write_text(json.dumps(tools, indent=2))
        names = [t["name"] for t in tools]
        full_tok = count_tokens(to_anthropic(tools)) - base
        print(f"[{up}] {len(tools)} tools / {full_tok} tok — {names}", flush=True)

    # 2. Per role, per upstream: slim to the role's tools, measure.
    rows = []
    for role, wishlist in ROLES.items():
        combined_full = combined_slim = 0
        per_up = {}
        for up, tools in full.items():
            # Match the wishlist (underscore form) against sanitized upstream
            # names, but keep the RAW name in the policy so lagom matches upstream.
            raw_by_san = {sanitize(t["name"]): t["name"] for t in tools}
            keep = [raw_by_san[w] for w in wishlist.get(up, []) if w in raw_by_san]
            full_tok = count_tokens(to_anthropic(tools)) - base
            if keep:
                p = str(pol / f"{role}.{up}.slim.json")
                write_profile(p, UPSTREAMS[up][0], UPSTREAMS[up][1:],
                    {"default_presence": "drop", "tools": {k: {"presence": "keep"} for k in keep}})
                slim = capture_tools(p)
                slim_tok = (count_tokens(to_anthropic(slim)) - base) if slim else 0
                slim_n = len(slim) if slim else 0
            else:  # role uses nothing from this upstream -> 0 tokens (drop entirely)
                slim_tok, slim_n = 0, 0
            per_up[up] = {"full_tools": len(tools), "full_tok": full_tok,
                          "slim_tools": slim_n, "slim_tok": slim_tok, "kept": keep}
            combined_full += full_tok
            combined_slim += slim_tok
        saved = round(100 * (combined_full - combined_slim) / combined_full, 1) if combined_full else None
        rows.append({"role": role, "per_upstream": per_up,
                     "combined_full_tok": combined_full, "combined_slim_tok": combined_slim, "saved_pct": saved})
        print(f"[{role}] combined full={combined_full}tok slim={combined_slim}tok ({saved}% saved per turn)", flush=True)

    ts = subprocess.run(["date", "-u", "+%Y-%m-%dT%H:%M:%SZ"], capture_output=True, text=True).stdout.strip()
    (OUT / "role_results.jsonl").write_text("".join(json.dumps({**r, "ts": ts}) + "\n" for r in rows))
    cols = ["role", "combined_full_tok", "combined_slim_tok", "saved_pct"]
    (OUT / "role_results.csv").write_text(",".join(cols) + "\n" +
        "\n".join(",".join(str(r.get(c, "")) for c in cols) for r in rows) + "\n")
    rep = ["# sand per-role token-savings benchmark (real upstreams: ta + hylla)\n",
           f"Measured {ts} via the real `sand mcp --profile` binary + Anthropic `count_tokens` "
           f"(model `{MODEL}`, baseline={base}). Each row = a cascade role's COMBINED tool-surface cost "
           f"across the upstreams it loads (ta+hylla), full vs slimmed to that role's actual tools. "
           f"Tool defs are re-sent on EVERY turn, so this is a per-turn-per-agent saving. Raw surfaces + "
           f"profiles in `bench/raw/`.\n",
           "| role | full tok (ta+hylla) | slim tok | saved/turn |",
           "|---|---|---|---|"]
    for r in rows:
        rep.append(f"| {r['role']} | {r['combined_full_tok']} | {r['combined_slim_tok']} | {r['saved_pct']}% |")
    # Per-upstream anchor + a worked, fully-labelled cost example (assumptions are
    # explicit so the reader can re-derive with their own numbers).
    anchor = ", ".join(f"{up}={count_tokens(to_anthropic(t)) - base} tok ({len(t)} tools)" for up, t in full.items())
    TURNS, AGENTS, PRICE = 15, 12, 3.0  # illustrative: turns/agent, agents/cascade, $/Mtok input
    saved_turn = {r["role"]: r["combined_full_tok"] - r["combined_slim_tok"] for r in rows}
    avg_saved = sum(saved_turn.values()) // len(saved_turn)
    per_cascade = avg_saved * TURNS * AGENTS
    rep += ["",
        f"**Anchor (full surface, per turn):** {anchor}; combined = {rows[0]['combined_full_tok']} tok.",
        "",
        "**Why it compounds — tool defs are re-sent on EVERY turn of EVERY agent.** "
        f"Avg tokens saved/turn across roles = {avg_saved}. Illustrative cascade "
        f"({AGENTS} agents × {TURNS} turns) → **~{per_cascade:,} input tokens saved per cascade** "
        f"(at ${PRICE:.2f}/Mtok input ≈ **${per_cascade/1e6*PRICE:.2f}/cascade**, before output/caching effects). "
        "Adjust TURNS/AGENTS/PRICE in the script for your own model + fan-out. The undeniable version "
        "is an A/B run of one real cascade (full vs slim) comparing billed input tokens from the dispatch trace."]
    (OUT / "role_REPORT.md").write_text("\n".join(rep) + "\n")
    print("\n".join(rep))


if __name__ == "__main__":
    main()
