package backends

import "os"

// releaseBaseOverride honors TK_RELEASE_BASE_URL (tests and mirrors).
// Return "" for default GitHub releases behavior.
func releaseBaseOverride() string {
	return os.Getenv("TK_RELEASE_BASE_URL")
}

// scriptBaseOverride honors TK_SCRIPT_BASE_URL (tests and mirrors) so a fake
// installer script can be served in place of the real one.
func scriptBaseOverride() string {
	return os.Getenv("TK_SCRIPT_BASE_URL")
}
