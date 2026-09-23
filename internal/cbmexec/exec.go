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
	"strings"
	"time"

	"github.com/true-knowledge/tk/internal/cbmresolve"
	"github.com/true-knowledge/tk/internal/config"
	"github.com/true-knowledge/tk/internal/paths"
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
		return "", fmt.Errorf("cbm %s failed: %s", tool, firstLine(msg))
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
	rraw, ok := raw["result"]
	if !ok {
		return "", false, ""
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(rraw, &res) == nil {
		for _, c := range res.Content {
			if c.Text != "" {
				return c.Text, false, ""
			}
		}
	}
	var s string
	if json.Unmarshal(rraw, &s) == nil && s != "" {
		return s, false, ""
	}
	return "", false, ""
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
