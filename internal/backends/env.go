package backends

import "os"

// releaseBaseOverride honors TK_RELEASE_BASE_URL (tests and mirrors).
// Return "" for default GitHub releases behavior.
func releaseBaseOverride() string {
	return os.Getenv("TK_RELEASE_BASE_URL")
}
