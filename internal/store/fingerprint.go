package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fingerprint summarizes a non-git tree as "maxmtime-filecount".
// Cheap by design (stat walk only, no hashing): it detects *whether*
// anything changed, not what. Git repos must use git HEAD instead —
// .git internals churn on every git op and would defeat no-op syncs.
func Fingerprint(dir string) (string, error) {
	var maxMtime int64
	var count int64
	err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort: unreadable entries don't poison the print
		}
		if fi.IsDir() {
			if path != dir && strings.HasPrefix(fi.Name(), ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		count++
		if m := fi.ModTime().Unix(); m > maxMtime {
			maxMtime = m
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", maxMtime, count), nil
}
