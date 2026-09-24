// Package config owns tk's source-of-truth configuration.
// CBM is configured by propagation (env + `cbm config set`), never by sharing this file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

const CurrentVersion = 1

// Budgets bound every model/human-facing output.
type Budgets struct {
	DefaultChars      int `json:"default_chars"`
	ArchitectureChars int `json:"architecture_chars"`
	NotesTocChars     int `json:"notes_toc_chars"`
	LedgerChars       int `json:"ledger_chars"`
}

// Ledger gate for tk's per-project working-truth blobs (full-text replace).
type Ledger struct {
	Enabled bool `json:"enabled"`
}

// Embedding configures the optional Ollama-compatible /api/embed endpoint
// used to enrich note search (BM25 + cosine RRF fusion). tk never bundles or
// downloads embedding models — the endpoint is external by design; when
// disabled or unreachable, search is BM25 only and never fails.
type Embedding struct {
	Enabled   bool   `json:"enabled"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	TimeoutMS int    `json:"timeout_ms"`
}

// UI gates interactive terminal affordances for humans (never agents: TTY
// + !--json only). The picker is a convenience over recall, default-on.
type UI struct {
	Picker bool `json:"picker"`
}

// Config is the tk source of truth stored in <config>/config.json.
// External-binary pins live here (cbm_version_pin). Same-language
// dependencies (zoekt) pin in go.mod instead — never in this file.
type Config struct {
	Version        int       `json:"version"`
	CBMBinary      string    `json:"cbm_binary,omitempty"`
	CBMVersionPin  string    `json:"cbm_version_pin,omitempty"`
	IndexMode      string    `json:"index_mode"`
	AutoIndex      bool      `json:"auto_index"`
	AutoWatch      bool      `json:"auto_watch"`
	WatcherEnabled bool      `json:"watcher_enabled"`
	AllowedRoot    string    `json:"allowed_root,omitempty"`
	Budgets        Budgets   `json:"budgets"`
	Embedding      Embedding `json:"embedding"`
	Ledger         Ledger    `json:"ledger"`
	// UI holds human-terminal affordances (see mvp4-human-ux ADR).
	UI UI `json:"ui"`
	// MCPProfile is the machine default MCP tool profile when neither the
	// --tool-profile flag nor TK_MCP_PROFILE is set. Empty = scout.
	MCPProfile string `json:"mcp_profile,omitempty"`
}

// Defaults returns the Linux-first defaults.
func Defaults() Config {
	return Config{
		Version:        CurrentVersion,
		CBMVersionPin:  DefaultCBMPin,
		IndexMode:      "moderate",
		AutoIndex:      true,
		AutoWatch:      true,
		WatcherEnabled: true,
		Budgets:        Budgets{DefaultChars: 6000, ArchitectureChars: 2200, NotesTocChars: 700, LedgerChars: 1500},
		Embedding:      Embedding{Enabled: false, Endpoint: "http://127.0.0.1:11434", TimeoutMS: 3000},
		Ledger:         Ledger{Enabled: true},
		UI:             UI{Picker: true},
	}
}

// DefaultCBMPin is the pinned codebase-memory-mcp version for fresh installs.
const DefaultCBMPin = "0.11.0"

// ValidModes for tk index.
func ValidModes() []string { return []string{"fast", "moderate", "full"} }

// ValidProfiles for the MCP tool surface (also used by completion).
func ValidProfiles() []string { return []string{"scout", "analysis", "minimal", "memory"} }

// Load reads path or returns Defaults when missing.
func Load(path string) (Config, error) {
	cfg := Defaults()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Defaults(), fmt.Errorf("invalid config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Defaults(), err
	}
	return cfg, nil
}

// Validate rejects malformed values without silently accepting them.
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d (want %d)", c.Version, CurrentVersion)
	}
	switch c.IndexMode {
	case "fast", "moderate", "full":
	default:
		return fmt.Errorf("invalid index_mode %q (want fast|moderate|full)", c.IndexMode)
	}
	if c.Budgets.DefaultChars <= 0 || c.Budgets.ArchitectureChars <= 0 || c.Budgets.NotesTocChars <= 0 || c.Budgets.LedgerChars <= 0 {
		return fmt.Errorf("budgets must be positive")
	}
	if c.CBMVersionPin != "" && !versionRe.MatchString(c.CBMVersionPin) {
		return fmt.Errorf("invalid cbm_version_pin %q (want X.Y.Z)", c.CBMVersionPin)
	}
	if c.Embedding.Enabled {
		if strings.TrimSpace(c.Embedding.Endpoint) == "" {
			return fmt.Errorf("embedding.enabled requires embedding.endpoint")
		}
		if strings.TrimSpace(c.Embedding.Model) == "" {
			return fmt.Errorf("embedding.enabled requires embedding.model")
		}
		if c.Embedding.TimeoutMS <= 0 {
			return fmt.Errorf("embedding.timeout_ms must be positive")
		}
	}
	if c.MCPProfile != "" && !slices.Contains(ValidProfiles(), c.MCPProfile) {
		return fmt.Errorf("invalid mcp.profile %q (want %s)", c.MCPProfile, strings.Join(ValidProfiles(), "|"))
	}
	return nil
}

// Save writes atomically (tmp + rename) with 0600.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// KnownKeys for completion and `config set` validation.
func KnownKeys() []string {
	return []string{
		"index_mode", "auto_index", "auto_watch", "watcher_enabled",
		"allowed_root", "cbm_binary", "cbm_version_pin",
		"budgets.default_chars", "budgets.architecture_chars", "budgets.notes_toc_chars", "budgets.ledger_chars",
		"embedding.enabled", "embedding.endpoint", "embedding.model", "embedding.timeout_ms",
		"ledger.enabled",
		"mcp.profile",
		"ui.picker",
	}
}

// SetKey applies a dotted config key from `config set`. Only known keys are
// accepted; unknown keys error so typo'd settings never silently land.
func SetKey(cfg *Config, key, value string) error {
	switch key {
	case "index_mode":
		cfg.IndexMode = value
	case "auto_index":
		return setBool(&cfg.AutoIndex, value)
	case "auto_watch":
		return setBool(&cfg.AutoWatch, value)
	case "watcher_enabled":
		return setBool(&cfg.WatcherEnabled, value)
	case "allowed_root":
		cfg.AllowedRoot = strings.TrimSpace(value)
	case "cbm_binary":
		cfg.CBMBinary = strings.TrimSpace(value)
	case "budgets.default_chars":
		return setBudget(&cfg.Budgets.DefaultChars, "budgets.default_chars", value)
	case "budgets.architecture_chars":
		return setBudget(&cfg.Budgets.ArchitectureChars, "budgets.architecture_chars", value)
	case "budgets.notes_toc_chars":
		return setBudget(&cfg.Budgets.NotesTocChars, "budgets.notes_toc_chars", value)
	case "budgets.ledger_chars":
		return setBudget(&cfg.Budgets.LedgerChars, "budgets.ledger_chars", value)
	case "embedding.enabled":
		return setBool(&cfg.Embedding.Enabled, value)
	case "embedding.endpoint":
		cfg.Embedding.Endpoint = strings.TrimSpace(value)
	case "embedding.model":
		cfg.Embedding.Model = strings.TrimSpace(value)
	case "embedding.timeout_ms":
		return setBudget(&cfg.Embedding.TimeoutMS, "embedding.timeout_ms", value)
	case "ledger.enabled":
		return setBool(&cfg.Ledger.Enabled, value)
	case "mcp.profile":
		p := strings.TrimSpace(value)
		if p != "" && !slices.Contains(ValidProfiles(), p) {
			return fmt.Errorf("invalid mcp.profile %q (want %s)", p, strings.Join(ValidProfiles(), "|"))
		}
		cfg.MCPProfile = p
	case "ui.picker":
		return setBool(&cfg.UI.Picker, value)
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func setBool(dst *bool, v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1":
		*dst = true
	case "false", "0":
		*dst = false
	default:
		return fmt.Errorf("invalid bool %q", v)
	}
	return nil
}

func setBudget(dst *int, key, v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return fmt.Errorf("invalid %s %q (want positive integer)", key, v)
	}
	*dst = n
	return nil
}
