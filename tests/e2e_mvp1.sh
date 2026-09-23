#!/bin/sh
# MVP1 end-to-end smoke: isolated TK_HOME, fixture repo, full CLI + MCP matrix.
# Default mode uses a fake cbm backend (no network, CI-safe).
# TK_LIVE=1 uses the real CBM binary via TK_CBM_BIN (needs a managed copy,
# e.g. from a prior `tk setup`: TK_CBM_BIN=<cache>/bin/codebase-memory-mcp).
# Both modes use a fresh mktemp TK_HOME unless TK_HOME is already set.
set -eu

TK_BIN="${TK_BIN:-./tk}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
if [ -z "${TK_HOME:-}" ]; then export TK_HOME="$T/home"; fi

if [ "${TK_LIVE:-0}" = "1" ]; then
  echo "== live mode: using managed backends"
  if [ -z "${TK_CBM_BIN:-}" ] || [ ! -x "${TK_CBM_BIN:-}" ]; then
    echo "TK_LIVE=1 needs executable TK_CBM_BIN" >&2
    exit 2
  fi
else
  echo "== fake-backend mode"
  cat > "$T/fake-cbm" <<'EOF'
#!/bin/sh
# args: cli <tool> --args-file <path>  (or raw passthrough)
tool="$2"
case "$tool" in
  index_repository) echo '{"status":"indexed"}';;
  search_graph|search_code)
    if grep -q 'Demo' "$4" 2>/dev/null; then echo '{"ok":true,"results":[{"name":"Demo","path":"main.go"}]}'
    else echo '{"ok":true,"results":[]}'; fi;;
  get_code_snippet) echo 'func Demo() {} // fake';;
  trace_path) echo '{"callers":[],"callees":[]}';;
  get_architecture|query_graph|list_projects|index_status|get_file_outline|detect_changes) echo '{"ok":true,"coverage":"clean"}';;
  check_index_coverage) echo 'generation_matches: true
hash_records_complete: true
recording_status: complete';;
  *) echo "{\"ok\":true,\"tool\":\"$tool\"}";;
esac
EOF
  chmod +x "$T/fake-cbm"
  export TK_CBM_BIN="$T/fake-cbm"
fi

FIX="$T/fixture"
mkdir -p "$FIX"
printf 'package main\n\nfunc Demo() {}\n' > "$FIX/main.go"
if command -v git >/dev/null 2>&1; then
  git -C "$FIX" init -q
  git -C "$FIX" add -A
  git -C "$FIX" -c user.email=t@t -c user.name=t commit -qm init
fi

pass=0; fail=0
check() { # desc, expected_exit, cmd...
  desc="$1"; want="$2"; shift 2
  if out=$("$@" 2>&1); then code=0; else code=$?; fi
  if [ "$code" = "$want" ]; then pass=$((pass+1)); printf 'ok   %s\n' "$desc";
  else fail=$((fail+1)); printf 'FAIL %s (exit=%s want=%s)\n  %s\n' "$desc" "$code" "$want" "$out"; fi
}

check init 0 $TK_BIN init
check register 0 $TK_BIN register "$FIX" --name demo
check register-dup 1 $TK_BIN register "$FIX" --name demo
check index 0 $TK_BIN index demo
check sync 0 $TK_BIN sync demo
check status 0 $TK_BIN status
check status-json 0 $TK_BIN status --json
check find 0 $TK_BIN find Demo demo
check find-label 0 $TK_BIN find Demo demo --label Function
check explain 0 $TK_BIN explain Demo demo
check grep 0 $TK_BIN grep Demo demo
check grep-badregex 1 $TK_BIN grep "(unclosed" --regex
check arch 0 $TK_BIN arch demo
check query 0 $TK_BIN query "MATCH (f:Function) RETURN f.name LIMIT 5" demo
check outline 0 $TK_BIN outline main.go demo
check impact 0 $TK_BIN impact demo
check source-search 0 $TK_BIN source-search Demo demo
check config-get 0 $TK_BIN config get index_mode
check config-validate 0 $TK_BIN config validate
check daemon-status 0 $TK_BIN daemon status
check completion 0 $TK_BIN completion bash
check complete-projects 0 $TK_BIN __complete index ""
check validate-hit 0 $TK_BIN validate Demo demo
check validate-miss 0 $TK_BIN validate DoesNotExist demo
check kg-find-alias 0 $TK_BIN kg_find Demo demo
check kg-explain-alias 0 $TK_BIN kg_explain Demo demo
check kg-grep-alias 0 $TK_BIN kg_grep Demo demo

# --- memory layer (tk-owned, no CBM needed: same checks in both modes) ---
MEM="$TK_HOME/data/mem"
check mem-save 0 $TK_BIN mem save db postgres --project demo --provenance setup
check mem-recall 0 $TK_BIN mem recall db --project demo
check mem-save-global 0 $TK_BIN mem save api-base https://api.internal.example.com --scope global
check mem-recall-global-fallback 0 $TK_BIN mem recall api-base --project demo
check mem-secret-queued 0 $TK_BIN mem save openai-key "sk-proj-E2Ee2e01234567890123456789012" --project demo
check mem-review-list 0 $TK_BIN mem review list
memid=$("$TK_BIN" mem review list --json | python3 -c "import json,sys; print(json.load(sys.stdin)['reviews'][0]['id'])")
check mem-review-approve 0 $TK_BIN mem review approve "$memid"
check mem-recall-approved-secret 0 $TK_BIN mem recall openai-key --project demo
"$TK_BIN" mem save deadtoken "ghp_E2ETestToken01234567890123456789012" --project demo >/dev/null
rid=$("$TK_BIN" mem review list --json | python3 -c "import json,sys; print([r['id'] for r in json.load(sys.stdin)['reviews'] if r['topic']=='deadtoken'][0])")
check mem-review-reject 0 $TK_BIN mem review reject "$rid"
if "$TK_BIN" mem recall deadtoken --project demo | grep -q 'E2ETestToken'; then
  fail=$((fail+1)); printf 'FAIL mem-rejected-not-stored\n'
else
  pass=$((pass+1)); printf 'ok   mem-rejected-not-stored\n'
fi
if command -v stat >/dev/null 2>&1 && [ "$(stat -c '%a' "$MEM/facts.db" 2>/dev/null)" = "600" ]; then
  pass=$((pass+1)); printf 'ok   mem-facts-db-0600\n'
else
  fail=$((fail+1)); printf 'FAIL mem-facts-db-0600\n'
fi

# --- notes layer (tk-owned, review-gated) ---
check note-save 0 $TK_BIN note save "tls config" --text "certificates rotate monthly via letsencrypt on the proxy" --project demo
if $TK_BIN note search letsencrypt --project demo | grep -q 'proxy'; then
  fail=$((fail+1)); printf 'FAIL note-gated-before-approval\n'
else
  pass=$((pass+1)); printf 'ok   note-gated-before-approval\n'
fi
nid=$("$TK_BIN" note review list --json | python3 -c "import json,sys; print(json.load(sys.stdin)['reviews'][0]['id'])")
check note-review-approve 0 $TK_BIN note review approve "$nid"
check note-search-hit 0 $TK_BIN note search letsencrypt --project demo
check note-toc 0 $TK_BIN note toc --project demo
"$TK_BIN" note save "superseded idea" --text "an old idea about feature flags that we dropped" --project demo >/dev/null
nid2=$("$TK_BIN" note review list --json | python3 -c "import json,sys; print([r['id'] for r in json.load(sys.stdin)['reviews'] if r['title']=='superseded idea'][0])")
check note-review-reject 0 $TK_BIN note review reject "$nid2"
if $TK_BIN note search feature --project demo | grep -q 'old idea'; then
  fail=$((fail+1)); printf 'FAIL note-rejected-not-stored\n'
else
  pass=$((pass+1)); printf 'ok   note-rejected-not-stored\n'
fi
check note-reindex 0 $TK_BIN note reindex
if command -v stat >/dev/null 2>&1 && [ "$(stat -c '%a' "$TK_HOME"/data/notes/demo/*.md 2>/dev/null | head -1)" = "600" ]; then
  pass=$((pass+1)); printf 'ok   note-md-0600\n'
else
  fail=$((fail+1)); printf 'FAIL note-md-0600\n'
fi

# --- embedding layer (optional external /api/embed, BM25-first) ---
if command -v python3 >/dev/null 2>&1; then
  python3 - "$T" <<'PYEOF' >/dev/null 2>&1 &
import sys, json, http.server, socketserver
d = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', 0))
        body = json.loads(self.rfile.read(n))
        with open(d + '/mock_embed.log', 'a') as f:
            f.write(json.dumps([body.get('model'), len(body.get('input', []))]) + '\n')
        vecs = [[0.1] * 8 for _ in body.get('input', [])]
        resp = json.dumps({'model': body.get('model'), 'embeddings': vecs}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
    def log_message(self, *a):
        pass
with socketserver.TCPServer(('127.0.0.1', 0), H) as srv:
    with open(d + '/mock_embed.port', 'w') as f:
        f.write(str(srv.server_address[1]))
    srv.serve_forever()
PYEOF
  EMB_PID=$!
  for _ in $(seq 1 50); do [ -f "$T/mock_embed.port" ] && break; sleep 0.1; done
  EMB_PORT=$(cat "$T/mock_embed.port" 2>/dev/null)
  if [ -n "$EMB_PORT" ]; then
    check embed-set-model 0 $TK_BIN config set embedding.model e2e-notes
    check embed-set-endpoint 0 $TK_BIN config set embedding.endpoint "http://127.0.0.1:$EMB_PORT"
    check embed-enable 0 $TK_BIN config set embedding.enabled true
    check embed-reindex 0 $TK_BIN note reindex
    check embed-search-live 0 $TK_BIN note search letsencrypt --project demo
    kill "$EMB_PID" 2>/dev/null || true
    wait "$EMB_PID" 2>/dev/null || true
    check embed-search-down 0 $TK_BIN note search letsencrypt --project demo
    if grep -q 'e2e-notes' "$T/mock_embed.log" 2>/dev/null; then
      pass=$((pass+1)); printf 'ok   embed-calls-recorded\n'
    else
      fail=$((fail+1)); printf 'FAIL embed-calls-recorded (%s)\n' "$(cat "$T/mock_embed.log" 2>/dev/null)"
    fi
  else
    fail=$((fail+1)); printf 'FAIL embed-mock-server\n'
  fi
fi

out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | $TK_BIN mcp)
n=$(printf '%s' "$out" | python3 -c "import json,sys; print(len(json.load(sys.stdin)['result']['tools']))")
if [ "$n" = "11" ]; then pass=$((pass+1)); printf 'ok   mcp-tools-11\n';
else fail=$((fail+1)); printf 'FAIL mcp-tools (n=%s)\n' "$n"; fi
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | $TK_BIN mcp --tool-profile analysis)
n=$(printf '%s' "$out" | python3 -c "import json,sys; print(len(json.load(sys.stdin)['result']['tools']))")
if [ "$n" = "14" ]; then pass=$((pass+1)); printf 'ok   mcp-tools-analysis-14\n';
else fail=$((fail+1)); printf 'FAIL mcp-tools-analysis (n=%s)\n' "$n"; fi
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | $TK_BIN mcp --tool-profile minimal)
n=$(printf '%s' "$out" | python3 -c "import json,sys; print(len(json.load(sys.stdin)['result']['tools']))")
if [ "$n" = "3" ]; then pass=$((pass+1)); printf 'ok   mcp-tools-minimal-3\n';
else fail=$((fail+1)); printf 'FAIL mcp-tools-minimal (n=%s)\n' "$n"; fi
# NOTE: zoekt is a linked library, not a backend binary — the fake only
# stubs CBM, so source_search genuinely succeeds in both modes.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Demo","project":"demo"}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'main.go'*Demo*) pass=$((pass+1)); printf 'ok   mcp-source-search-hit\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-source-search\n  %s\n' "$out";;
esac

printf '\npass=%d fail=%d\n' "$pass" "$fail"

# Unified trace log: every invocation recorded with input+output+backends.
LOG="$TK_HOME/state/logs/tk.log"
if [ ! -s "$LOG" ]; then fail=$((fail+1)); printf 'FAIL trace-log-missing\n';
else
  if python3 - "$LOG" <<'EOF'
import json, sys
recs = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
assert recs, "empty log"
for r in recs:
    for k in ("v", "ts", "exit", "output"):
        assert k in r, f"missing {k} in {r}"
    assert "argv" in r or "mcp" in r, f"missing input in {r}"
assert any(r.get("events") for r in recs), "no backend events logged"
assert any(r.get("exit") != 0 for r in recs), "no failure records (grep-badregex should log exit=1)"
print(f"trace-log ok ({len(recs)} records)")
EOF
  then pass=$((pass+1)); printf 'ok   trace-log\n';
  else fail=$((fail+1)); printf 'FAIL trace-log\n'; fi
fi

printf 'trace: pass=%d fail=%d\n' "$pass" "$fail"
[ "$fail" = "0" ]
