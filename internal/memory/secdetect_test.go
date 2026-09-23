package memory

import "testing"

func TestDetectSecret(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"plain memory statement", false},
		{"the db is postgres on port 5432", false},
		{"prefer tailwind over bootstrap", false},
		{"https://api.internal.example.com/v1/users", false},
		{"AKIAIOSFODNN7EXAMPLE-still-test-key", true},
		{"ghp_user1234567890abcdef12345678", true},
		{"ghu_user1234567890abcdef12345678", true},
		{"github_pat_11AA22BB33_XXXXXXXXXXXXXXXXXXXXXXXXXX", true},
		{"Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature", true},
		{"sk-proj-0123456789abcdef0123456789abcdef", true},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----", true},
		{"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dcziLnaP1wXeZskPkXutIqRPEeF", true},
		{"password = hunter2hunter2hunter2hunter2", true},
		{"api_key: XyZzsKqLcJmNpQ rStUvW0022113344", true},
		{"the token is a short one", false},
		{"aVeryLongLowercaseStringWithoutAnyDigitsAtAll", false},
		{"K8sP3fM9xZq2RvYt4Nw6Lj8Hk0Bs5Dc7", true},
	}

	for _, c := range cases {
		got := DetectSecret(c.in)
		if c.want && len(got) == 0 {
			t.Errorf("DetectSecret(%q) = none, want hit", c.in)
		}
		if !c.want && len(got) > 0 {
			t.Errorf("DetectSecret(%q) = %v, want none", c.in, got)
		}
	}
}

func TestDetectSecretReasons(t *testing.T) {
	got := DetectSecret("ghp_user1234567890abcdef12345678  sk-0123456789abcdef0123456789abcdef")
	seen := map[string]bool{}
	for _, r := range got {
		seen[r] = true
	}
	if !seen["github-token"] || !seen["openai-key"] {
		t.Errorf("want github-token+openai-key reasons, got %v", got)
	}
}

func TestDetectSecretClear(t *testing.T) {
	got := DetectSecret("the project uses react with vitest and playwright")
	if len(got) != 0 {
		t.Errorf("prose flagged: %v", got)
	}
}
