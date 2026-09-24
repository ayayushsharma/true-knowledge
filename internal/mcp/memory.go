package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/memory"
)

// isMemoryTool reports whether name is one of tk's in-process memory tools.
func isMemoryTool(name string) bool {
	for _, t := range memoryTools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// argStr/argInt read typed JSON arguments safely.
func argStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return def
}

// memoryOut wraps text into an MCP result (budget-truncated).
func memoryOut(text string, budget int) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": cbmexec.Truncate(text, budget)}},
	}
}

// callMemory serves the 9 memory-profile tools entirely in-process. They
// never need CBM (fail-open companions to the graph surface) and never
// spawn external processes.
func (s *Server) callMemory(ctx context.Context, id any, name string, args map[string]any) rpcResp {
	if s.Mem == nil {
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, "memory profile unavailable: stores not opened (run `tk mcp --tool-profile memory` from a configured TK_HOME)"}}
	}
	switch name {
	case "mem_save":
		fact, queued, err := s.Mem.MemSave(ctx, argStr(args, "scope"), argStr(args, "project"),
			argStr(args, "topic"), argStr(args, "value"), argStr(args, "provenance"))
		if err != nil {
			return memErr(id, err)
		}
		if queued {
			return memResult(id, s.Budget, fmt.Sprintf("saved %q for review (secret-looking value; approve via mem_review)", fact.Topic))
		}
		return memResult(id, s.Budget, fmt.Sprintf("saved fact %q (%s scope)", fact.Topic, fact.Scope))
	case "mem_recall":
		facts, err := s.Mem.MemRecall(ctx, argStr(args, "project"), argStr(args, "topic"))
		if err != nil {
			return memErr(id, err)
		}
		if len(facts) == 0 {
			return memResult(id, s.Budget, fmt.Sprintf("no facts for topic %q", argStr(args, "topic")))
		}
		var b strings.Builder
		for _, f := range facts {
			fmt.Fprintf(&b, "- %s [%s]", f.Value, f.Scope)
			if f.Project != "" {
				fmt.Fprintf(&b, "/%s", f.Project)
			}
			if f.Provenance != "" {
				fmt.Fprintf(&b, " (from %s)", f.Provenance)
			}
			b.WriteByte('\n')
		}
		return memResult(id, s.Budget, strings.TrimRight(b.String(), "\n"))
	case "mem_review":
		switch argStr(args, "action") {
		case "list", "":
			revs, err := s.Mem.MemReviews(ctx)
			if err != nil {
				return memErr(id, err)
			}
			if len(revs) == 0 {
				return memResult(id, s.Budget, "no pending fact reviews")
			}
			var b strings.Builder
			for _, e := range revs {
				fmt.Fprintf(&b, "%s  %s/%s  %q  reason=%s\n", e.ID, e.Scope, e.Project, e.Topic, strings.Join(e.Reason, ","))
			}
			return memResult(id, s.Budget, strings.TrimRight(b.String(), "\n"))
		case "approve":
			fact, err := s.Mem.MemApprove(ctx, argStr(args, "id"))
			if err != nil {
				return memErr(id, err)
			}
			return memResult(id, s.Budget, fmt.Sprintf("approved fact %q (%s)", fact.Topic, fact.Scope))
		case "reject":
			if err := s.Mem.MemReject(ctx, argStr(args, "id")); err != nil {
				return memErr(id, err)
			}
			return memResult(id, s.Budget, fmt.Sprintf("rejected review %s", argStr(args, "id")))
		default:
			return memErr(id, fmt.Errorf("mem_review action must be list|approve|reject"))
		}
	case "note_save":
		ent, err := s.Mem.NoteSave(ctx, argStr(args, "project"), argStr(args, "title"), argStr(args, "text"))
		if err != nil {
			return memErr(id, err)
		}
		return memResult(id, s.Budget, fmt.Sprintf("captured note %q for review (id %s); approve to make searchable", ent.Title, ent.ID))
	case "note_search":
		hits, err := s.Mem.NoteSearch(ctx, argStr(args, "query"), argStr(args, "project"), argInt(args, "limit", 10))
		if err != nil {
			return memErr(id, err)
		}
		if len(hits) == 0 {
			return memResult(id, s.Budget, fmt.Sprintf("no approved notes match %q", argStr(args, "query")))
		}
		var b strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&b, "- %s [%s] %s\n", h.Note.Title, h.Note.Project, h.Excerpt)
		}
		return memResult(id, s.Budget, strings.TrimRight(b.String(), "\n"))
	case "note_toc":
		lines, _, err := s.Mem.NoteTOC(ctx, argStr(args, "project"))
		if err != nil {
			return memErr(id, err)
		}
		if len(lines) == 0 {
			return memResult(id, s.Budget, "no approved notes yet (see note_review list)")
		}
		return memResult(id, s.Budget, strings.Join(lines, "\n"))
	case "note_reindex":
		count, err := s.Mem.NoteReindex(ctx, argStr(args, "project"))
		if err != nil {
			return memErr(id, err)
		}
		return memResult(id, s.Budget, fmt.Sprintf("reindexed %d note(s)", count))
	case "note_review":
		switch argStr(args, "action") {
		case "list", "":
			revs, err := s.Mem.NoteReviews(ctx)
			if err != nil {
				return memErr(id, err)
			}
			if len(revs) == 0 {
				return memResult(id, s.Budget, "no pending note reviews")
			}
			var b strings.Builder
			for _, e := range revs {
				fmt.Fprintf(&b, "%s  %s  %q  %d chars\n", e.ID, e.Project, e.Title, len(e.Text))
			}
			return memResult(id, s.Budget, strings.TrimRight(b.String(), "\n"))
		case "approve":
			note, err := s.Mem.NoteApprove(ctx, argStr(args, "id"))
			if err != nil {
				return memErr(id, err)
			}
			return memResult(id, s.Budget, fmt.Sprintf("approved note %q (%s)", note.Title, note.File))
		case "reject":
			if err := s.Mem.NoteReject(ctx, argStr(args, "id")); err != nil {
				return memErr(id, err)
			}
			return memResult(id, s.Budget, fmt.Sprintf("rejected review %s", argStr(args, "id")))
		default:
			return memErr(id, fmt.Errorf("note_review action must be list|approve|reject"))
		}
	case "ledger_update":
		text, err := s.Mem.LedgerUpdate(ctx, argStr(args, "project"), argStr(args, "text"))
		if err != nil {
			return memErr(id, err)
		}
		return memResult(id, s.Budget, fmt.Sprintf("updated ledger for %s (%d chars)", argStr(args, "project"), len(text)))
	default:
		return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32601, "unknown memory tool " + name}}
	}
}

func memErr(id any, err error) rpcResp {
	return rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{-32000, mask(err.Error())}}
}

func memResult(id any, budget int, text string) rpcResp {
	return rpcResp{JSONRPC: "2.0", ID: id, Result: memoryOut(mask(text), budget)}
}

// mask scrubs secret-shaped strings from memory tool output before it reaches
// logs or the client (facts/notes can legitimately hold such values).
func mask(s string) string {
	return memory.SecretMask(strings.TrimSpace(s))
}
