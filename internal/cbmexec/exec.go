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
}

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
		ctx, cancel = context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
	}
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
		return "", fmt.Errorf("args file: %w", err)
	}
	argsPath := tmp.Name()
	if _, err := tmp.Write(rawJSON); err != nil {
		_ = os.Remove(argsPath)
		return "", fmt.Errorf("args file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(argsPath)
		return "", fmt.Errorf("args file: %w", err)
	}
	defer os.Remove(argsPath)
	cmd := exec.CommandContext(ctx, r.Bin, "cli", tool, "--args-file", argsPath)
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
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
	}
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
		return "", fmt.Errorf("args file: %w", err)
	}
	argsPath := tmp.Name()
	if _, err := tmp.Write(rawJSON); err != nil {
		_ = os.Remove(argsPath)
		return "", fmt.Errorf("args file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(argsPath)
		return "", fmt.Errorf("args file: %w", err)
	}
	defer os.Remove(argsPath)
	cmd := exec.CommandContext(ctx, r.Bin, "cli", "--json", tool, "--args-file", argsPath)
	cmd.Env = r.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return r.Run(ctx, tool, payload)
	}
	text, isErr, msg := UnwrapEnvelope(out.String())
	if isErr {
		return "", fmt.Errorf("cbm %s failed: %s", tool, msg)
	}
	if text != "" {
		return text, nil
	}
	// Exit 0 but no envelope: older binary ignored the flag.
	// Prefer legacy output when it exists, else keep what we got.
	if legacy, lerr := r.Run(ctx, tool, payload); lerr == nil && legacy != "" {
		return legacy, nil
	}
	return out.String(), nil
}

// UnwrapEnvelope extracts display text from a CBM `cli --json` MCP
// envelope. Returns isErr=true with the server message when the envelope
// carries an error. Returns text="" when out is plain tree text from an
// older binary (caller keeps raw output).
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
func UnwrapEnvelope(out string) (text string, isErr bool, msg string) {
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false, ""
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", false, ""
	}
	if eraw, ok := raw["error"]; ok {
		var em struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(eraw, &em) == nil && em.Message != "" {
			return "", true, firstLine(em.Message)
		}
		return "", true, "unknown CBM error"
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
	}
	if json.Unmarshal(res, &obj) == nil {
		for _, c := range obj.Content {
			if c.Text != "" {
				if obj.IsError {
					return "", true, firstLine(diagnosis(c.Text))
				}
				return c.Text, false, ""
			}
		}
	}
	var s string
	if json.Unmarshal(res, &s) == nil && s != "" {
		return s, false, ""
	}
	return "", false, ""
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
		ctx, cancel = context.WithTimeout(context.Background(), defaultTimeout)
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
		ctx, cancel = context.WithTimeout(context.Background(), defaultTimeout)
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
