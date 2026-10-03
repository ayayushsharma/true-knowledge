package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/ayayushsharma/true-knowledge/internal/cbmexec"
	"github.com/ayayushsharma/true-knowledge/internal/resident"
)

// Resident is the optional warm path in front of a spawn.
//
// It is the one method of resident.Client this package needs, named as an
// interface so a test can stand in for a socket. Production wires the real
// client; nil means never dial, which is the behaviour of every caller that has
// no resident to ask.
type Resident interface {
	Call(req resident.Request) (resident.Reply, error)
}

// residentTry asks a running resident to answer one engine call.
//
// It answers ok=false only when there is nothing to talk to, and the caller
// then spawns. That is the normal case: the resident is opt-in, so most MCP
// sessions never dial successfully and must behave exactly as they did before
// the resident existed.
//
// Three outcomes stay distinct, for the same reason Ctx.residentTry keeps them
// distinct in the CLI. A refusal is an answer, not an absence: the engine said
// no, and re-running the identical call in a fresh process would produce the
// same no, slower. Collapsing refused into absent would report a failed engine
// call as a successful empty result, which is precisely the silent absence the
// coverage probe exists to rule out.
//
// structured selects which parse the reply gets. Both paths run the same
// ParseResult as the CLI so one engine answer cannot render two ways depending
// on which process asked for it.
func (s *Server) residentTry(tool string, args map[string]any, structured bool) (cbmexec.Result, bool, error) {
	if s.Resident == nil {
		return cbmexec.Result{}, false, nil
	}
	reply, err := s.Resident.Call(resident.Request{Tool: tool, Args: args, Structured: structured})
	if err != nil {
		// Includes ErrNoResident, which is an outcome and not a fault. A
		// resident that died between the dial and the read lands here too,
		// and spawning is the right answer for that as well.
		s.backend = backendSpawn
		return cbmexec.Result{}, false, nil
	}
	s.backend = backendResident
	if reply.Error != "" {
		return cbmexec.Result{}, true, fmt.Errorf("cbm %s: %w", tool, errors.New(reply.Error))
	}
	res, perr := cbmexec.ParseResult(reply.Result)
	if perr != nil {
		return cbmexec.Result{}, true, perr
	}
	return res, true, nil
}

// runJSON runs an envelope-first read, resident first and spawn second.
func (s *Server) runJSON(ctx context.Context, tool string, args map[string]any) (string, error) {
	res, ok, err := s.residentTry(tool, args, false)
	if ok {
		if err != nil {
			return "", err
		}
		if res.Text == "" && res.Data == nil {
			return "", fmt.Errorf("cbm %s: resident returned nothing", tool)
		}
		return res.Text, nil
	}
	s.backend = backendSpawn
	return s.Run.RunJSON(ctx, tool, args)
}

// runStructured runs a structured read, resident first and spawn second.
//
// Data == nil with no error is not a fault here: it is an engine older than
// format:"json", which the caller renders as prose. Only an error is an error,
// and the resident's zero spawns is recorded as such.
func (s *Server) runStructured(ctx context.Context, tool string, args map[string]any) (cbmexec.Result, error) {
	res, ok, err := s.residentTry(tool, args, true)
	if ok {
		return res, err
	}
	s.backend = backendSpawn
	return s.Run.RunStructured(ctx, tool, args)
}

// backend names which path served the last engine call, for tk.log. Serial
// dispatch means it needs no lock: s.handle runs inline in the read loop, so no
// second call can be in flight while this is read.
const (
	backendSpawn    = "spawn"
	backendResident = "resident"
)
