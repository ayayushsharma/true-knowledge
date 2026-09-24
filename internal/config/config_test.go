package config_test

import (
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
