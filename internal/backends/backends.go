// Package backends is the registry of external binaries tk installs and drives.
//
// A backend is a cross-language dependency shipped as a release archive
// (name, repo, binary, asset naming). Same-language dependencies link in as
// Go libraries instead (see the zoekt-library ADR) and never appear here.
// All install/update/check logic in internal/installer works off this
// registry — no per-backend code paths.
//
// tk installs ALL of its external dependencies itself into <cache>/bin:
// no apt, brew, npm, or toolchain required at runtime. `tk install`
// verifies checksums before any write.
package backends

import (
	"fmt"
	"runtime"
)

// Backend describes one installable external binary.
type Backend struct {
	// Name is the tk-local id (`tk install <name>`).
	Name string
	// Display is the human name.
	Display string
	// Repo is owner/repo hosting the release assets.
	Repo string
	// Binary is the executable name inside the archive ("..." + ".exe" on windows).
	Binary string
	// TagPrefix prefixes the version to form the release tag (usually "v").
	TagPrefix string
	// Checksums is the checksum manifest filename in the release.
	Checksums string
	// Archive maps runtime.GOOS/GOARCH to the release archive filename.
	Archive func(goos, goarch string) (string, error)
}

// CBM is the codebase-memory-mcp backend.
func CBM() Backend {
	return Backend{
		Name:      "cbm",
		Display:   "codebase-memory-mcp",
		Repo:      "DeusData/codebase-memory-mcp",
		Binary:    "codebase-memory-mcp",
		TagPrefix: "v",
		Checksums: "checksums.txt",
		Archive: func(goos, goarch string) (string, error) {
			switch goos {
			case "linux", "darwin":
				switch goarch {
				case "amd64", "arm64":
					return fmt.Sprintf("codebase-memory-mcp-%s-%s.tar.gz", goos, goarch), nil
				}
			case "windows":
				if goarch == "amd64" {
					return "codebase-memory-mcp-windows-amd64.zip", nil
				}
				return "", fmt.Errorf("cbm has no windows/%s release (only amd64)", goarch)
			}
			return "", fmt.Errorf("cbm has no %s/%s release", goos, goarch)
		},
	}
}

// All returns every backend tk manages, in install order.
func All() []Backend { return []Backend{CBM()} }

// ByName looks a backend up by its tk-local name.
func ByName(name string) (Backend, error) {
	for _, b := range All() {
		if b.Name == name {
			return b, nil
		}
	}
	return Backend{}, fmt.Errorf("unknown backend %q (known: %s)", name, names())
}

func names() string {
	out := ""
	for i, b := range All() {
		if i > 0 {
			out += ", "
		}
		out += b.Name
	}
	return out
}

// BinaryName applies the windows .exe suffix.
func (b Backend) BinaryName(goos string) string {
	if goos == "windows" {
		return b.Binary + ".exe"
	}
	return b.Binary
}

// Tag forms the release tag from a pinned version.
func (b Backend) Tag(version string) string { return b.TagPrefix + version }

// ReleaseBase returns the download base URL; TK_RELEASE_BASE_URL overrides
// it (tests, mirrors). Override is joined as <base>/<tag>/.
func (b Backend) ReleaseBase(tag string) string {
	if v := releaseBaseOverride(); v != "" {
		return v + "/" + tag
	}
	return "https://github.com/" + b.Repo + "/releases/download/" + tag
}

// HostGOOS/HostGOARCH expose runtime values for helpers/tests.
func HostGOOS() string   { return runtime.GOOS }
func HostGOARCH() string { return runtime.GOARCH }
