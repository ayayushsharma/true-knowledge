// Package e2ereal holds the opt-in end-to-end test that drives a real
// codebase-memory-mcp against a real repository — the only test in the repo
// that indexes megabytes of foreign code instead of a three-line fixture.
//
// The e2ereal build tag keeps it out of `go test ./...`, so the default suite
// needs no network, no second language, and no minutes of CPU.
//
//	mise run e2e-real REPO=/path/to/repo
//	TK_E2E_REAL=1 TK_E2E_REPO=/path/to/repo \
//	  go test -tags e2ereal -timeout 90m -v ./tests/e2ereal/
//
// Environment:
//
//	TK_E2E_REPO     required, a git work tree to register and index
//	TK_E2E_REAL     required, must be 1 — a second gate on top of the tag
//	TK_E2E_NAME     project name, defaults to the repo directory name
//	TK_E2E_MODE     fast|moderate|full, defaults to moderate
//	TK_E2E_CBM_BIN  reuse this binary instead of `tk install cbm` (offline)
//	TK_E2E_TIMEOUT  index budget, defaults to 45m
package e2ereal
