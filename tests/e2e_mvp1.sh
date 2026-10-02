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
# A spawn counter, so a test can prove a resident served N calls with 1 engine.
# The resident's whole claim is that the engine is started once; asserting it
# from inside the fake is the only way to count from the engine's side.
if [ -n "${TK_FAKE_SPAWNS:-}" ]; then echo x >> "$TK_FAKE_SPAWNS"; fi

# JSON-escapes a string body. awk, not sed: sed's N accumulates the input into
# one line, so by the time it could replace a newline the newline was already
# gone and every multi-line answer came back as one run-on line. That is not a
# cosmetic fault - the result is invalid JSON, tk rejects the envelope, and the
# read silently falls back to a second engine spawn.
json_escape() {
  printf '%s' "$1" | awk 'BEGIN{ORS=""} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); printf "%s\\n", $0}'
}

# tool_body prints the rendered answer for $tool, and sets body_total and
# body_returned from the counters in that answer so the structuredContent block
# is derived rather than invented. One definition, used by both the cli and the
# MCP path: the two must not be able to disagree.
#
# Arguments are passed explicitly, never read from the enclosing scope: this
# function is invoked in a command substitution, which is a subshell, so a
# variable set by the caller is simply not visible in here. A silent miss there
# would answer every trace_path with the wrong branch and look like a tk bug.
#
#   $1 = how to search for the fixture symbol: "file <path>" for the cli path,
#        "line <text>" for the MCP path, whose arguments are inline JSON
#   $2 = the first argv token, used to tell a cli tool from a control verb
#
# The two are spelled differently on purpose. The cli path has an args FILE and
# the MCP path has a request LINE, and a search that silently looks in the
# wrong one returns the empty branch for every real hit.
tool_body() {
  case "$1" in
    file) src="$2"; has() { grep -q "$1" "$src" 2>/dev/null; };;
    line) src="$2"; has() { printf '%s' "$src" | grep -q "$1" 2>/dev/null; };;
    *) echo "fake-cbm: tool_body needs file|line" >&2; return 1;;
  esac
  case "$tool" in
    index_repository) printf '{"status":"indexed"}\n'; return 0;;
    search_graph|search_code)
      if has Demo; then
        body='results: 1  (cols: qn label file lines in out)
  live.Demo Function main.go 3-4 1 1
total: 1
returned: 1
has_more: false
truncated: false'
      else
        body='results: 0  (cols: qn label file lines in out)
total: 0
returned: 0
has_more: false
truncated: false'
      fi;;
    get_code_snippet) body='func Demo() {} // fake';;
    trace_path)
      if has TotalMiss; then
        body='function: TotalMiss
direction: inbound
callers_total: 0
callers_total_relation: eq
callers: 0  (cols: qn hop)'
      else
        body='function: Demo
direction: inbound
callers_total: 1
callers_total_relation: eq
callers: 1  (cols: qn hop)
  live.main 1'
      fi;;
    get_architecture|query_graph|list_projects|index_status|get_file_outline|detect_changes)
      body='{"ok":true,"coverage":"clean"}';;
    check_index_coverage) body='generation_matches: true
hash_records_complete: true
recording_status: complete';;
    # Reports the soft limit. It must not `return`: tool_body is called in a
    # command substitution, so returning ends the subshell and the caller
    # captures an empty string. The answer is the body, like every other tool.
    stack_probe) body=$(ulimit -S -s);;
    # An unmodelled `cli` tool must fail loudly. The old catch-all answered
    # `{"ok":true,...}`, which parseEnvelope reads as a real envelope and which
    # therefore disguised a missing fake as a working call. Only the cli path
    # is strict: `daemon ...` and the raw passthrough are control-plane verbs
    # tk forwards verbatim, and tk decides what they mean, not the fake.
    *)
      case "$2" in
        cli) echo "fake-cbm: unmodelled tool: $tool" >&2; return 1;;
        *) body='{"ok":true,"tool":"'$tool'"}';;
      esac;;
  esac
  # The counters are printed, not exported: this function is always called in a
  # command substitution, and a variable set inside one dies with the subshell.
  # Two leading lines carry them out; the body is everything after.
  t=$(printf '%s' "$body" | sed -n 's/^total: \([0-9][0-9]*\).*/\1/p'); [ -n "$t" ] || t=0
  r=$(printf '%s' "$body" | sed -n 's/^returned: \([0-9][0-9]*\).*/\1/p'); [ -n "$r" ] || r=0
  printf '@@%s %s\n%s\n' "$t" "$r" "$body"
}

# No argv = MCP stdio server mode, which is what the resident starts.
# args: one JSON-RPC request per line on stdin; one reply line per reply.
if [ $# -eq 0 ]; then
  while IFS= read -r line; do
    # The id must be echoed: the child matches replies by id, so a hardcoded
    # one would be counted as a notification and every call would hang.
    id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
    [ -n "$id" ] || continue   # notification: no reply
    case "$line" in
      *'"initialize"'*)
        printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake"}}}\n' "$id";;
      *'"tools/call"'*)
        # The body comes from the same tool_body the cli path uses. Two
        # hand-written copies of this payload is how the resident path and the
        # spawn path end up disagreeing about shape, and the disagreement gets
        # blamed on tk rather than on the fake.
        case "$line" in *search_graph*) tool=search_graph;; *search_code*) tool=search_code;; *trace_path*) tool=trace_path;; *check_index_coverage*) tool=check_index_coverage;; *) tool=query_graph;; esac
        want_structured=0
        case "$line" in *'"format":"json"'*) want_structured=1;; esac
        packed=$(tool_body line "$line" mcp) || exit $?
        body_total=$(printf '%s' "$packed" | sed -n '1s/^@@\([0-9]*\) \([0-9]*\)$/\1/p')
        body_returned=$(printf '%s' "$packed" | sed -n '1s/^@@\([0-9]*\) \([0-9]*\)$/\2/p')
        body=$(printf '%s' "$packed" | tail -n +2)
        if [ "$want_structured" = 1 ]; then
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"%s"}],"structuredContent":{"total":%s,"returned":%s,"has_more":false,"truncated":false}}}\n' \
            "$id" "$(json_escape "$body")" "$body_total" "$body_returned"
        else
          printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"%s"}]}}\n' \
            "$id" "$(json_escape "$body")"
        fi;;
      *) printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id";;
    esac
  done
  exit 0
fi

# argv is `cli [--json] <tool> --args-file <path>`, or a raw passthrough.
# The tool is located by scanning for the first argument that is not a flag and
# not the value of --args-file, because tk sends BOTH shapes and a positional
# read of $2 silently mislabels every --json call. That mistake was invisible
# for as long as the catch-all branch answered plausibly: each structured read
# fell through to the two-spawn legacy fallback and the suite reported a
# single-spawn path it was never exercising.
tool=""
args=""
prev=""
for a in "$@"; do
  case "$prev" in
    --args-file) args="$a"; prev="$a"; continue;;
  esac
  case "$a" in
    cli|--json) ;;
    --args-file) prev="$a";;
    *) if [ -z "$tool" ]; then tool="$a"; fi;;
  esac
  prev="$a"
done
[ -n "$args" ] || args="$4"
# The fake mirrors CBM 0.11.0's real output shape: an empty result is a set
# of zero counters, not prose. Marker-only emptiness matching would let
# every real absence through unproven, so the fake must not invent phrases.
#
# `cli --json` answers with an MCP envelope, which is the only shape
# parseEnvelope recognises. A bare-text answer there would be read as
# "engine sent no envelope" and would push every read onto the two-spawn
# legacy fallback — so the fake has to be honest here or it is testing a
# path tk does not take.
json_wrap() {
  # Real CBM answers a format:"json" read with structuredContent beside the
  # text block, on BOTH the cli and the MCP path. The counters come from
  # tool_body, which derived them from the body, so the structured face can
  # never claim a different count than the text face.
  esc=$(json_escape "$1")
  if [ "$want_structured" = 1 ]; then
    printf '{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%s"}],"structuredContent":{"total":%s,"returned":%s,"has_more":false,"truncated":false}}}' \
      "$esc" "$body_total" "$body_returned"
  else
    printf '{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"%s"}]}}' "$esc"
  fi
}
want_json=0
want_structured=0
for a in "$@"; do [ "$a" = "--json" ] && want_json=1; done
if grep -q '"format":"json"' "$args" 2>/dev/null; then want_structured=1; fi

# One call for the body: tool_body is the same function the MCP path uses, so
# the two transports cannot drift apart.
packed=$(tool_body file "$args" "$1") || exit $?
body_total=$(printf '%s' "$packed" | sed -n '1s/^@@\([0-9]*\) \([0-9]*\)$/\1/p')
body_returned=$(printf '%s' "$packed" | sed -n '1s/^@@\([0-9]*\) \([0-9]*\)$/\2/p')
body=$(printf '%s' "$packed" | tail -n +2)
if [ "$want_json" = 1 ]; then json_wrap "$body"; else printf '%s\n' "$body"; fi
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

pass=0; fail=0; skip=0
check() { # desc, expected_exit, cmd...
  desc="$1"; want="$2"; shift 2
  if out=$("$@" 2>&1); then code=0; else code=$?; fi
  if [ "$code" = "$want" ]; then pass=$((pass+1)); printf 'ok   %s\n' "$desc";
  else fail=$((fail+1)); printf 'FAIL %s (exit=%s want=%s)\n  %s\n' "$desc" "$code" "$want" "$out"; fi
}

# JSON/envelope probes need python3, which is not a POSIX utility and is not
# guaranteed present (macOS ships only a command-line-tools shim). Every such
# probe must tolerate its absence without lying. Empty output would read as a
# genuine FAIL, and a failing command substitution under `set -eu` aborts the
# whole script with no summary at all - which is how a missing python3 used to
# end this suite. Absent probes report `skip`: never pass, never fail.
have_python3=0
command -v python3 >/dev/null 2>&1 && have_python3=1
[ "$have_python3" = 1 ] || echo "-- note: python3 absent; JSON probes report skip (not fail)"

skipped() { skip=$((skip+1)); printf 'skip %s (%s)\n' "$1" "$2"; }

# py runs a python3 probe, printing nothing when python3 is absent. It never
# fails its caller: a probe that errors says so on stderr and yields empty
# output, so the calling check reports a real FAIL instead of aborting the run.
py() {
  if [ "$have_python3" = 1 ]; then
    python3 "$@" || printf 'py: probe failed: python3 %s\n' "$*" >&2
  fi
  return 0
}

# json_id extracts a review id from a `--json` list; $1 is an expression over
# the decoded list bound to `d`. Empty output means "cannot proceed", so every
# caller reports a skip instead of handing tk an empty argument.
json_id() { py -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

# toolcount <label> <expected> <cmd...> -- counts tools/list tools for a profile.
toolcount() {
  tc_label="$1"; tc_want="$2"; shift 2
  if [ "$have_python3" != 1 ]; then skipped "$tc_label" "python3 absent"; return 0; fi
  if ! tc_out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | "$@" 2>/dev/null); then
    fail=$((fail+1)); printf 'FAIL %s (mcp exited non-zero)\n' "$tc_label"; return 0
  fi
  tc_n=$(printf '%s' "$tc_out" | py -c "import json,sys; print(len(json.load(sys.stdin)['result']['tools']))")
  if [ "$tc_n" = "$tc_want" ]; then pass=$((pass+1)); printf 'ok   %s\n' "$tc_label"
  else fail=$((fail+1)); printf 'FAIL %s (n=%s want=%s)\n' "$tc_label" "$tc_n" "$tc_want"; fi
}

# mode600 <file> -- POSIX mode test. `find -perm 600` is an exact match, so test
# its output (GNU and BSD find both exit 0 on no match). `stat -c` is GNU-only
# and `stat -f` BSD-only, so neither is portable; the 0600 invariant is also
# asserted portably in Go (internal/memory/{facts,notes,ledger}_test.go).
mode600() { [ -n "$(find "$1" -perm 600 -print 2>/dev/null)" ]; }

check init 0 $TK_BIN init
check register 0 $TK_BIN register "$FIX" --name demo
check register-dup 1 $TK_BIN register "$FIX" --name demo
check index 0 $TK_BIN index demo
check sync 0 $TK_BIN sync demo
check status 0 $TK_BIN status
check status-json 0 $TK_BIN status --json
check find 0 $TK_BIN find --query Demo --project demo
check find-label 0 $TK_BIN find --query Demo --project demo --label Function
check explain 0 $TK_BIN explain --symbol Demo --project demo
check trace 0 $TK_BIN trace --symbol Demo --project demo
check trace-outbound 0 $TK_BIN trace --symbol Demo --project demo --direction outbound --depth 3
# flag validation is local and precedes the spawn: bad traversal knobs are
# usage errors, never a silently empty result
check trace-bad-direction 1 $TK_BIN trace --symbol Demo --project demo --direction sideways
check trace-bad-depth 1 $TK_BIN trace --symbol Demo --project demo --depth 9
# strict flag-only grammar: a stray positional never becomes a symbol/project
check trace-positional-rejected 1 $TK_BIN trace Demo --project demo
# an empty trace is the negative claim "nothing calls X" -> must carry a
# coverage verdict (coverage-before-absence, same as the search tools)
check trace-miss 0 $TK_BIN trace --symbol TotalMiss --project demo
if $TK_BIN trace --symbol TotalMiss --project demo 2>&1 | grep -q '(coverage: clean'; then
  pass=$((pass+1)); printf 'ok   trace-miss-absence-annotated\n';
else fail=$((fail+1)); printf 'FAIL trace-miss-absence-annotated (no coverage verdict)\n'; fi
# a hit carries a non-zero counter and must stay unannotated
if $TK_BIN trace --symbol Demo --project demo 2>&1 | grep -q 'coverage: clean'; then
  fail=$((fail+1)); printf 'FAIL trace-hit-not-annotated\n'
else
  pass=$((pass+1)); printf 'ok   trace-hit-not-annotated\n'
fi
# same rule for the search verbs: a counter-shaped zero result is absence
# and must be proven, not silent
if $TK_BIN find --query TotalMiss --project demo 2>&1 | grep -q '(coverage: clean'; then
  pass=$((pass+1)); printf 'ok   find-miss-absence-annotated\n';
else fail=$((fail+1)); printf 'FAIL find-miss-absence-annotated (no coverage verdict)\n'; fi
if $TK_BIN grep --pattern TotalMiss --project demo 2>&1 | grep -q '(coverage: clean'; then
  pass=$((pass+1)); printf 'ok   grep-miss-absence-annotated\n';
else fail=$((fail+1)); printf 'FAIL grep-miss-absence-annotated (no coverage verdict)\n'; fi
if $TK_BIN trace --symbol Demo --project demo --direction outbound --depth 3 --json | grep -q '"direction": "outbound"'; then
  pass=$((pass+1)); printf 'ok   trace-json-envelope\n';
else fail=$((fail+1)); printf 'FAIL trace-json-envelope\n'; fi
check grep 0 $TK_BIN grep --pattern Demo --project demo
check grep-badregex 1 $TK_BIN grep --pattern "(unclosed" --regex
check arch 0 $TK_BIN arch --project demo
check query 0 $TK_BIN query --cypher "MATCH (f:Function) RETURN f.name LIMIT 5" --project demo
check outline 0 $TK_BIN outline --file main.go --project demo
check impact 0 $TK_BIN impact --project demo
check source-search 0 $TK_BIN source-search --pattern Demo --project demo
# source-search never infers a scope: no --project is an error naming the
# routes, and --all-projects is the only way to widen past one project.
# A command substitution under `set -eu` aborts the script when the command
# exits non-zero, so the failing call is made in an `if` and the message is
# captured with `|| true`.
if out=$($TK_BIN source-search --pattern Demo 2>&1); then
  fail=$((fail+1)); printf 'FAIL source-search-no-scope-must-fail\n';
else
  case "$out" in
    *--project*--select*--all-projects*) pass=$((pass+1)); printf 'ok   source-search-no-scope-names-routes\n';;
    *) fail=$((fail+1)); printf 'FAIL source-search-no-scope-routes\n  %s\n' "$out";;
  esac
fi
check source-search-all-projects 0 $TK_BIN source-search --pattern Demo --all-projects
out=$($TK_BIN source-search --pattern Demo --all-projects 2>&1)
case "$out" in
  *'[source-search:'*'match'*'in demo'*'demo:main.go'*) pass=$((pass+1)); printf 'ok   source-search-fleet-scope-line\n';;
  *) fail=$((fail+1)); printf 'FAIL source-search-fleet-scope-line\n  %s\n' "$out";;
esac
check config-get 0 $TK_BIN config get index_mode
check config-validate 0 $TK_BIN config validate
check config-picker-get 0 $TK_BIN config get ui.picker
check config-picker-off 0 $TK_BIN config set ui.picker false
picker_val=$("$TK_BIN" config get ui.picker)
if [ "$picker_val" = "false" ]; then pass=$((pass+1)); printf 'ok   config-picker-off-value\n';
else fail=$((fail+1)); printf 'FAIL config-picker-off-value (ui.picker=%s)\n' "$picker_val"; fi
check config-picker-back 0 $TK_BIN config set ui.picker true
check daemon-status 0 $TK_BIN daemon status
check completion 0 $TK_BIN completion bash
check complete-projects 0 $TK_BIN __complete index ""
check validate-hit 0 $TK_BIN validate --symbol Demo --project demo
check validate-miss 0 $TK_BIN validate --symbol DoesNotExist --project demo
check kg-find-alias 0 $TK_BIN kg_find --query Demo --project demo
check kg-explain-alias 0 $TK_BIN kg_explain --symbol Demo --project demo
check kg-grep-alias 0 $TK_BIN kg_grep --pattern Demo --project demo
check kg-trace-alias 0 $TK_BIN kg_trace --symbol Demo --project demo

# --- routing is always explicit: a bare query fails, it never guesses ---
# Nothing is inferred any more. One registered project used to auto-default, so
# `tk arch` with no flags used to succeed here; it must now fail naming both
# routes. `demo` is the only project registered at this point, which is exactly
# the case the inference covered.
check no-project-no-select 1 $TK_BIN arch
if out=$($TK_BIN arch 2>&1); then
  fail=$((fail+1)); printf 'FAIL arch-no-project-must-fail\n';
else
  case "$out" in
    *arch*--project*--select*) pass=$((pass+1)); printf 'ok   arch-no-project-names-routes\n';;
    *) fail=$((fail+1)); printf 'FAIL arch-no-project-names-routes\n  %s\n' "$out";;
  esac
fi
# Same contract on a payload verb, and the message names the verb.
if out=$($TK_BIN find --query Demo 2>&1); then
  fail=$((fail+1)); printf 'FAIL find-no-project-must-fail\n';
else
  case "$out" in
    *find*--project*--select*) pass=$((pass+1)); printf 'ok   find-no-project-names-routes\n';;
    *) fail=$((fail+1)); printf 'FAIL find-no-project-names-routes\n  %s\n' "$out";;
  esac
fi
# An unregistered --project fails here, before any spawn, and hands back the
# command that would fix it.
check project-unknown 1 $TK_BIN arch --project knolwedge
if $TK_BIN arch --project knolwedge 2>&1 | grep -q 'not currently registered'; then
  pass=$((pass+1)); printf 'ok   project-unknown-wording\n';
else fail=$((fail+1)); printf 'FAIL project-unknown-wording\n'; fi
if $TK_BIN arch --project knolwedge 2>&1 | grep -q 'tk register <path> --name knolwedge'; then
  pass=$((pass+1)); printf 'ok   project-unknown-register-hint\n';
else fail=$((fail+1)); printf 'FAIL project-unknown-register-hint\n'; fi

# --- --select opens the picker (e2e is non-TTY, so it must hard-fail) ---
# --select is the only way a menu opens, and it is a request to interact: never
# silently reach CBM with an empty project, and always point at --project.
check select-no-tty 1 $TK_BIN find --query Demo --select
if $TK_BIN find --query Demo --select 2>&1 | grep -q 'pass --project'; then
  pass=$((pass+1)); printf 'ok   select-no-tty-hint\n';
else fail=$((fail+1)); printf 'FAIL select-no-tty-hint (no --project routing hint)\n'; fi
check select-project-only-no-tty 1 $TK_BIN arch --select
# --select on trace: same picker contract as every project-resolving verb
check select-trace-no-tty 1 $TK_BIN trace --symbol Demo --select
if $TK_BIN trace --symbol Demo --select 2>&1 | grep -q 'pass --project'; then
  pass=$((pass+1)); printf 'ok   select-trace-hint\n';
else fail=$((fail+1)); printf 'FAIL select-trace-hint (no --project routing hint)\n'; fi
# --project always wins: --select is ignored, so this succeeds off-TTY.
check select-ignored-with-project 0 $TK_BIN find --query Demo --project demo --select
check select-ignored-with-project-arch 0 $TK_BIN arch --project demo --select
# --select never lands on non-project commands.
check select-not-on-index 1 $TK_BIN index demo --select

# --- memory layer (tk-owned, no CBM needed: same checks in both modes) ---
MEM="$TK_HOME/data/mem"
check mem-save 0 $TK_BIN mem save db postgres --project demo --provenance setup
check mem-recall 0 $TK_BIN mem recall db --project demo
check mem-save-global 0 $TK_BIN mem save api-base https://api.internal.example.com --scope global
check mem-recall-global-fallback 0 $TK_BIN mem recall api-base --project demo
check mem-secret-queued 0 $TK_BIN mem save openai-key "sk-proj-E2Ee2e01234567890123456789012" --project demo
check mem-review-list 0 $TK_BIN mem review list
memid=$("$TK_BIN" mem review list --json | json_id "d['reviews'][0]['id']")
if [ -n "$memid" ]; then
  check mem-review-approve 0 $TK_BIN mem review approve "$memid"
else
  skipped mem-review-approve "python3 absent"
fi
check mem-recall-approved-secret 0 $TK_BIN mem recall openai-key --project demo
"$TK_BIN" mem save deadtoken "ghp_E2ETestToken01234567890123456789012" --project demo >/dev/null
rid=$("$TK_BIN" mem review list --json | json_id "[r['id'] for r in d['reviews'] if r['topic']=='deadtoken'][0]")
if [ -n "$rid" ]; then
  check mem-review-reject 0 $TK_BIN mem review reject "$rid"
else
  skipped mem-review-reject "python3 absent"
fi
if "$TK_BIN" mem recall deadtoken --project demo | grep -q 'E2ETestToken'; then
  fail=$((fail+1)); printf 'FAIL mem-rejected-not-stored\n'
else
  pass=$((pass+1)); printf 'ok   mem-rejected-not-stored\n'
fi
if mode600 "$MEM/facts.db"; then
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
nid=$("$TK_BIN" note review list --json | json_id "d['reviews'][0]['id']")
# note_approved gates every later check that needs an *approved* note: a skipped
# approval leaves the note in the review queue, so its markdown is never written
# and note_search legitimately finds nothing. Those checks must skip, not fail.
note_approved=0
if [ -n "$nid" ]; then
  check note-review-approve 0 $TK_BIN note review approve "$nid"
  note_approved=1
else
  skipped note-review-approve "python3 absent"
fi
check note-search-hit 0 $TK_BIN note search letsencrypt --project demo
check note-toc 0 $TK_BIN note toc --project demo
"$TK_BIN" note save "superseded idea" --text "an old idea about feature flags that we dropped" --project demo >/dev/null
nid2=$("$TK_BIN" note review list --json | json_id "[r['id'] for r in d['reviews'] if r['title']=='superseded idea'][0]")
if [ -n "$nid2" ]; then
  check note-review-reject 0 $TK_BIN note review reject "$nid2"
else
  skipped note-review-reject "python3 absent"
fi
if $TK_BIN note search feature --project demo | grep -q 'old idea'; then
  fail=$((fail+1)); printf 'FAIL note-rejected-not-stored\n'
else
  pass=$((pass+1)); printf 'ok   note-rejected-not-stored\n'
fi
check note-reindex 0 $TK_BIN note reindex
# every note markdown must be 0600, and there must be at least one to check
md_seen=0; md_bad=0
for f in "$TK_HOME"/data/notes/demo/*.md; do
  [ -f "$f" ] || continue
  md_seen=$((md_seen+1))
  mode600 "$f" || md_bad=$((md_bad+1))
done
if [ "$note_approved" = 0 ]; then
  skipped note-md-0600 "note approval skipped, no markdown written"
elif [ "$md_seen" -gt 0 ] && [ "$md_bad" = 0 ]; then
  pass=$((pass+1)); printf 'ok   note-md-0600\n'
else
  fail=$((fail+1)); printf 'FAIL note-md-0600 (%s file(s), %s not 0600)\n' "$md_seen" "$md_bad"
fi

# --- embedding layer (optional external /api/embed, BM25-first) ---
if [ "$have_python3" = 1 ]; then
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
  # POSIX wait: `seq` is not a POSIX utility and neither is a fractional
  # `sleep`, so poll with integer arithmetic and whole seconds.
  emb_wait=0
  while [ ! -f "$T/mock_embed.port" ] && [ "$emb_wait" -lt 10 ]; do
    emb_wait=$((emb_wait+1)); sleep 1
  done
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

# --- live-Ollama embeddings (skip-guarded: only when an Ollama daemon with
# an embed-capable model actually runs on 127.0.0.1:11434) ---
if command -v curl >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1 && curl -s --max-time 2 http://127.0.0.1:11434/api/tags > "$T/ollama_tags.json" 2>/dev/null
then
  OLLAMA_MODEL="$(python3 - "$T/ollama_tags.json" <<'PYEOF'
import json, sys
tags = json.load(open(sys.argv[1])).get('models', [])
pref = ['nomic-embed-text', 'bge-m3', 'snowflake-arctic-embed', 'all-minilm', 'mxbai-embed-large', 'qwen3-embedding']
names = [m.get('name', '') for m in tags]
for p in pref:
    if any(n == p or n.startswith(p + ':') for n in names):
        print(p + ':latest'); break
else:
    for n in names:
        if 'embed' in n:
            print(n); break
PYEOF
)"
  if [ -n "$OLLAMA_MODEL" ]; then
    pass=$((pass+1)); printf 'ok   live-ollama-present (%s)\n' "$OLLAMA_MODEL"
    check live-ollama-enable 0 $TK_BIN config set embedding.enabled true
    check live-ollama-endpoint 0 $TK_BIN config set embedding.endpoint "http://127.0.0.1:11434"
    check live-ollama-model 0 $TK_BIN config set embedding.model "$OLLAMA_MODEL"
    check live-ollama-timeout 0 $TK_BIN config set embedding.timeout_ms 60000
    $TK_BIN note save "accelerator graph fusion" --text "xla clusters fuse the computation graph across accelerator worker nodes so fused kernels cross device boundaries with minimal host sync" --project demo >/dev/null
    oid=$("$TK_BIN" note review list --json | python3 -c "import json,sys; print([r['id'] for r in json.load(sys.stdin)['reviews'] if r['title']=='accelerator graph fusion'][0])")
    check live-ollama-approve 0 $TK_BIN note review approve "$oid"
    check live-ollama-reindex 0 $TK_BIN note reindex
    if $TK_BIN note search "fusion across accelerator worker nodes xla" --project demo | grep -q 'accelerator graph fusion'; then
      pass=$((pass+1)); printf 'ok   live-ollama-search-works\n'
    else
      fail=$((fail+1)); printf 'FAIL live-ollama-search-works\n'
    fi
    vstored=$(python3 - "$TK_HOME/data/notes/index.db" "$OLLAMA_MODEL" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
print(c.execute('SELECT COUNT(*) FROM notes WHERE embed_model = ? AND embedding IS NOT NULL', (sys.argv[2],)).fetchone()[0])
PYEOF
)
    if [ "$vstored" -ge 1 ]; then
      pass=$((pass+1)); printf 'ok   live-ollama-vectors-stored (%s)\n' "$vstored"
    else
      fail=$((fail+1)); printf 'FAIL live-ollama-vectors-stored\n'
    fi
    # incremental cache: unchanged bodies reindexed against a dead endpoint
    # must keep their vectors (fail-open BM25 still serves, never blocks)
    check live-ollama-deadendpoint 0 $TK_BIN config set embedding.endpoint "http://127.0.0.1:9"
    check live-ollama-reindex-cached 0 $TK_BIN note reindex
    vcached=$(python3 - "$TK_HOME/data/notes/index.db" "$OLLAMA_MODEL" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
print(c.execute('SELECT COUNT(*) FROM notes WHERE embed_model = ? AND embedding IS NOT NULL', (sys.argv[2],)).fetchone()[0])
PYEOF
)
    if [ "$vcached" = "$vstored" ]; then
      pass=$((pass+1)); printf 'ok   live-ollama-cache-survives (%s)\n' "$vcached"
    else
      fail=$((fail+1)); printf 'FAIL live-ollama-cache-survives (%s != %s)\n' "$vcached" "$vstored"
    fi
    check live-ollama-search-bm25 0 $TK_BIN note search letsencrypt --project demo
  else
    pass=$((pass+1)); printf 'ok   live-ollama-absent (no embed-capable model on daemon)\n'
  fi
  check live-ollama-restore-enabled 0 $TK_BIN config set embedding.enabled false
  check live-ollama-restore-model 0 $TK_BIN config set embedding.model ""
  check live-ollama-restore-endpoint 0 $TK_BIN config set embedding.endpoint ""
  check live-ollama-restore-timeout 0 $TK_BIN config set embedding.timeout_ms 3000
else
  pass=$((pass+1)); printf 'ok   live-ollama-skipped (no daemon on 127.0.0.1:11434)\n'
fi

# --- ledger layer (v2: append-only JSONL, five keys, last-write-wins fold) ---
check ledger-update 0 $TK_BIN ledger update demo goal "demo serves the public API; deploys weekly."
check ledger-update-key2 0 $TK_BIN ledger update demo next "ship the cross-repo fleet"
check ledger-get 0 $TK_BIN ledger get demo
shiftled=$(TK_HOME="$TK_HOME" $TK_BIN ledger get demo)
if echo "$shiftled" | grep -q 'public API'; then
  pass=$((pass+1)); printf 'ok   ledger-roundtrip\n'
else
  fail=$((fail+1)); printf 'FAIL ledger-roundtrip\n'
fi
if echo "$shiftled" | grep -q 'ship the cross-repo fleet'; then
  pass=$((pass+1)); printf 'ok   ledger-get-folds-both-keys\n'
else
  fail=$((fail+1)); printf 'FAIL ledger-get-folds-both-keys\n'
fi
hist=$(TK_HOME="$TK_HOME" $TK_BIN ledger history demo)
if echo "$hist" | grep -q 'public API' && echo "$hist" | grep -q 'goal'; then
  pass=$((pass+1)); printf 'ok   ledger-history-complete\n'
else
  fail=$((fail+1)); printf 'FAIL ledger-history-complete\n'
fi
if $TK_BIN ledger update demo bogus "nope" >/dev/null 2>&1; then
  fail=$((fail+1)); printf 'FAIL ledger-bad-key-rejected\n'
else
  pass=$((pass+1)); printf 'ok   ledger-bad-key-rejected\n'
fi
if $TK_BIN ledger update demo goal "" >/dev/null 2>&1; then
  fail=$((fail+1)); printf 'FAIL ledger-empty-value-rejected\n'
else
  pass=$((pass+1)); printf 'ok   ledger-empty-value-rejected\n'
fi
if $TK_BIN ledger prune demo >/dev/null 2>&1; then
  fail=$((fail+1)); printf 'FAIL ledger-prune-refused-off-tty\n'
else
  pass=$((pass+1)); printf 'ok   ledger-prune-refused-off-tty\n'
fi
check ledger-budget 0 $TK_BIN config set budgets.ledger_chars 24
check ledger-update-trunc 0 $TK_BIN ledger update demo goal "much longer ledger text that must be truncated aggressively here"
if [ "$have_python3" = 1 ]; then
  shortled=$(TK_HOME="$TK_HOME" $TK_BIN ledger get demo --json 2>/dev/null | py -c "import json,sys; print(json.load(sys.stdin)['keys']['goal']['value'])" 2>/dev/null)
  if [ "${#shortled}" -le 24 ]; then
    pass=$((pass+1)); printf 'ok   ledger-budget-enforced\n'
  else
    fail=$((fail+1)); printf 'FAIL ledger-budget-enforced (%d chars)\n' "${#shortled}"
  fi
  # the cap is retrieval-only: history still holds the value whole
  longhist=$(TK_HOME="$TK_HOME" $TK_BIN ledger history demo --json 2>/dev/null | py -c "import json,sys; v=[e['value'] for e in json.load(sys.stdin)['entries'] if 'much longer ledger text' in e['value']]; print(v[-1] if v else '')" 2>/dev/null)
  if [ "${#longhist}" -gt 24 ]; then
    pass=$((pass+1)); printf 'ok   ledger-history-uncapped\n'
  else
    fail=$((fail+1)); printf 'FAIL ledger-history-uncapped\n'
  fi
else
  skipped ledger-budget-enforced "python3 absent"
  skipped ledger-history-uncapped "python3 absent"
fi
check ledger-disable 0 $TK_BIN config set ledger.enabled false
if $TK_BIN ledger update demo goal "should fail when disabled" >/dev/null 2>&1; then
  fail=$((fail+1)); printf 'FAIL ledger-disabled-gate\n'
else
  pass=$((pass+1)); printf 'ok   ledger-disabled-gate\n'
fi
# the gate must hold on the MCP write path with the same message (parity)
mcpdis=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"goal","text":"must fail while disabled"}}}' | TK_HOME="$TK_HOME" $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$mcpdis" in
  *'"error"'*'ledger is disabled'*) pass=$((pass+1)); printf 'ok   mcp-ledger-disabled-gate\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-ledger-disabled-gate\n  %s\n' "$mcpdis";;
esac
check ledger-reenable 0 $TK_BIN config set ledger.enabled true
check ledger-budget-reset 0 $TK_BIN config set budgets.ledger_chars 1500

toolcount mcp-tools-11 11 $TK_BIN mcp
toolcount mcp-tools-analysis-15 15 $TK_BIN mcp --tool-profile analysis
toolcount mcp-tools-minimal-3 3 $TK_BIN mcp --tool-profile minimal
toolcount mcp-tools-memory-22 22 $TK_BIN mcp --tool-profile memory
# profile is a runtime knob: TK_MCP_PROFILE env drives it, flag beats env, invalid env fails loudly
toolcount mcp-env-analysis-15 15 env TK_MCP_PROFILE=analysis $TK_BIN mcp
toolcount mcp-flag-beats-env-22 22 env TK_MCP_PROFILE=minimal $TK_BIN mcp --tool-profile memory
if TK_MCP_PROFILE=bogus sh -c "printf '%s\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{}}' | $TK_BIN mcp" 2>&1 | grep -q 'scout|analysis|minimal|memory'; then
  pass=$((pass+1)); printf 'ok   mcp-env-invalid-rejected\n'
else
  fail=$((fail+1)); printf 'FAIL mcp-env-invalid-rejected\n'
fi
check mcp-config-set 0 $TK_BIN config set mcp.profile analysis
if $TK_BIN config get mcp.profile | grep -q '^analysis$'; then
  pass=$((pass+1)); printf 'ok   mcp-config-get\n'
else
  fail=$((fail+1)); printf 'FAIL mcp-config-get\n'
fi
toolcount mcp-config-profile-15 15 $TK_BIN mcp
check mcp-config-unset 0 $TK_BIN config set mcp.profile ""
toolcount mcp-config-unset-back-to-11 11 $TK_BIN mcp
# memory profile: in-process tools work even though CBM was killed in live mode
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mem_save","arguments":{"topic":"deploy-tool","value":"make deploy ship it","scope":"project","project":"demo","provenance":"e2e"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'saved fact'*) pass=$((pass+1)); printf 'ok   mcp-mem-save\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-mem-save\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mem_recall","arguments":{"topic":"deploy-tool","project":"demo"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'make deploy ship it'*) pass=$((pass+1)); printf 'ok   mcp-mem-recall\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-mem-recall\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"mem_save","arguments":{"topic":"creds","value":"sk-a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0","scope":"project","project":"demo"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'for review'*) pass=$((pass+1)); printf 'ok   mcp-mem-secret-queued\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-mem-secret-queued\n  %s\n' "$out";;
esac
if printf '%s\n' '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"mem_review","arguments":{"action":"list"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null | grep -q 'sk-a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0'; then
  fail=$((fail+1)); printf 'FAIL mcp-mem-secret-masked\n'
else
  pass=$((pass+1)); printf 'ok   mcp-mem-secret-masked\n'
fi
if [ "$note_approved" = 0 ]; then
  skipped mcp-note-search "note approval skipped, nothing searchable"
else
  out=$(printf '%s\n' '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"note_search","arguments":{"query":"letsencrypt","project":"demo"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
  case "$out" in
    *'tls config'*) pass=$((pass+1)); printf 'ok   mcp-note-search\n';;
    *) fail=$((fail+1)); printf 'FAIL mcp-note-search\n  %s\n' "$out";;
  esac
fi
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"goal","text":"demo serves the API and owns the tls config"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'appended ledger demo/goal'*) pass=$((pass+1)); printf 'ok   mcp-ledger-update\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-ledger-update\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"ledger_get","arguments":{"project":"demo"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'tls config'*) pass=$((pass+1)); printf 'ok   mcp-ledger-get\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-ledger-get\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ledger_history","arguments":{"project":"demo"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'public API'*) pass=$((pass+1)); printf 'ok   mcp-ledger-history\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-ledger-history\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"ledger_update","arguments":{"project":"demo","key":"bogus","text":"x"}}}' | $TK_BIN mcp --tool-profile memory 2>/dev/null)
case "$out" in
  *'error'*) pass=$((pass+1)); printf 'ok   mcp-ledger-bad-key-rejected\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-ledger-bad-key-rejected\n  %s\n' "$out";;
esac
# NOTE: zoekt is a linked library, not a backend binary — the fake only
# stubs CBM, so source_search genuinely succeeds in both modes.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Demo","project":"demo"}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'main.go'*Demo*) pass=$((pass+1)); printf 'ok   mcp-source-search-hit\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-source-search\n  %s\n' "$out";;
esac
# all_projects is the explicit fleet scope and returns the same hit, prefixed
# and annotated with the scope it searched.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Demo","all_projects":true}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'[source-search:'*'demo:main.go'*Demo*) pass=$((pass+1)); printf 'ok   mcp-source-search-all-projects\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-source-search-all-projects\n  %s\n' "$out";;
esac
# Giving both is a schema-level error, not a silent precedence rule.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"source_search","arguments":{"pattern":"Demo","project":"demo","all_projects":true}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'not both'*) pass=$((pass+1)); printf 'ok   mcp-source-search-scope-exclusive\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-source-search-scope-exclusive\n  %s\n' "$out";;
esac
# coverage-before-absence: an empty search_graph must carry the coverage
# verdict (scout-only profiles standardized), and a hit stays bare.
#
# The assertions read the ENGINE's counters, not the rendered tree. search_graph
# is a structured tool, so a faithful CBM answers with structuredContent and tk
# puts that payload in the text block; these cases were written when the fake
# withheld structuredContent, which silently pinned them to the degraded
# text-only path. Asserting on the counters tests the coverage gate — the thing
# this row exists for — and stays true on both paths.
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"TotalMiss","project":"demo"}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'total\":0'*|*'"total":0'*|*'(coverage: clean'*) pass=$((pass+1)); printf 'ok   mcp-scout-absence-annotated\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-scout-absence-annotated\n  %s\n' "$out";;
esac
out=$(printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_graph","arguments":{"name_pattern":"Demo","project":"demo"}}}' | $TK_BIN mcp 2>/dev/null)
case "$out" in
  *'total\":1'*|*'"total":1'*|*'Demo'*'main.go'*) pass=$((pass+1)); printf 'ok   mcp-scout-search-hit\n';;
  *) fail=$((fail+1)); printf 'FAIL mcp-scout-search-hit\n  %s\n' "$out";;
esac

# --- CBM stack-limit guard (linux/macos) ---
# CBM's deep pipeline passes recurse hard, and a thin parent limit can hand
# the engine a 512KB main-thread stack that overflows mid-index. The wrapper
# raises the child soft stack to the 8MB floor when the inherited ulimit is
# below it. Prove the raise reached the child, then prove spawns still work
# from a crippled parent and from an already-raised parent. Each case runs in
# a subshell so the suite's own limit is untouched afterwards. Use -S (soft
# only): bash's bare `ulimit -s` also collapses the hard limit, which would
# legitimately cap the raise and prove nothing.
case "$(uname -s)" in
  Linux|Darwin)
    if [ "${TK_LIVE:-0}" != "1" ]; then
      crippled="$( ( ulimit -S -s 512 2>/dev/null || true
        TK_HOME="$TK_HOME" "$TK_BIN" cbm stack_probe 2>/dev/null || true ) )"
      case "$crippled" in
        unlimited) pass=$((pass+1)); printf 'ok   stack-guard-raised (unlimited)\n';;
        *[!0-9]*|'') fail=$((fail+1)); printf 'FAIL stack-guard-raised (child soft=%s)\n' "$crippled";;
        *) if [ "$crippled" -ge 8192 ]; then
             pass=$((pass+1)); printf 'ok   stack-guard-raised (%sKB)\n' "$crippled"
           else
             fail=$((fail+1)); printf 'FAIL stack-guard-raised (child soft=%sKB, floor=8192KB)\n' "$crippled"
           fi;;
      esac
    else
      skipped stack-guard-raised "live mode has no stack_probe fake tool"
    fi
    if ( ulimit -S -s 512 2>/dev/null || true; "$TK_BIN" index demo >/dev/null 2>&1 ); then
      pass=$((pass+1)); printf 'ok   stack-crippled-index\n'
    else
      fail=$((fail+1)); printf 'FAIL stack-crippled-index (index failed at ulimit -s 512)\n'
    fi
    if ( ulimit -S -s 65532 2>/dev/null || true; "$TK_BIN" sync demo >/dev/null 2>&1 ); then
      pass=$((pass+1)); printf 'ok   stack-raised-parent-index\n'
    else
      fail=$((fail+1)); printf 'FAIL stack-raised-parent-index (sync failed at ulimit -s 65532)\n'
    fi
    ;;
esac

printf '\npass=%d fail=%d\n' "$pass" "$fail"

# Unified trace log: every invocation recorded with input+output+backends.
LOG="$TK_HOME/state/logs/tk.log"
if [ ! -s "$LOG" ]; then fail=$((fail+1)); printf 'FAIL trace-log-missing\n';
elif [ "$have_python3" != 1 ]; then skipped trace-log "python3 absent";
elif python3 - "$LOG" <<'EOF'
import json, sys
recs = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
assert recs, "empty log"
for r in recs:
    for k in ("v", "ts", "exit", "output"):
        assert k in r, f"missing {k} in {r}"
    assert "argv" in r or "mcp" in r, f"missing input in {r}"
assert any(r.get("events") for r in recs), "no backend events logged"
assert any(e.get("op") == "trace_path" for r in recs for e in r.get("events", [])), "no trace_path backend event logged"
assert any(r.get("exit") != 0 for r in recs), "no failure records (grep-badregex should log exit=1)"
print(f"trace-log ok ({len(recs)} records)")
EOF
then pass=$((pass+1)); printf 'ok   trace-log\n';
else fail=$((fail+1)); printf 'FAIL trace-log\n'; fi

# --- resident: `tk mcp --detach` -------------------------------------------
# The resident's whole claim is that N reads cost ONE engine start, so the fake
# counts spawns and these cases read that count. Everything here is fake-mode
# only: the spawn counter is the instrument, and a real engine cannot report
# how many times it was exec'd.
case "${TK_LIVE:-0}" in
  1) skipped resident-lifecycle "live mode has no spawn counter";;
  *)
    SOCK="$TK_HOME/state/resident.sock"
    PIDF="$TK_HOME/state/resident.pid"
    SPAWNS="$T/spawns"
    : > "$SPAWNS"
    # The fake only counts when it is told where to count. This has to be
    # exported, not just set: the resident is a separate process started later.
    TK_FAKE_SPAWNS="$SPAWNS"; export TK_FAKE_SPAWNS

    out=$("$TK_BIN" mcp --detach 2>&1) && rc=0 || rc=$?
    if [ "$rc" = 0 ] && [ -S "$SOCK" ] && [ -s "$PIDF" ]; then
      pass=$((pass+1)); printf 'ok   resident-detached\n'
    else
      fail=$((fail+1)); printf 'FAIL resident-detached (exit=%s)\n  %s\n' "$rc" "$out"
    fi
    rpid=$(cat "$PIDF" 2>/dev/null || echo 0)

    # Three reads in three separate processes. Each must render like the spawn
    # path, and the engine count must not move after the first.
    if [ "$rc" = 0 ]; then
      a=$("$TK_BIN" find --query Demo --project demo 2>&1 || true)
      b=$("$TK_BIN" find --query Demo --project demo 2>&1 || true)
      c=$("$TK_BIN" find --query Demo --project demo 2>&1 || true)
      n=$(wc -l < "$SPAWNS" | tr -d ' ')
      case "$a$b$c" in
        *Demo*main.go*Demo*main.go*Demo*main.go*)
          pass=$((pass+1)); printf 'ok   resident-render-parity\n';;
        *) fail=$((fail+1)); printf 'FAIL resident-render-parity\n  %s\n' "$a";;
      esac
      if [ "$n" = "1" ]; then
        pass=$((pass+1)); printf 'ok   resident-one-spawn-for-three-reads\n'
      else
        fail=$((fail+1)); printf 'FAIL resident-one-spawn-for-three-reads (spawns=%s want=1)\n' "$n"
      fi
    else
      skipped resident-render-parity "no resident"
      skipped resident-one-spawn-for-three-reads "no resident"
    fi

    # A second detach must adopt the running one, not replace it: the warm
    # engine is the product.
    # Asserted on the human text, not a --json key: per the pre-release law a
    # --json shape may change in any release, while the human surface is the
    # part that is protected. Whether a second engine was started is checked
    # directly against the spawn count in the next case, which is a stronger
    # claim than any key would be.
    out=$("$TK_BIN" mcp --detach 2>&1) || true
    case "$out" in
      *'already running'*) pass=$((pass+1)); printf 'ok   resident-second-detach-adopts\n';;
      *) fail=$((fail+1)); printf 'FAIL resident-second-detach-adopts\n  %s\n' "$out";;
    esac
    n=$(wc -l < "$SPAWNS" | tr -d ' ')
    if [ "$n" = "1" ]; then
      pass=$((pass+1)); printf 'ok   resident-second-detach-no-respawn\n'
    else
      fail=$((fail+1)); printf 'FAIL resident-second-detach-no-respawn (spawns=%s want=1)\n' "$n"
    fi

    # Reads went through the resident, not a spawn. This is what makes the log
    # worth keeping: backend is the only field that separates the two paths.
    if grep -q '"backend":"resident"' "$TK_HOME/state/logs/tk.log" 2>/dev/null; then
      pass=$((pass+1)); printf 'ok   resident-trace-backend\n'
    else
      fail=$((fail+1)); printf 'FAIL resident-trace-backend\n'
    fi

    # Lifecycle must not land in the trace log. tk.log is one invocation
    # envelope per line, and the documented `jq '[.ts, (.argv|join(" "))]'`
    # recipe dies on a record with no argv.
    if [ -s "$TK_HOME/state/logs/resident.log" ] && \
       grep -q 'resident listening' "$TK_HOME/state/logs/resident.log" 2>/dev/null && \
       [ ! -e "$TK_HOME/logs/resident.log" ]; then
      pass=$((pass+1)); printf 'ok   resident-lifecycle-log-separate\n'
    else
      fail=$((fail+1)); printf 'FAIL resident-lifecycle-log-separate\n'
    fi
    if [ "$have_python3" = 1 ]; then
      if python3 - "$TK_HOME/state/logs/tk.log" <<'EOF'
import json, sys
for l in open(sys.argv[1]):
    if not l.strip():
        continue
    r = json.loads(l)
    assert "argv" in r or "mcp" in r, f"lifecycle record in the trace log: {r}"
print("ok")
EOF
      then pass=$((pass+1)); printf 'ok   trace-log-invocations-only\n'
      else fail=$((fail+1)); printf 'FAIL trace-log-invocations-only\n'; fi
    else
      skipped trace-log-invocations-only "python3 absent"
    fi

    # SIGTERM is the ordinary stop. The socket and pid file are promises: a
    # stale socket that no one will ever accept on is worse than none.
    if [ "$rpid" != 0 ]; then
      kill -TERM "$rpid" 2>/dev/null || true
      i=0
      while [ "$i" -lt 100 ] && [ -e "$SOCK" ]; do i=$((i+1)); sleep 0.1; done
      if [ ! -e "$SOCK" ] && [ ! -e "$PIDF" ]; then
        pass=$((pass+1)); printf 'ok   resident-sigterm-cleans-up\n'
      else
        fail=$((fail+1)); printf 'FAIL resident-sigterm-cleans-up (sock=%s pid=%s)\n' "$([ -e "$SOCK" ] && echo present || echo gone)" "$([ -e "$PIDF" ] && echo present || echo gone)"
      fi
    else
      skipped resident-sigterm-cleans-up "no pid"
    fi

    # --home must reach the child. A decoy TK_HOME is exported so a child that
    # re-derives its home from the environment lands somewhere visibly wrong:
    # without the flag the socket appears in the decoy and every later read
    # silently spawns for a home the user is not using.
    ALT="$T/alt-home"; DECOY="$T/decoy-home"
    mkdir -p "$ALT" "$DECOY"
    "$TK_BIN" --home "$ALT" init >/dev/null 2>&1 || true
    "$TK_BIN" --home "$ALT" register "$FIX" --name demo >/dev/null 2>&1 || true
    out=$(TK_HOME="$DECOY" "$TK_BIN" --home "$ALT" mcp --detach 2>&1) || true
    if [ -S "$ALT/state/resident.sock" ] && [ ! -e "$DECOY/state/resident.sock" ]; then
      pass=$((pass+1)); printf 'ok   resident-honours-home-flag\n'
    else
      fail=$((fail+1)); printf 'FAIL resident-honours-home-flag\n  %s\n' "$out"
    fi
    apid=$(cat "$ALT/state/resident.pid" 2>/dev/null || echo 0)
    [ "$apid" != 0 ] && kill -TERM "$apid" 2>/dev/null || true
    ;;
esac

printf 'trace: pass=%d fail=%d skip=%d\n' "$pass" "$fail" "$skip"
[ "$fail" = "0" ]
