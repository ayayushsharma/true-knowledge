// Package memory owns tk's durable memory layer: facts (scoped durable
// statements), notes (captured project knowledge), and ledger (per-project
// context sections). Everything lives under the Data home as 0600/0700 data;
// facts and the notes index use SQLite via modernc.org/sqlite (CGo-free),
// while ledger stays an inspectable JSON file per the frozen spec layout.
package memory

import (
	"math"
	"regexp"
	"strings"
)

// secretRes pairs a match reason (recorded into review queues) with a pattern.
// Deliberately broader than trace.Redact: memory values never reach tk.log,
// and approval is manual, so flagging more aggressively is safe.
var secretRes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"aws-key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"github-token", regexp.MustCompile(`gh[pous]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`)},
	{"bearer-token", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-._~+/=]+`)},
	{"openai-key", regexp.MustCompile(`sk-[A-Za-z0-9\-_]{20,}`)},
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{4,}`)},
	{"key-assignment", regexp.MustCompile(`(?i)[a-z0-9_-]*(api[_-]?key|secret|password|passwd|token|credential)[a-z0-9_-]*\s*[:=]\s*\S{6,}`)},
}

// DetectSecret reports why a value looks secret; empty means clear to store.
// A hit never blocks storing — it routes the value through a review queue.
func DetectSecret(s string) []string {
	var out []string
	for _, r := range secretRes {
		if r.re.MatchString(s) {
			out = append(out, r.name)
		}
	}
	if highEntropy(s) {
		out = append(out, "high-entropy")
	}
	return out
}

// SecretMask replaces recognized secret spans with [REDACTED]. Used for
// tk.log masking of memory output; entropy checks are whole-string and do
// not apply here (arg-level masking in cli/finalize covers those).
func SecretMask(s string) string {
	for _, r := range secretRes {
		s = r.re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// highEntropy flags long base64-ish strings with no spaces that mix upper,
// lower, and digits in near-uniform distribution (URLs and prose fail the
// class mask or the ':'/'?'/' ' exclusions and stay clear).
func highEntropy(s string) bool {
	if len(s) < 32 || strings.ContainsAny(s, ":? \t\n") {
		return false
	}
	classes := byte(0)
	freq := make(map[byte]int)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			classes |= 1
		case c >= 'a' && c <= 'z':
			classes |= 2
		case c >= '0' && c <= '9':
			classes |= 4
		case c == '-' || c == '_' || c == '.' || c == '+' || c == '/' || c == '=':
			classes |= 8
		default:
			return false
		}
		freq[c]++
	}
	if classes&4 == 0 || classes&3 == 0 {
		return false
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h >= 4.3
}
