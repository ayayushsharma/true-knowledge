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

// MCP holds the tk MCP server's machine defaults. Profile is the tool surface
// used when neither the --tool-profile flag nor TK_MCP_PROFILE is set; empty
// = scout. Grouped under a nested object so the dotted key mcp.profile maps
// to a real JSON "mcp" object, like budgets/embedding/ledger/ui.
type MCP struct {
	Profile string `json:"profile,omitempty"`
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
	// MCP holds the tk MCP server machine defaults (mcp.profile). See the
	// dynamic-mcp-profile-env ADR: profile resolution is flag > env > config.
	MCP MCP `json:"mcp"`
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

// configDoc decodes the on-disk shape. Every field is a pointer so an *absent*
// key is distinguishable from a present-but-zero one: absent keys inherit
// Defaults(), present keys win. That distinction matters for booleans whose
// default is true (ledger.enabled, ui.picker) — a plain bool field cannot tell
// "absent" from "false", so a partial config.json would silently switch
// defaults off. The legacy flat "mcp_profile" scalar (pre-comments-pass) is
// folded into the nested "mcp" object so existing configs keep loading; Save
// rewrites the canonical nested shape.
type configDoc struct {
	Version        *int       `json:"version"`
	CBMBinary      *string    `json:"cbm_binary,omitempty"`
	CBMVersionPin  *string    `json:"cbm_version_pin,omitempty"`
	IndexMode      *string    `json:"index_mode"`
	AutoIndex      *bool      `json:"auto_index"`
	AutoWatch      *bool      `json:"auto_watch"`
	WatcherEnabled *bool      `json:"watcher_enabled"`
	AllowedRoot    *string    `json:"allowed_root,omitempty"`
	Budgets        *Budgets   `json:"budgets"`
	Embedding      *Embedding `json:"embedding"`
	Ledger         *Ledger    `json:"ledger"`
	UI             *UI        `json:"ui"`
	MCP            *MCP       `json:"mcp"`
	LegacyProfile  *string    `json:"mcp_profile,omitempty"`
}

// apply overlays the present keys of doc onto c, which must already hold
// Defaults(). A present group object replaces its whole group wholesale, so a
// hand-written partial "budgets" fails Validate loudly (zeros are rejected)
// rather than silently mixing defaults into a group the user meant to replace.
func (doc configDoc) apply(c *Config) {
	if doc.Version != nil {
		c.Version = *doc.Version
	}
	if doc.CBMBinary != nil {
		c.CBMBinary = strings.TrimSpace(*doc.CBMBinary)
	}
	if doc.CBMVersionPin != nil {
		c.CBMVersionPin = strings.TrimSpace(*doc.CBMVersionPin)
	}
	if doc.IndexMode != nil {
		c.IndexMode = *doc.IndexMode
	}
	if doc.AutoIndex != nil {
		c.AutoIndex = *doc.AutoIndex
	}
	if doc.AutoWatch != nil {
		c.AutoWatch = *doc.AutoWatch
	}
	if doc.WatcherEnabled != nil {
		c.WatcherEnabled = *doc.WatcherEnabled
	}
	if doc.AllowedRoot != nil {
		c.AllowedRoot = strings.TrimSpace(*doc.AllowedRoot)
	}
	if doc.Budgets != nil {
		c.Budgets = *doc.Budgets
	}
	if doc.Embedding != nil {
		c.Embedding = *doc.Embedding
	}
	if doc.Ledger != nil {
		c.Ledger = *doc.Ledger
	}
	if doc.UI != nil {
		c.UI = *doc.UI
	}
	if doc.MCP != nil {
		c.MCP = *doc.MCP
	}
	// A present legacy scalar fills an empty nested profile; the nested form wins.
	if c.MCP.Profile == "" && doc.LegacyProfile != nil {
		c.MCP.Profile = strings.TrimSpace(*doc.LegacyProfile)
	}
}

// UnmarshalJSON seeds Defaults() and overlays only the keys present on disk,
// so a partial/older/hand-edited config inherits defaults instead of decoding
// to zero values. Unknown fields still error. The legacy flat "mcp_profile"
// scalar is accepted alongside the nested "mcp" object.
func (c *Config) UnmarshalJSON(raw []byte) error {
	var doc configDoc
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	*c = Defaults()
	doc.apply(c)
	return c.Validate()
}

// Load reads path or returns Defaults when missing.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults(), nil
		}
		return Defaults(), err
	}
	var cfg Config
	if err := cfg.UnmarshalJSON(raw); err != nil {
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
	if c.MCP.Profile != "" && !slices.Contains(ValidProfiles(), c.MCP.Profile) {
		return fmt.Errorf("invalid mcp.profile %q (want %s)", c.MCP.Profile, strings.Join(ValidProfiles(), "|"))
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

// KnownKeys for completion and `config set` validation. It is the single
// registry: GetKey and SetKey must handle every entry, and
// TestKnownKeysRoundTrip enforces that so a key can never be advertised for
// completion while rejected by `config set`.
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

// ProfileScout is the profile used when neither the --tool-profile flag nor
// TK_MCP_PROFILE nor config mcp.profile selects one. It lives here because the
// profile list is a config value (ValidProfiles), but internal/mcp keeps its
// own copy of the constant: mcp must not import config, and one shared string
// value is not worth an import cycle to deduplicate.
const ProfileScout = "scout"

// GetKey reads a dotted config key as the human `config get` face prints it,
// returning the *effective* value (never a display-only string) so the output
// round-trips back through SetKey. Unset enum keys report their default.
func GetKey(c Config, key string) (string, error) {
	switch key {
	case "index_mode":
		return c.IndexMode, nil
	case "auto_index":
		return strconv.FormatBool(c.AutoIndex), nil
	case "auto_watch":
		return strconv.FormatBool(c.AutoWatch), nil
	case "watcher_enabled":
		return strconv.FormatBool(c.WatcherEnabled), nil
	case "allowed_root":
		return c.AllowedRoot, nil
	case "cbm_binary":
		return c.CBMBinary, nil
	case "cbm_version_pin":
		if c.CBMVersionPin == "" {
			return DefaultCBMPin, nil
		}
		return c.CBMVersionPin, nil
	case "budgets.default_chars":
		return strconv.Itoa(c.Budgets.DefaultChars), nil
	case "budgets.architecture_chars":
		return strconv.Itoa(c.Budgets.ArchitectureChars), nil
	case "budgets.notes_toc_chars":
		return strconv.Itoa(c.Budgets.NotesTocChars), nil
	case "budgets.ledger_chars":
		return strconv.Itoa(c.Budgets.LedgerChars), nil
	case "embedding.enabled":
		return strconv.FormatBool(c.Embedding.Enabled), nil
	case "embedding.endpoint":
		return c.Embedding.Endpoint, nil
	case "embedding.model":
		return c.Embedding.Model, nil
	case "embedding.timeout_ms":
		return strconv.Itoa(c.Embedding.TimeoutMS), nil
	case "ledger.enabled":
		return strconv.FormatBool(c.Ledger.Enabled), nil
	case "mcp.profile":
		if c.MCP.Profile == "" {
			return ProfileScout, nil
		}
		return c.MCP.Profile, nil
	case "ui.picker":
		return strconv.FormatBool(c.UI.Picker), nil
	}
	return "", fmt.Errorf("unknown key %q (see `tk config list` / known keys)", key)
}

// SetKey applies a dotted config key from `config set`. Only known keys are
// accepted, so a typo'd setting never silently lands. Every case validates its
// own value, then the whole config is re-validated for cross-field rules
// (e.g. embedding.enabled requires an endpoint and model). The returned error
// leaves cfg unchanged for that key, but the caller writes nothing unless
// SetKey succeeded.
func SetKey(cfg *Config, key, value string) error {
	switch key {
	case "index_mode":
		m := strings.TrimSpace(value)
		if !slices.Contains(ValidModes(), m) {
			return fmt.Errorf("invalid index_mode %q (want %s)", m, strings.Join(ValidModes(), "|"))
		}
		cfg.IndexMode = m
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
	case "cbm_version_pin":
		v := strings.TrimSpace(value)
		if !versionRe.MatchString(v) {
			return fmt.Errorf("invalid cbm_version_pin %q (want X.Y.Z)", v)
		}
		cfg.CBMVersionPin = v
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
			return fmt.Errorf("invalid mcp.profile %q (want %s or empty to unset)", p, strings.Join(ValidProfiles(), "|"))
		}
		cfg.MCP.Profile = p
	case "ui.picker":
		return setBool(&cfg.UI.Picker, value)
	default:
		return fmt.Errorf("unknown key %q (see `tk config list` / known keys)", key)
	}
	return cfg.Validate()
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
