package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactKnownSecrets(t *testing.T) {
	cases := map[string]bool{ // input -> must change?
		"key AKIAIOSFODNN7EXAMPLE here":                                        true,
		"token ghp_abcdefghijklmnopqrstuvwxyz1234":                             true,
		"Auth: Bearer abcdef12345":                                             true,
		"key sk-abcdefghijklmnopqrstuvwxyz123456":                              true,
		"-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----": true,
	}
	for in, want := range cases {
		if got := Redact(in); (got != in) != want {
			t.Fatalf("redact(%q) = %q, wantChange=%v", in, got, want)
		}
		if got := Redact(in); strings.Contains(got, "EXAMPLE") || strings.Contains(got, "abcdef12345") && strings.Contains(in, "Bearer") {
			t.Fatalf("secret leaked in %q", got)
		}
	}
}

func TestRedactLeavesCodeAlone(t *testing.T) {
	// Generic `token =` lines are legitimate search output — precision only.
	in := "orders.go:3: token = getToken()\napi_key = load()\n"
	if got := Redact(in); got != in {
		t.Fatalf("false positive: %q", got)
	}
}

func TestAppendSchemaAndRotation(t *testing.T) {
	old := MaxLogBytes
	MaxLogBytes = 200
	defer func() { MaxLogBytes = old }()

	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "tk.log")
	rec := func(i int) map[string]any {
		return map[string]any{"v": 1, "argv": []string{"status"}, "exit": 0, "i": i}
	}
	for i := 0; i < 10; i++ {
		Append(log, rec(i))
	}
	for _, p := range []string{log, log + ".1"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("bad line in %s: %v", p, err)
			}
			if m["v"] == nil || m["argv"] == nil || m["exit"] == nil {
				t.Fatalf("missing keys in %v", m)
			}
		}
	}
}
