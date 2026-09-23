// Package paths resolves Linux-style true-knowledge directories on every OS.
//
// Layout (never ~/Library, %AppData%, or ~/.tk on fresh installs):
//
//	~/.config/true-knowledge/       config.json
//	~/.local/share/true-knowledge/  tk.json, graph-refs.json
//	~/.cache/true-knowledge/        CBM_CACHE_DIR
//	~/.local/state/true-knowledge/  logs, history, rendezvous (CBM_RUNTIME_DIR)
//
// Overrides for tests/isolation:
// TK_CONFIG_HOME, TK_DATA_HOME, TK_CACHE_HOME, TK_STATE_HOME,
// or single TK_HOME=<dir> -> {config,data,cache,state}, or --home flag.
package paths

import (
	"os"
	"path/filepath"
)

const subdir = "true-knowledge"

// Paths holds the four resolved base directories (each already includes subdir).
type Paths struct {
	Config string
	Data   string
	Cache  string
	State  string
}

// Home returns $HOME (or %USERPROFILE% via os.UserHomeDir).
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}

// Resolve returns directories honoring explicit home > TK_* > XDG > $HOME defaults.
func Resolve(homeFlag string) Paths {
	home := Home()
	if homeFlag != "" {
		return Paths{
			Config: filepath.Join(homeFlag, "config"),
			Data:   filepath.Join(homeFlag, "data"),
			Cache:  filepath.Join(homeFlag, "cache"),
			State:  filepath.Join(homeFlag, "state"),
		}
	}
	if tk := os.Getenv("TK_HOME"); tk != "" {
		return Paths{
			Config: filepath.Join(tk, "config"),
			Data:   filepath.Join(tk, "data"),
			Cache:  filepath.Join(tk, "cache"),
			State:  filepath.Join(tk, "state"),
		}
	}
	get := func(key, xdg, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v := os.Getenv(xdg); v != "" {
			return filepath.Join(v, subdir)
		}
		return filepath.Join(home, fallback, subdir)
	}
	return Paths{
		Config: get("TK_CONFIG_HOME", "XDG_CONFIG_HOME", filepath.Join(".config")),
		Data:   get("TK_DATA_HOME", "XDG_DATA_HOME", filepath.Join(".local", "share")),
		Cache:  get("TK_CACHE_HOME", "XDG_CACHE_HOME", filepath.Join(".cache")),
		State:  get("TK_STATE_HOME", "XDG_STATE_HOME", filepath.Join(".local", "state")),
	}
}

// Ensure creates all four dirs (0700) plus logs + rendezvous. Best-effort on Windows ACLs.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Config, p.Data, p.Cache, p.State} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	for _, d := range []string{filepath.Join(p.State, "logs"), filepath.Join(p.State, "rendezvous")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// CBMCacheDir is what we export as CBM_CACHE_DIR.
func (p Paths) CBMCacheDir() string { return p.Cache }

// CBMRuntimeDir is what we export as CBM_RUNTIME_DIR.
func (p Paths) CBMRuntimeDir() string { return filepath.Join(p.State, "rendezvous") }

// ConfigFile, RegistryFile, LogFile (tk.log is the unified trace log)
func (p Paths) ConfigFile() string   { return filepath.Join(p.Config, "config.json") }
func (p Paths) RegistryFile() string { return filepath.Join(p.Data, "tk.json") }
func (p Paths) LogFile() string      { return filepath.Join(p.State, "logs", "tk.log") }

// ZoektDir is the root for per-project trigram shards: <cache>/zoekt/<project>/.
func (p Paths) ZoektDir() string { return filepath.Join(p.Cache, "zoekt") }

// ZoektShards returns the shard dir for one project (created on demand by indexing).
func (p Paths) ZoektShards(project string) string { return filepath.Join(p.ZoektDir(), project) }
