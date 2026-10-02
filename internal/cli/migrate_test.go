package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/paths"
)

func migrateCmd(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	cmd := cmdMigrate(&Globals{Home: home})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// legacyTree writes a config.json that tk will refuse to migrate from if it is
// the live home, so --from is the only way in.
func legacyTree(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A migration that moved files must say so.
func TestMigrateCopiesLegacyConfig(t *testing.T) {
	home := t.TempDir()
	legacy := legacyTree(t, `{"version":1}`)

	out, err := migrateCmd(t, home, "--from", legacy)
	if err != nil {
		t.Fatalf("first migrate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "migrated 1 file(s)") {
		t.Errorf("want a count of one:\n%s", out)
	}
	p := paths.Resolve(home)
	got, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if string(got) != `{"version":1}` {
		t.Errorf("config = %q, want the legacy body", got)
	}
	if _, err := os.Stat(filepath.Join(p.Data, "MIGRATED")); err != nil {
		t.Error("the MIGRATED marker must be written")
	}
}

// The re-run is the case the doc got wrong. `03-COMMANDS.md` promised a refusal
// and no --force existed, so a second run exited 0 having moved nothing. A
// migration that migrated nothing is a failed migration.
func TestMigrateRerunRefusesWithoutForce(t *testing.T) {
	home := t.TempDir()
	legacy := legacyTree(t, `{"version":1}`)
	if _, err := migrateCmd(t, home, "--from", legacy); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	out, err := migrateCmd(t, home, "--from", legacy)
	if err == nil {
		t.Fatalf("a re-run that moves nothing must exit nonzero; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal must name the flag that overrides it: %v", err)
	}
	// The refusal must not have touched the live config on its way out.
	got, rerr := os.ReadFile(paths.Resolve(home).ConfigFile())
	if rerr != nil || string(got) != `{"version":1}` {
		t.Errorf("a refused re-run must leave the config alone: %q %v", got, rerr)
	}
}

// A --json caller learns the command failed from the envelope as well as from
// the exit code. A refusal that renders nothing on stdout leaves a machine with
// no diagnosis at all.
func TestMigrateRerunJSONEnvelopeIsNotOK(t *testing.T) {
	home := t.TempDir()
	legacy := legacyTree(t, `{"version":1}`)
	if _, err := migrateCmd(t, home, "--from", legacy); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	out := &bytes.Buffer{}
	cmd := cmdMigrate(&Globals{Home: home, JSON: true})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--from", legacy})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("a re-run must exit nonzero; output:\n%s", out)
	}
	var env struct {
		OK      bool     `json:"ok"`
		Text    string   `json:"text"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	if env.OK {
		t.Error("ok:true on a refusal tells a machine the migration happened:\n" + out.String())
	}
	if !strings.Contains(env.Text, "--force") || len(env.Skipped) == 0 {
		t.Errorf("the envelope must carry the diagnosis and what was skipped: %+v", env)
	}
}

// --force is what the documented flag has to actually do.
func TestMigrateForceOverwrites(t *testing.T) {
	home := t.TempDir()
	legacy := legacyTree(t, `{"version":1,"index_mode":"full"}`)
	if _, err := migrateCmd(t, home, "--from", legacy); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	out, err := migrateCmd(t, home, "--from", legacy, "--force")
	if err != nil {
		t.Fatalf("--force must succeed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "migrated 1 file(s)") {
		t.Errorf("--force must report the write:\n%s", out)
	}
	got, rerr := os.ReadFile(paths.Resolve(home).ConfigFile())
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != `{"version":1,"index_mode":"full"}` {
		t.Errorf("config = %q, want the forced body", got)
	}
}

// Nothing to migrate is an answer, not a failure: there is no legacy tree, so
// there is nothing to be confused about.
func TestMigrateNoLegacyTreeIsNotAnError(t *testing.T) {
	home := t.TempDir()
	empty := t.TempDir() // exists, but holds no config.json
	out, err := migrateCmd(t, home, "--from", empty)
	if err != nil {
		t.Fatalf("no legacy files must exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to migrate") {
		t.Errorf("want the no-op line:\n%s", out)
	}
}

// --dry-run must not write, and must not claim it migrated anything.
func TestMigrateDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	legacy := legacyTree(t, `{"version":1}`)

	out, err := migrateCmd(t, home, "--from", legacy, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would migrate") {
		t.Errorf("want the would-migrate line:\n%s", out)
	}
	if _, err := os.Stat(paths.Resolve(home).ConfigFile()); !os.IsNotExist(err) {
		t.Error("a dry run must not write the config")
	}
	if _, err := os.Stat(filepath.Join(paths.Resolve(home).Data, "MIGRATED")); !os.IsNotExist(err) {
		t.Error("a dry run must not write the marker")
	}
}
