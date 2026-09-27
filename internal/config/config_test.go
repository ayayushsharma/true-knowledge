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

// A partial config.json must inherit Defaults() for every absent key. The
// regression this guards: Config implements json.Unmarshaler, and a
// whole-struct assignment there discarded the defaults Load pre-seeded, so a
// config omitting "ledger"/"ui" silently ran with enabled=false/picker=false.
func TestLoadPartialInheritsDefaults(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no groups at all", `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true}`},
		{"only budgets", `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true,"budgets":{"default_chars":6000,"architecture_chars":2200,"notes_toc_chars":700,"ledger_chars":1500}}`},
		{"legacy pre-mcp shape", `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true,"budgets":{"default_chars":6000,"architecture_chars":2200,"notes_toc_chars":700,"ledger_chars":1500}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			if err := writeFile(p, tc.raw); err != nil {
				t.Fatal(err)
			}
			got, err := config.Load(p)
			if err != nil {
				t.Fatalf("partial config must load: %v", err)
			}
			if !got.Ledger.Enabled {
				t.Error("ledger.enabled must inherit the true default, got false")
			}
			if !got.UI.Picker {
				t.Error("ui.picker must inherit the true default, got false")
			}
			if got.Budgets.DefaultChars != config.Defaults().Budgets.DefaultChars {
				t.Errorf("budgets.default_chars = %d, want the default %d", got.Budgets.DefaultChars, config.Defaults().Budgets.DefaultChars)
			}
			if got.Embedding.Endpoint != config.Defaults().Embedding.Endpoint {
				t.Errorf("embedding.endpoint = %q, want the default %q", got.Embedding.Endpoint, config.Defaults().Embedding.Endpoint)
			}
			if got.CBMVersionPin != config.DefaultCBMPin {
				t.Errorf("cbm_version_pin = %q, want the default %q", got.CBMVersionPin, config.DefaultCBMPin)
			}
		})
	}
}

// A present key always wins over the default, including a false boolean: an
// explicit ledger.enabled=false must not be read back as the true default.
func TestLoadExplicitFalseBeatsDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	raw := `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true,` +
		`"ledger":{"enabled":false},"ui":{"picker":false},` +
		`"budgets":{"default_chars":6000,"architecture_chars":2200,"notes_toc_chars":700,"ledger_chars":1500}}`
	if err := writeFile(p, raw); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ledger.Enabled {
		t.Error("explicit ledger.enabled=false was overwritten by the default")
	}
	if got.UI.Picker {
		t.Error("explicit ui.picker=false was overwritten by the default")
	}
}

// A present group replaces wholesale, so a hand-written partial "budgets"
// fails loudly instead of silently zeroing the other three budgets.
func TestLoadPartialGroupRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	raw := `{"version":1,"index_mode":"moderate","auto_index":true,"auto_watch":true,"watcher_enabled":true,` +
		`"budgets":{"default_chars":6000}}`
	if err := writeFile(p, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(p); err == nil {
		t.Fatal("expected an error for a budgets group missing three keys")
	}
}

// KnownKeys is the single key registry: every advertised key must be readable
// and settable. This is the invariant whose absence let "cbm_version_pin" be
// offered by completion while `config set` rejected it.
func TestKnownKeysRoundTrip(t *testing.T) {
	for _, k := range config.KnownKeys() {
		t.Run(k, func(t *testing.T) {
			if _, err := config.GetKey(config.Defaults(), k); err != nil {
				t.Fatalf("GetKey(%q): %v", k, err)
			}
			cfg := config.Defaults()
			if err := config.SetKey(&cfg, k, validValueFor(k)); err != nil {
				t.Fatalf("SetKey(%q): %v", k, err)
			}
			if _, err := config.GetKey(cfg, k); err != nil {
				t.Fatalf("GetKey(%q) after set: %v", k, err)
			}
		})
	}
}

func validValueFor(k string) string {
	switch k {
	case "index_mode":
		return "fast"
	case "auto_index", "auto_watch", "watcher_enabled", "embedding.enabled", "ledger.enabled", "ui.picker":
		return "true"
	case "cbm_version_pin":
		return "9.9.9"
	case "budgets.default_chars", "budgets.architecture_chars", "budgets.notes_toc_chars", "budgets.ledger_chars", "embedding.timeout_ms":
		return "1234"
	case "mcp.profile":
		return "analysis"
	default: // allowed_root, cbm_binary, embedding.endpoint, embedding.model
		return "/tmp/x"
	}
}

func TestSetKeyRejectsUnknown(t *testing.T) {
	cfg := config.Defaults()
	if err := config.SetKey(&cfg, "no_such_key", "x"); err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if err := config.SetKey(&cfg, "cbm_version_pin", "not-a-version"); err == nil {
		t.Fatal("expected an error for a malformed cbm_version_pin")
	}
	if err := config.SetKey(&cfg, "index_mode", "slow"); err == nil {
		t.Fatal("expected an error for an invalid index_mode")
	}
	// Cross-field rule: enabling embeddings then clearing the endpoint is invalid.
	emb := config.Defaults()
	if err := config.SetKey(&emb, "embedding.model", "nomic-embed-code"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetKey(&emb, "embedding.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetKey(&emb, "embedding.endpoint", ""); err == nil {
		t.Fatal("expected a cross-field error for an empty embedding.endpoint")
	}
}

// `tk config get` must print a round-trippable value, never a display-only
// string, and `config reset` feeds that same value back through SetKey.
func TestGetKeyMCPProfileIsRoundTrippable(t *testing.T) {
	cfg := config.Defaults()
	v, err := config.GetKey(cfg, "mcp.profile")
	if err != nil {
		t.Fatal(err)
	}
	if v != config.ProfileScout {
		t.Fatalf("unset mcp.profile = %q, want %q", v, config.ProfileScout)
	}
	if err := config.SetKey(&cfg, "mcp.profile", v); err != nil {
		t.Fatalf("the get output must feed back into set: %v", err)
	}
	// Empty unsets the key, which then reads back as the scout default.
	if err := config.SetKey(&cfg, "mcp.profile", ""); err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Profile != "" {
		t.Fatalf("empty value must unset the profile, got %q", cfg.MCP.Profile)
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
