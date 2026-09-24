package config_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/ayayushsharma/true-knowledge/internal/config"
)

func TestDefaultsValidate(t *testing.T) {
	if err := config.Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	cfg := config.Defaults()
	cfg.IndexMode = "fast"
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.IndexMode != "fast" {
		t.Fatalf("mode = %s", got.IndexMode)
	}
}

func TestRejectBadMode(t *testing.T) {
	cfg := config.Defaults()
	cfg.IndexMode = "slow"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for slow mode")
	}
}

func TestUIPickerDefaultsAndKnownKey(t *testing.T) {
	if !config.Defaults().UI.Picker {
		t.Fatal("default ui.picker must be true (humans get the picker)")
	}
	found := false
	for _, k := range config.KnownKeys() {
		if k == "ui.picker" {
			found = true
		}
	}
	if !found {
		t.Fatal("ui.picker missing from KnownKeys")
	}
}

func TestSetKeyUIPicker(t *testing.T) {
	cfg := config.Defaults()
	if err := config.SetKey(&cfg, "ui.picker", "false"); err != nil || cfg.UI.Picker {
		t.Fatalf("set false: off=%v err=%v", !cfg.UI.Picker, err)
	}
	if err := config.SetKey(&cfg, "ui.picker", "true"); err != nil || !cfg.UI.Picker {
		t.Fatalf("set true: on=%v err=%v", cfg.UI.Picker, err)
	}
	if err := config.SetKey(&cfg, "ui.picker", "notabool"); err == nil {
		t.Fatal("expected error for non-bool ui.picker")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := config.Save(p, config.Defaults()); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"bogus":true,"index_mode":"fast","auto_index":true,"auto_watch":true,"watcher_enabled":true,"budgets":{"default_chars":1,"architecture_chars":1,"notes_toc_chars":1}}`
	if err := writeFile(p, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(p); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

// mcp.profile is stored as a nested "mcp" object (dotted-key convention, same
// as budgets/embedding/ledger/ui). The legacy flat "mcp_profile" scalar keeps
// loading and migrates on the next Save.
func TestMCPProfileNestedShape(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	cfg := config.Defaults()
	if err := config.SetKey(&cfg, "mcp.profile", "analysis"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := readFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"mcp"`)) || bytes.Contains(raw, []byte(`"mcp_profile"`)) {
		t.Fatalf("config must use nested mcp object, got:\n%s", raw)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.MCP.Profile != "analysis" {
		t.Fatalf("profile = %q", got.MCP.Profile)
	}
}

func TestMCPProfileLegacyFlatLoads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true,"mcp_profile":"memory","budgets":{"default_chars":6000,"architecture_chars":2200,"notes_toc_chars":700,"ledger_chars":1500}}`
	if err := writeFile(p, legacy); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatalf("legacy flat mcp_profile must load: %v", err)
	}
	if got.MCP.Profile != "memory" {
		t.Fatalf("profile = %q", got.MCP.Profile)
	}
	if err := config.Save(p, got); err != nil {
		t.Fatal(err)
	}
	raw, err := readFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"mcp_profile"`)) || !bytes.Contains(raw, []byte(`"mcp"`)) {
		t.Fatalf("re-saved config must be nested, got:\n%s", raw)
	}
}
