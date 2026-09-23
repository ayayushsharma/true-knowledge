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
  search_graph|search_code) echo '{"ok":true,"results":["fake-hit"]}';;
  get_code_snippet) echo 'func Demo() {} // fake';;
  trace_path) echo '{"callers":[],"callees":[]}';;
  get_architecture|query_graph|list_projects|index_status|check_index_coverage|get_file_outline|detect_changes) echo '{"ok":true}';;
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

out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | $TK_BIN mcp)
n=$(printf '%s' "$out" | python3 -c "import json,sys; print(len(json.load(sys.stdin)['result']['tools']))")
if [ "$n" = "11" ]; then pass=$((pass+1)); printf 'ok   mcp-tools-11\n';
else fail=$((fail+1)); printf 'FAIL mcp-tools (n=%s)\n' "$n"; fi
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
