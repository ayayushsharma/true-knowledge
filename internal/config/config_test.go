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
