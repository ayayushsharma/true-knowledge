// Package cbmexec is the SINGLE spawn wrapper for all graph work.
// Every CBM invocation must go through Run — never exec cbm elsewhere.
// It sets CBM_CACHE_DIR / CBM_RUNTIME_DIR / CBM_ALLOWED_ROOT, captures
// output, translates exit status, and truncates by whole lines.
package cbmexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ayayushsharma/true-knowledge/internal/cbmresolve"
	"github.com/ayayushsharma/true-knowledge/internal/config"
	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

// ErrNotFound is returned when the CBM binary is missing (fail-open upstream).
var ErrNotFound = errors.New("cbm not installed")

const defaultTimeout = 120 * time.Second

// Runner holds resolved context for spawns.
type Runner struct {
	Bin   string
	Paths paths.Paths
	Cfg   config.Config

	// formatProbed/formatOK memoize whether the resolved binary honours
	// format:"json". The probe is the absence of structuredContent, so a
	// process asks once instead of on every call — which also means a
	// binary that *rejects* the unknown arg can never fail the second
	// read. Single-threaded per process; the MCP server is the only
	// long-lived holder.
	formatProbed bool
	formatOK     bool
}

// Result is one CBM read reply: the engine's display text plus the
// structured payload when it produced one. Data is nil for a tree-format
// reply and for a binary that predates format:"json" — callers branch on
// Structured(), never on a guess.
type Result struct {
	Text string
	Data json.RawMessage
}

// Structured reports whether the engine sent a structured payload.
func (r Result) Structured() bool { return len(r.Data) > 0 }

// New resolves the binary (fail-open error, never fatal by itself).
func New(p paths.Paths, cfg config.Config) (*Runner, error) {
	bin, err := cbmresolve.Find(cfg.CBMBinary, p.Cache)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, err)
	}
	return &Runner{Bin: bin, Paths: p, Cfg: cfg}, nil
}

// Run invokes `cbm cli <tool> --args-file <json>` and returns stdout.
// The raw-JSON argv form is deprecated upstream and will be removed;
// --args-file keeps one generic wrapper across all tools.
// Empty-string values are dropped so optional fields (e.g. project on
// single-project stores is still required by CBM — callers must resolve it)
// never serialize as "".
func (r *Runner) Run(ctx context.Context, tool string, payload map[string]any) (string, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = deadline()
		defer cancel()
	}
	argsPath, cleanup, err := writeArgs(payload)
	if err != nil {
		return "", err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, r.Bin, "cli", tool, "--args-file", argsPath)
	cmd.Env = r.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		// A failed tool call is the engine's own diagnosis, usually a small
		// JSON object with `error` + `hint` on stderr. Render the hint
		// instead of dumping the raw payload at the caller.
		return "", fmt.Errorf("cbm %s failed: %s", tool, firstLine(diagnosis(msg)))
	}
	return out.String(), nil
}

// RunJSON invokes `cbm cli --json <tool> --args-file <json>` and unwraps
// the MCP envelope to display text. Any failure (unsupported flag, bad
// exit, non-envelope output) falls back to legacy Run: older binaries
// keep working, newer ones return structured payloads.
func (r *Runner) RunJSON(ctx context.Context, tool string, payload map[string]any) (string, error) {
	res, err := r.runEnvelope(ctx, tool, payload, false)
	return res.Text, err
}

// RunStructured is RunJSON with format:"json" forced, so the engine
// answers with a structuredContent object instead of a rendered table.
// Text still carries whatever the engine put in the content block, but
// Data is the payload a machine caller wants.
//
// CBM 0.11.0 accepts format:"json" on all 13 read tools and then emits
// real typed data (cols/rows tables, integer counters, hint strings) in
// the envelope's structuredContent. A binary that predates the argument
// ignores it and sends no structuredContent; that absence *is* the
// capability probe, so the caller degrades to the text path instead of
// failing, and the verdict is memoized on the Runner.
func (r *Runner) RunStructured(ctx context.Context, tool string, payload map[string]any) (Result, error) {
	return r.runEnvelope(ctx, tool, payload, true)
}

// formatJSON is the engine argument that switches a read tool from its
// rendered tree to a structured payload.
const formatJSON = "json"

// runEnvelope is the single `cli --json` spawn behind RunJSON and
// RunStructured. structured forces format:"json" and keeps
// structuredContent; either way an unsupported flag, a bad exit, or a
// non-envelope reply falls back to the legacy spawn so older binaries
// keep working.
func (r *Runner) runEnvelope(ctx context.Context, tool string, payload map[string]any, structured bool) (Result, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = deadline()
		defer cancel()
	}
	// A probed binary without format support never gets the argument again.
	if structured && r.formatProbed && !r.formatOK {
		structured = false
	}
	if structured {
		payload = withFormat(payload, formatJSON)
	}
	argsPath, cleanup, err := writeArgs(payload)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, r.Bin, "cli", "--json", tool, "--args-file", argsPath)
	cmd.Env = r.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return r.legacy(ctx, tool, payload)
	}
	env := parseEnvelope(out.String())
	if env.IsErr {
		return Result{}, fmt.Errorf("cbm %s failed: %s", tool, env.Msg)
	}
	if structured && !r.formatProbed {
		r.formatProbed, r.formatOK = true, env.Data != nil
	}
	if env.Data != nil {
		return Result{Text: env.Text, Data: env.Data}, nil
	}
	if env.Text != "" {
		return Result{Text: env.Text}, nil
	}
	// Exit 0 but no envelope: an older binary ignored --json (or format).
	// Prefer legacy output when it exists, else keep what we got.
	if legacy, lerr := r.Run(ctx, tool, withoutFormat(payload)); lerr == nil && legacy != "" {
		return Result{Text: legacy}, nil
	}
	return Result{Text: out.String()}, nil
}

// legacy re-runs a tool with format stripped, for a binary that rejected
// the argument outright. Failure is reported as-is: the engine's own
// diagnosis beats a speculative second attempt.
func (r *Runner) legacy(ctx context.Context, tool string, payload map[string]any) (Result, error) {
	out, err := r.Run(ctx, tool, withoutFormat(payload))
	if err != nil {
		return Result{}, err
	}
	return Result{Text: out}, nil
}

// deadline gives a nil context the default timeout.
func deadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultTimeout)
}

// writeArgs materializes payload as CBM's --args-file. Empty-string values
// are dropped so optional fields never serialize as "" (CBM requires
// `project` on nearly every tool, and tk resolves it rather than sending a
// blank). Returns the path plus its cleanup.
func writeArgs(payload map[string]any) (string, func(), error) {
	clean := make(map[string]any, len(payload))
	for k, v := range payload {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		clean[k] = v
	}
	rawJSON, _ := json.Marshal(clean)
	tmp, err := os.CreateTemp("", "tk-args-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("args file: %w", err)
	}
	argsPath := tmp.Name()
	if _, err := tmp.Write(rawJSON); err != nil {
		_ = tmp.Close()
		_ = os.Remove(argsPath)
		return "", nil, fmt.Errorf("args file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(argsPath)
		return "", nil, fmt.Errorf("args file: %w", err)
	}
	return argsPath, func() { _ = os.Remove(argsPath) }, nil
}

// withFormat copies payload with the engine's format argument set. The
// caller's map is never mutated: the same payload object is reused for the
// withoutFormat retry.
func withFormat(payload map[string]any, format string) map[string]any {
	out := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	out["format"] = format
	return out
}

// withoutFormat drops the format argument for a legacy retry.
func withoutFormat(payload map[string]any) map[string]any {
	if _, ok := payload["format"]; !ok {
		return payload
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "format" {
			continue
		}
		out[k] = v
	}
	return out
}

// envelope is a decoded `cbm cli --json` reply: the engine's display text,
// the structured payload when it sent one, and the error state.
type envelope struct {
	Text      string
	Data      json.RawMessage
	IsErr     bool
	Msg       string
	HasResult bool
}

// parseEnvelope decodes a CBM `cli --json` reply. Data is nil unless the
// engine sent structuredContent, which is what makes absence-of-payload
// the capability probe for format:"json".
//
// Two on-the-wire shapes are accepted. The JSON-RPC form
// (`{"result":{"content":[...]}}`) is what the MCP spec mandates; CBM
// 0.11.0's `cli --json` actually emits the bare result object
// (`{"content":[...],"isError":false}`) with no `result` wrapper, so that
// shape is unwrapped too — otherwise every read silently falls through to
// the legacy re-spawn and reports nothing. A tool that fails sets
// `isError:true` with the diagnosis in its content, which is surfaced as
// an error (with the engine's own `hint` when it supplies one) instead of
// being passed off as a result.
func parseEnvelope(out string) envelope {
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "{") {
		return envelope{}
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return envelope{}
	}
	if eraw, ok := raw["error"]; ok {
		var em struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(eraw, &em) == nil && em.Message != "" {
			return envelope{IsErr: true, Msg: firstLine(em.Message)}
		}
		return envelope{IsErr: true, Msg: "unknown CBM error"}
	}
	res, wrapped := raw["result"]
	if !wrapped {
		// Bare MCP result object (CBM 0.11.0 `cli --json`): no wrapper.
		res = json.RawMessage(trimmed)
	}
	var obj struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(res, &obj) == nil {
		env := envelope{HasResult: true}
		if sc := bytes.TrimSpace(obj.StructuredContent); len(sc) > 0 && !bytes.Equal(sc, []byte("null")) {
			env.Data = json.RawMessage(sc)
		}
		for _, c := range obj.Content {
			if c.Text != "" {
				if obj.IsError {
					return envelope{IsErr: true, Msg: firstLine(diagnosis(c.Text))}
				}
				env.Text = c.Text
				return env
			}
		}
		// A result can be structured-only; that is a real answer, not a
		// missing envelope.
		if env.Data != nil {
			return env
		}
	}
	var s string
	if json.Unmarshal(res, &s) == nil && s != "" {
		return envelope{Text: s, HasResult: true}
	}
	return envelope{}
}

// UnwrapEnvelope extracts display text from a CBM `cli --json` MCP
// envelope. Returns isErr=true with the server message when the envelope
// carries an error. Returns text="" when out is plain tree text from an
// older binary (caller keeps raw output). Thin view over parseEnvelope
// for callers that only need the text.
func UnwrapEnvelope(out string) (text string, isErr bool, msg string) {
	e := parseEnvelope(out)
	return e.Text, e.IsErr, e.Msg
}

// diagnosis renders a tool-error payload for humans. CBM puts the
// diagnosis in the content text, usually as a small JSON object carrying
// `error` and often a `hint` (e.g. an unresolvable function_name tells you
// to resolve it with search_graph first). The hint is the remediation, so
// it must survive into tk's error instead of a raw JSON blob.
func diagnosis(text string) string {
	var p struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &p); err != nil {
		return text
	}
	msg := p.Error
	if msg == "" {
		msg = p.Message
	}
	if msg == "" {
		return text
	}
	if p.Hint != "" {
		return msg + " (hint: " + p.Hint + ")"
	}
	return msg
}

// env builds the spawn environment (shared by all Run variants).
func (r *Runner) env() []string {
	env := append(os.Environ(),
		"CBM_CACHE_DIR="+r.Paths.CBMCacheDir(),
		"CBM_RUNTIME_DIR="+r.Paths.CBMRuntimeDir(),
	)
	if r.Cfg.AllowedRoot != "" {
		env = append(env, "CBM_ALLOWED_ROOT="+r.Cfg.AllowedRoot)
	}
	return env
}
func (r *Runner) RunRaw(ctx context.Context, argv ...string) (string, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = deadline()
		defer cancel()
	}
	args := append([]string{"cli"}, argv...)
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Env = append(os.Environ(),
		"CBM_CACHE_DIR="+r.Paths.CBMCacheDir(),
		"CBM_RUNTIME_DIR="+r.Paths.CBMRuntimeDir(),
	)
	if r.Cfg.AllowedRoot != "" {
		cmd.Env = append(cmd.Env, "CBM_ALLOWED_ROOT="+r.Cfg.AllowedRoot)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("cbm cli failed: %s", firstLine(msg))
	}
	return out.String(), nil
}

// RunDaemon invokes a top-level (non-cli) cbm subcommand such as
// `daemon status|stop` with the same env mapping. CLI-only surface:
// model-facing MCP must never control daemon lifecycle.
// Unlike Run/RunRaw, stdout is returned even on failure: daemon commands
// report state ("daemon: not running") on stdout with a nonzero exit.
// Callers decide whether that output is the answer (status) or an error.
func (r *Runner) RunDaemon(ctx context.Context, argv ...string) (string, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = deadline()
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, r.Bin, argv...)
	cmd.Env = append(os.Environ(),
		"CBM_CACHE_DIR="+r.Paths.CBMCacheDir(),
		"CBM_RUNTIME_DIR="+r.Paths.CBMRuntimeDir(),
	)
	if r.Cfg.AllowedRoot != "" {
		cmd.Env = append(cmd.Env, "CBM_ALLOWED_ROOT="+r.Cfg.AllowedRoot)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		// Preserve any daemon output for the caller to interpret.
		if text := strings.TrimSpace(out.String()); text != "" {
			return text + "\n", fmt.Errorf("cbm daemon failed: %s", firstLine(msg))
		}
		return "", fmt.Errorf("cbm daemon failed: %s", firstLine(msg))
	}
	return out.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// FirstLine is firstLine exported for cross-package callers.
func FirstLine(s string) string { return firstLine(s) }

// LooksEmpty reports whether tool output carries no evidence.
// Absence claims gate on this: empty output must prove coverage.
func LooksEmpty(out string) bool {
	t := strings.TrimSpace(out)
	if t == "" {
		return true
	}
	lower := strings.ToLower(t)
	for _, marker := range []string{"no result", "no match", "0 result", "not found", "no caller"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return zeroEvidence(t)
}

// LooksEmptyData is the structured twin of LooksEmpty: same doctrine, same
// guards, applied to a format:"json" payload instead of a rendered tree.
//
// It exists because LooksEmpty cannot be reused. Its counters arrive as
// `key: 0` lines, and under format:"json" those same counters are JSON keys
// with no colon to match — reusing the text scan would find nothing, call
// every reply evidence-free, and quietly disable coverage-before-absence on
// the whole structured path. The two must stay in step; a divergence is a
// silent loss of the absence guarantee, not a cosmetic bug.
//
// nil data is not empty: there is no payload to judge, and the caller is on
// the text path where LooksEmpty applies.
func LooksEmptyData(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return false // never guess at a payload we cannot read
	}
	seen, zero := scanEvidence(root)
	return seen && zero
}

// scanEvidence walks a decoded payload and reports whether any evidence
// counter was present and whether every one of them was zero. Counters are
// the same family the text path matches (evidenceKey == countField, applied
// to a JSON key rather than a line prefix), and the three readings of a
// counter key are:
//
//	numeric          — the count itself
//	empty array      — zero evidence: the engine says it has no rows
//	populated array  — a finding: zero is disproved
//	anything else    — metadata (a string relation label, a bool freshness
//	                   flag, a null cursor), never a statement about how
//	                   much was found
//
// Only keys in evidenceKey are considered, which is what keeps
// `query_graph`'s always-populated `columns` header list from reading as a
// finding: a zero-row result is `columns:["f.name"], rows:[], total:0`, and
// the header must not manufacture evidence.
//
// Two guards carry over from the text path unchanged: at least one counter
// must be seen (absence is never inferred from a payload that states
// nothing), and one non-zero counter disqualifies emptiness —
// `callees_total: 1` with `callers_total: 0` is a traversal that found
// something.
func scanEvidence(v any) (seen, zero bool) {
	zero = true
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if evidenceKey.MatchString(k) {
				if n, ok := asNumber(val); ok {
					seen = true
					if n != 0 {
						zero = false
					}
					continue
				}
				if arr, ok := val.([]any); ok {
					seen = true
					if len(arr) > 0 {
						zero = false
					}
				}
				continue
			}
			if s, z := scanEvidence(val); s {
				seen = true
				if !z {
					zero = false
				}
			}
		}
	case []any:
		for _, e := range t {
			if s, z := scanEvidence(e); s {
				seen = true
				if !z {
					zero = false
				}
			}
		}
	}
	return seen, zero
}

// evidenceKey is countField applied to a JSON key: the identical
// total/count/results/returned family, so both paths accept exactly the
// same fields.
var evidenceKey = regexp.MustCompile(`(?i)^(?:.*_)?(?:total|counts?|matches|results|returned|found|rows|nodes|edges|entries|items)$`)

// asNumber reads a JSON scalar as a counter. Anything else — a string
// (`*_relation: "eq"`), a bool (`has_more`), null (`content_next_offset`) —
// is metadata, never a statement about how much was found.
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// countField matches the evidence counters the engine prints for every
// query — the total/count/results/returned family. Pagination cursors
// (result_offset), timings (elapsed_ms), flags (has_more, truncated) and
// relation labels (callers_total_relation) are deliberately excluded: they
// are metadata, never a statement about how much was found.
var countField = regexp.MustCompile(`(?i)^(?:.*_)?(?:total|counts?|matches|results|returned|found|rows|nodes|edges|entries|items)$`)

// countLine splits a `key: 0  (cols: …)` engine line into key and number.
var countLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):\s*(-?\d+)\b`)

// zeroEvidence reports whether every evidence counter the engine printed is
// zero — i.e. it stated, in its own counters, that it found nothing.
//
// This is load-bearing, not belt-and-braces: CBM 0.11.0 renders an empty
// result as counters, never as prose. A real zero-caller trace is
//
//	callers_total: 0
//	callers_total_relation: eq
//	callers: 0  (cols: qn hop)
//
// and a real search miss is `results: 0 / total: 0 / returned: 0`, neither
// of which contains any prose marker. Detecting only the markers would let
// every genuine absence claim through unproven.
//
// Two guards keep this conservative: at least one counter must be present
// (unstructured text is never guessed at), and a single non-zero counter
// disqualifies emptiness — `direction=both` on a function with 2 callers
// and 0 callees is a hit, not an absence.
func zeroEvidence(t string) bool {
	seen := false
	for _, ln := range strings.Split(t, "\n") {
		m := countLine.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil || !countField.MatchString(m[1]) {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		seen = true
		if n != 0 {
			return false
		}
	}
	return seen
}

// NearMissTokens splits a symbol into search tokens for candidate lookup.
// Camel/snake/dotted boundaries split; tiny tokens drop.
func NearMissTokens(sym string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if w := cur.String(); len(w) >= 3 {
			toks = append(toks, w)
		}
		cur.Reset()
	}
	for i, r := range sym {
		switch {
		case r == '_' || r == '.' || r == '/' || r == ':' || r == '-':
			flush()
		case unicode.IsUpper(r) && i > 0:
			flush()
			cur.WriteRune(unicode.ToLower(r))
		default:
			cur.WriteRune(unicode.ToLower(r))
		}
	}
	flush()
	seen := map[string]bool{}
	out := toks[:0]
	for _, t := range toks {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// summaryLine picks the first line containing key, else firstLine.
func summaryLine(t, key string) string {
	for _, ln := range strings.Split(t, "\n") {
		if strings.Contains(strings.ToLower(ln), key) {
			return strings.TrimSpace(ln)
		}
	}
	return firstLine(t)
}

// CoverageVerdict derives a one-line absence-claim verdict from a
// check_index_coverage text reply (scopes=whole-project). Fresh + complete
// recording = clean; anything else counts as a gap (absence unverified).
func CoverageVerdict(out string) string {
	t := strings.TrimSpace(out)
	if t == "" {
		return "coverage: unknown (empty reply)"
	}
	lower := strings.ToLower(t)
	if strings.Contains(lower, "generation_matches: true") &&
		strings.Contains(lower, "hash_records_complete: true") &&
		strings.Contains(lower, "recording_status: complete") {
		return "coverage: clean — " + summaryLine(t, "recording_status")
	}
	return "coverage: GAP — " + summaryLine(t, "generation_matches")
}

// QualifiedNames reassembles the qualified names in a search_graph payload.
//
// The engine does not repeat the qualified name per row. It states the rule
// once — qn = qn_prefix == "" ? name : qn_prefix + "." + name — hands back a
// qualifier per group plus a `name` column per row, and leaves the caller to
// put the two together. `tk validate` needs that, because nothing else in the
// reply can answer "is this exactly the symbol I asked about".
//
// Two facts make this the only way to ask that question. search_graph matches
// name_pattern against the leaf name, never the qualified name, so searching
// for "sweep.Level2" comes back empty. And the rendered text cannot be
// substring-tested instead: a search summary names the project, so a
// substring test for "sweep.Level2" matches a reply that found nothing.
func QualifiedNames(data json.RawMessage) []string {
	var payload struct {
		Cols   []string `json:"cols"`
		Groups []struct {
			QNPrefix string  `json:"qn_prefix"`
			Rows     [][]any `json:"rows"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil
	}
	name := -1
	for i, col := range payload.Cols {
		if col == "name" {
			name = i
			break
		}
	}
	if name < 0 {
		return nil
	}
	var out []string
	for _, g := range payload.Groups {
		for _, row := range g.Rows {
			if name >= len(row) {
				continue
			}
			leaf, ok := row[name].(string)
			if !ok {
				continue
			}
			if g.QNPrefix == "" {
				out = append(out, leaf)
				continue
			}
			out = append(out, g.QNPrefix+"."+leaf)
		}
	}
	return out
}

// QualifiedNamesFromText is the rendered-tree counterpart of QualifiedNames,
// for callers that have the human reply rather than the payload. The engine's
// tree puts the qualified name in the first column of every result row, so
// the first field of an indented row is a qualified name.
//
// Reading that field, rather than substring-testing the reply, is the whole
// point. A search renders a row per group, and the group for the project
// itself renders as "sweep Project {} - 0 0" — so a substring test for
// "sweep.Level2" matches a row that has nothing to do with it, and a symbol
// that does not exist reads as found.
func QualifiedNamesFromText(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		// Result rows are indented; headers, counters and hints are not.
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		names = append(names, fields[0])
	}
	return names
}

// LeafName is the part of a symbol the engine can actually search for.
// search_graph matches name_pattern against the leaf `name`, never against a
// qualified name, so looking up "sweep.Level2" has to ask for "Level2" and
// settle the qualifier by reassembly afterwards.
func LeafName(sym string) string {
	if i := strings.LastIndexByte(sym, '.'); i >= 0 {
		return sym[i+1:]
	}
	return sym
}

// MatchSymbol returns the name among a search reply's results that is exactly
// the symbol asked about, or "" when there is none. Pass it
// QualifiedNames(res.Data) or QualifiedNamesFromText(text).
//
// The test is exact on purpose, and it reads the qualified name out of a row
// rather than searching the reply. A substring test over the rendered text is
// wrong twice over: a search summary renders the project row
// "sweep Project {} - 0 0", so a substring test for "sweep.Level2" matches a
// reply that found nothing; and the leaf search the engine performed could
// never return the qualified symbol in the first place. Together those report
// a symbol that exists as invalid — which is how `tk validate` came to deny
// symbols it was being asked about by their own qualified names.
//
// A bare name is matched as a leaf, which is what someone asking about
// "Level2" means. A dotted symbol must match whole: asking about a.b.c and
// finding a.b.x is a miss, not a near hit.
func MatchSymbol(names []string, sym string) string {
	dotted := strings.Contains(sym, ".")
	for _, qn := range names {
		if qn == sym {
			return qn
		}
		if dotted {
			continue
		}
		if i := strings.LastIndexByte(qn, '.'); i >= 0 && qn[i+1:] == sym {
			return qn
		}
	}
	return ""
}

// Truncate cuts text to budget chars preferring whole lines + marker.
func Truncate(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	cut := strings.LastIndex(s[:budget], "\n")
	if cut < budget/2 {
		cut = budget
	}
	return s[:cut] + "\n...truncated"
}

// rowList locates one droppable array inside the decoded payload, so the
// trim can shorten it in place without holding a pointer into the tree.
type rowList struct {
	path rowPath
}

// newRowList copies the path: walks append onto a shared backing array, so
// two sibling branches would otherwise overwrite each other's coordinates.
func newRowList(p rowPath) rowList { return rowList{path: append(rowPath{}, p...)} }

// len is the container's current element count.
func (c rowList) len(root any) int { return len(c.path.get(root)) }

// drop removes n elements from the end of the container.
func (c rowList) drop(root any, n int) {
	arr := c.path.get(root)
	if n <= 0 || n > len(arr) {
		return
	}
	c.path.set(root, arr[:len(arr)-n])
}

// pathStep is one hop: into a map key or into a slice index.
type pathStep struct {
	key   string
	idx   int
	isKey bool
}

// rowPath is a location in the decoded payload.
type rowPath []pathStep

// get resolves the array the path points at. A step that cannot be taken
// (wrong kind of container, out of range) ends the walk rather than
// panicking: a payload shape tk has not seen must trim to "no rows found",
// never to a crash.
func (p rowPath) get(root any) []any {
	cur := root
	for _, s := range p {
		if s.isKey {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[s.key]
			continue
		}
		arr, ok := cur.([]any)
		if !ok || s.idx >= len(arr) {
			return nil
		}
		cur = arr[s.idx]
	}
	arr, _ := cur.([]any)
	return arr
}

// set stores arr at the path's location. Re-navigating from the root is
// what makes a shortened slice visible to the marshaller; a slice header
// copied out of the tree would otherwise be dropped on the floor.
func (p rowPath) set(root any, arr []any) {
	if len(p) == 0 {
		return
	}
	cur := root
	for _, s := range p[:len(p)-1] {
		if s.isKey {
			cur, _ = cur.(map[string]any)[s.key]
			continue
		}
		outer, _ := cur.([]any)
		if s.idx >= len(outer) {
			return
		}
		cur = outer[s.idx]
	}
	last := p[len(p)-1]
	switch n := cur.(type) {
	case map[string]any:
		n[last.key] = arr
	case []any:
		if last.idx < len(n) {
			n[last.idx] = arr
		}
	}
}

// BudgetResult trims a structured payload to budget chars by dropping whole
// rows — the structured counterpart of Truncate, which cuts text at a line
// boundary. Character-slicing a JSON document would produce invalid JSON,
// which fails at the consumer's parser instead of tk's, so the budget is
// spent on records instead of bytes:
//
//	"data": { …, "budget_truncated": true, "rows_dropped": 12 }
//
// Scalars are never dropped. Counters are the evidence an absence claim
// rests on, and dropping them would turn "found nothing" into "found an
// unknown amount". The engine's own `truncated` field keeps its own meaning
// (its page limit fired), so the budget's marker is named `budget_truncated`
// rather than overwriting it.
//
// If every droppable row is gone and the payload still does not fit, it is
// returned whole with `budget_exceeded: true`. Under-trimming is honest;
// inventing a cut is not. Output is always valid JSON.
func BudgetResult(data json.RawMessage, budget int) json.RawMessage {
	if budget <= 0 || len(data) <= budget {
		return data
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return data
	}
	dropped := 0
	// Container order is fixed (deepest first, key-sorted), so the same
	// payload always trims to the same bytes. Batches double instead of
	// dropping one row per marshal, keeping the loop logarithmic.
	for _, c := range rowContainers(root) {
		step := 1
		for n := c.len(root); n > 0; {
			batch := step
			if batch > n {
				batch = n
			}
			c.drop(root, batch)
			dropped += batch
			n -= batch
			// Measured against the marked bytes, not the bare tree: the
			// markers are part of the payload, so budgeting the tree alone
			// would overshoot by the size of the marker every time.
			if size, ok := markedSize(root, dropped, false); ok && size <= budget {
				return marked(root, dropped, false)
			}
			step *= 2
		}
	}
	if _, ok := marshal(root); !ok {
		return data
	}
	return marked(root, dropped, true)
}

// marked records what the budget cost, so a caller can tell a trimmed
// answer from a complete one. A payload whose top level is not an object
// cannot carry the markers, so it is returned as marshalled.
func marked(root any, dropped int, exceeded bool) json.RawMessage {
	shaped, ok := withMarkers(root, dropped, exceeded)
	if !ok {
		raw, err := json.Marshal(root)
		if err != nil {
			return nil
		}
		return raw
	}
	raw, err := json.Marshal(shaped)
	if err != nil {
		return nil
	}
	return raw
}

// markedSize is the length of the bytes marked would return. The budget has
// to be spent against the payload as delivered, markers included.
func markedSize(root any, dropped int, exceeded bool) (int, bool) {
	shaped, ok := withMarkers(root, dropped, exceeded)
	if !ok {
		return 0, false
	}
	raw, err := json.Marshal(shaped)
	return len(raw), err == nil
}

// withMarkers returns a shallow copy of root carrying the budget markers.
// The copy matters: the trim keeps dropping rows and re-measuring, so the
// markers cannot be left on the tree being measured.
func withMarkers(root any, dropped int, exceeded bool) (any, bool) {
	obj, ok := root.(map[string]any)
	if !ok {
		return root, true
	}
	if dropped == 0 && !exceeded {
		return obj, true
	}
	out := make(map[string]any, len(obj)+3)
	for k, v := range obj {
		out[k] = v
	}
	out["budget_truncated"] = true
	out["rows_dropped"] = dropped
	if exceeded {
		out["budget_exceeded"] = true
	}
	return out, true
}

func marshal(v any) ([]byte, bool) {
	raw, err := json.Marshal(v)
	return raw, err == nil
}

// rowContainers finds every droppable row list in a payload, deepest first
// and key-sorted within each level, so the trim order is deterministic.
//
// What counts as a row list is deliberately narrow, because trimming the
// wrong container is a correctness bug rather than a bad cut:
//
//   - an array of arrays — `rows` (search_code, get_file_outline,
//     query_graph, and the per-group `rows` of search_graph/trace_path);
//     the engine's own `cols` fixes the positional mapping, and an array of
//     scalars is never touched;
//   - an array of objects — `groups`, `projects`, `scopes`, `impacted`.
//
// Arrays of scalars are left alone. query_graph pairs `columns` with
// positional `rows`, so dropping a column would silently re-index every
// remaining row. search_code rows carry a `matches` array apiece, so the
// walk must not descend into a kept row either — hence an array of arrays
// is registered and not descended into, while an array of objects is
// descended into first (that is how a group's `rows` is found) and only
// then registered itself.
func rowContainers(root any) []rowList {
	var out []rowList
	walkRows(root, nil, &out)
	return out
}

// walkRows registers the droppable containers reachable from v, recursing
// into maps (sorted keys, for a deterministic trim order) and into record
// arrays, but never into a positional row table.
func walkRows(v any, path rowPath, out *[]rowList) {
	if arr, ok := v.([]any); ok {
		if isRowArray(arr) {
			*out = append(*out, newRowList(path))
			return
		}
		for i, e := range arr {
			switch e.(type) {
			case []any, map[string]any:
				walkRows(e, append(path, pathStep{idx: i}), out)
			}
		}
		if isObjectArray(arr) {
			// Records (groups/projects/scopes/impacted): nested rows were
			// collected first, so a whole record is only dropped once its
			// own rows are gone.
			*out = append(*out, newRowList(path))
		}
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := append(path, pathStep{key: k, isKey: true})
		if child, isArr := m[k].([]any); isArr {
			walkRows(child, p, out)
			if isObjectArray(child) {
				// Records are the fallback tier: their own rows were
				// collected above, so a whole record is only reached once
				// those are spent.
				*out = append(*out, newRowList(p))
			}
			continue
		}
		walkRows(m[k], p, out)
	}
}

// isRowArray reports an array of arrays: the engine's positional tables.
func isRowArray(a []any) bool {
	if len(a) == 0 {
		return true // empty: nothing to drop, but a legitimate container
	}
	_, ok := a[0].([]any)
	return ok
}

// isObjectArray reports an array of objects (records), as opposed to a
// header list of scalars.
func isObjectArray(a []any) bool {
	if len(a) == 0 {
		return true
	}
	_, ok := a[0].(map[string]any)
	return ok
}
