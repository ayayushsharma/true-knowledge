// Package config owns tk's source-of-truth configuration.
// CBM is configured by propagation (env + `cbm config set`), never by sharing this file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

const CurrentVersion = 1

// Budgets bound every model/human-facing output.
type Budgets struct {
	DefaultChars      int `json:"default_chars"`
	ArchitectureChars int `json:"architecture_chars"`
	NotesTocChars     int `json:"notes_toc_chars"`
}

// Config is the tk source of truth stored in <config>/config.json.
// External-binary pins live here (cbm_version_pin). Same-language
// dependencies (zoekt) pin in go.mod instead — never in this file.
type Config struct {
	Version        int     `json:"version"`
	CBMBinary      string  `json:"cbm_binary,omitempty"`
	CBMVersionPin  string  `json:"cbm_version_pin,omitempty"`
	IndexMode      string  `json:"index_mode"`
	AutoIndex      bool    `json:"auto_index"`
	AutoWatch      bool    `json:"auto_watch"`
	WatcherEnabled bool    `json:"watcher_enabled"`
	AllowedRoot    string  `json:"allowed_root,omitempty"`
	Budgets        Budgets `json:"budgets"`
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
		Budgets:        Budgets{DefaultChars: 6000, ArchitectureChars: 2200, NotesTocChars: 700},
	}
}

// DefaultCBMPin is the pinned codebase-memory-mcp version for fresh installs.
const DefaultCBMPin = "0.11.0"

// ValidModes for tk index.
func ValidModes() []string { return []string{"fast", "moderate", "full"} }

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
	if c.Budgets.DefaultChars <= 0 || c.Budgets.ArchitectureChars <= 0 || c.Budgets.NotesTocChars <= 0 {
		return fmt.Errorf("budgets must be positive")
	}
	if c.CBMVersionPin != "" && !versionRe.MatchString(c.CBMVersionPin) {
		return fmt.Errorf("invalid cbm_version_pin %q (want X.Y.Z)", c.CBMVersionPin)
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
		"budgets.default_chars", "budgets.architecture_chars", "budgets.notes_toc_chars",
	}
}
