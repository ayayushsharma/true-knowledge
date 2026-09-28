package backends

import (
	"os"
	"strings"
)

// releaseBaseOverride honors TK_RELEASE_BASE_URL (tests and mirrors).
// Return "" for default GitHub releases behavior.
func releaseBaseOverride() string {
	return os.Getenv("TK_RELEASE_BASE_URL")
}

// perBackendBaseOverride honors TK_RELEASE_BASE_URL_<NAME>, e.g.
// TK_RELEASE_BASE_URL_CBM. It is what lets one backend be pointed at a mirror
// while the rest keep the default, so adding a backend needs no new env var —
// the name is derived from the registry entry.
func perBackendBaseOverride(b Backend) string {
	return os.Getenv("TK_RELEASE_BASE_URL_" + strings.ToUpper(b.Name))
}
