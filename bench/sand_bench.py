#!/usr/bin/env python3
"""sand token-savings benchmark.

Drives `sand mcp --profile <p>` (the real sand binary, lagom-go inside) over each
real upstream MCP server with a passthrough profile (full surface) and a sealed
allowlist profile (slim surface), then measures the REAL Claude token cost of
each tool surface with Anthropic's free `count_tokens` API. Proves sand's OWN
end-to-end savings hold through the shipped binary (not just lagom's engine).

Run:  python3 bench/sand_bench.py
Env:  ANTHROPIC_API_KEY (real key; count_tokens is free).
      SAND (optional path to sand binary; defaults to `sand` on PATH).
"""
import json, os, select, shutil, subprocess, time, pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "bench"
RAW = OUT / "raw"
RAW.mkdir(parents=True, exist_ok=True)
MODEL = "claude-haiku-4-5-20251001"
BASE = os.environ.get("ANTHROPIC_BASE_URL", "https://api.anthropic.com").rstrip("/")
KEY = os.environ["ANTHROPIC_API_KEY"]
SAND = os.environ.get("SAND") or shutil.which("sand")
NODE = shutil.which("node")
FAST = os.environ.get("SAND_E2E_UPSTREAM", "/Users/evanschultz/Documents/Code/hylla/lagom/main/bin/fast-mcp.js")
KEEP_K = 2

import urllib.request

# Upstreams to wrap. fast-mcp.js is the guaranteed local data point; the npm
# servers (lagom's headline set) are best-effort — unreachable ones are skipped.
SERVERS = [
    ("fast", [NODE, FAST]),
    ("everything", ["npx", "-y", "@modelcontextprotocol/server-everything"]),
    ("filesystem", ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"]),
    ("memory", ["npx", "-y", "@modelcontextprotocol/server-memory"]),
]


def write_profile(path, command, args, policy):
    pathlib.Path(path).write_text(json.dumps({
        "server_name": "bench",
        "upstream": {"command": command, "args": args},
        "policy": policy,
    }))


def capture_tools(profile_path, timeout=90):
    """Spawn `sand mcp --profile`, run the MCP handshake, return the slim tools/list."""
    proc = subprocess.Popen(
        [SAND, "mcp", "--profile", profile_path],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    try:
        # mcp-go enforces the protocol: initialize -> initialized -> tools/list.
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
        # sand mcp reaps its own upstream on stdin close; kill ensures no orphan.


def to_anthropic_tools(mcp_tools):
    return [{
        "name": t.get("name", "x"),
        "description": t.get("description", "") or "",
        "input_schema": t.get("inputSchema") or {"type": "object", "properties": {}},
    } for t in mcp_tools]


def count_tokens(tools):
    body = json.dumps({"model": MODEL, "messages": [{"role": "user", "content": "."}], "tools": tools}).encode()
    req = urllib.request.Request(f"{BASE}/v1/messages/count_tokens", data=body,
        headers={"x-api-key": KEY, "anthropic-version": "2023-06-01", "content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)["input_tokens"]


def main():
    if not SAND:
        raise SystemExit("sand not found (set SAND or run mage install)")
    base_tokens = count_tokens([])
    results = []
    pol = RAW / "profiles"
    pol.mkdir(exist_ok=True)

    for name, cmd in SERVERS:
        if cmd[0] is None:
            print(f"[{name}] SKIP (command unavailable)", flush=True)
            continue
        print(f"[{name}] capturing full surface…", flush=True)
        full_path = str(pol / f"{name}.full.json")
        write_profile(full_path, cmd[0], cmd[1:], {"default_presence": "keep"})
        full = capture_tools(full_path)
        if not full:
            print(f"[{name}] SKIP (no tools / unreachable)", flush=True)
            results.append({"server": name, "status": "skipped"})
            continue
        keep = [t["name"] for t in full][:KEEP_K]
        slim_path = str(pol / f"{name}.slim.json")
        write_profile(slim_path, cmd[0], cmd[1:],
            {"default_presence": "drop", "tools": {k: {"presence": "keep"} for k in keep}})
        slim = capture_tools(slim_path)

        (RAW / f"{name}.full.tools.json").write_text(json.dumps(full, indent=2))
        if slim:
            (RAW / f"{name}.slim.tools.json").write_text(json.dumps(slim, indent=2))

        tf = count_tokens(to_anthropic_tools(full)) - base_tokens
        ts = (count_tokens(to_anthropic_tools(slim)) - base_tokens) if slim else None
        row = {"server": name, "status": "ok", "tools_full": len(full),
               "tools_slim": len(slim) if slim else None, "kept": keep,
               "tool_tokens_full": tf, "tool_tokens_slim": ts,
               "saved_pct": round(100 * (tf - ts) / tf, 1) if ts is not None and tf else None,
               "model": MODEL, "base_tokens": base_tokens}
        results.append(row)
        print(f"[{name}] full={len(full)}t/{tf}tok slim={row['tools_slim']}t/{ts}tok ({row['saved_pct']}% saved)", flush=True)

    ts_stamp = subprocess.run(["date", "-u", "+%Y-%m-%dT%H:%M:%SZ"], capture_output=True, text=True).stdout.strip()
    (OUT / "results.jsonl").write_text("".join(json.dumps({**r, "ts": ts_stamp}) + "\n" for r in results))
    ok = [r for r in results if r.get("status") == "ok"]
    cols = ["server", "tools_full", "tools_slim", "tool_tokens_full", "tool_tokens_slim", "saved_pct"]
    (OUT / "results.csv").write_text(",".join(cols) + "\n" +
        "\n".join(",".join(str(r.get(c, "")) for c in cols) for r in ok) + "\n")
    tot_full = sum(r["tool_tokens_full"] for r in ok)
    tot_slim = sum(r["tool_tokens_slim"] for r in ok if r["tool_tokens_slim"] is not None)
    rep = ["# sand token-savings benchmark\n",
           f"Measured {ts_stamp} via the real `sand mcp --profile` binary (lagom-go inside) with Anthropic "
           f"`count_tokens` (model `{MODEL}`); tool-def tokens = count(message+tools) − count(message-only "
           f"baseline={base_tokens}). Slim = sealed allowlist keeping {KEEP_K} tools. Raw surfaces in `bench/raw/`.\n",
           "| upstream | tools full→slim | full tok | slim tok | saved |",
           "|---|---|---|---|---|"]
    for r in ok:
        rep.append(f"| {r['server']} | {r['tools_full']}→{r['tools_slim']} | {r['tool_tokens_full']} | "
                   f"{r['tool_tokens_slim']} | {r['saved_pct']}% |")
    if tot_full:
        rep.append(f"\n**Totals:** {tot_full} → {tot_slim} tool tokens — "
                   f"**{round(100*(tot_full-tot_slim)/tot_full,1)}% saved** across {len(ok)} upstreams.")
    (OUT / "REPORT.md").write_text("\n".join(rep) + "\n")
    print("\n".join(rep))


if __name__ == "__main__":
    main()
