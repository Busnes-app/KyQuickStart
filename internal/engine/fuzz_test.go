package engine

import (
	"strings"
	"testing"
)

// Redact guards every message that leaves for a terminal, a result file or KyYard. No
// registered secret may survive in its output, whatever the text around it or the other
// secrets; text that holds none is unchanged. A secret that shares text with the marker
// itself ("redacted", or anything with a bracket) can be rebuilt by it, so it is exempt.
func FuzzRedact(f *testing.F) {
	f.Add("docker compose up: exit status 1: DB_PASSWORD=s3cr3t-value-that-leaked", "s3cr3t-value-that-leaked", "")
	f.Add(strings.Repeat("x", 3000)+"Zq7Kp2VwN9bRt4LmQ8sHc3JdF6gA1eUoT5iYk0nBvXa", "Zq7Kp2VwN9bRt4LmQ8sHc3JdF6gA1eUoT5iYk0nBvXa", "abc")
	f.Add("abc", "abc", "abcdefgh")
	f.Add("postgres://u:hunter2hunter2@db/x", "hunter2hunter2", "u:hunter2hunter2@db")
	f.Add("xxxxAAAABBBB", "xxxxAAAA", "AAAABBBB")
	f.Add("[redacted]", "redacted", "[redacted]")
	f.Fuzz(func(t *testing.T, text, a, b string) {
		r := &Redactor{}
		r.Add(a)
		r.Add(b)
		got := r.Redact(text)
		for _, s := range []string{a, b} {
			if len(s) >= minSecret && !strings.ContainsAny(s, "[]") && !strings.Contains("[redacted]", s) && strings.Contains(got, s) {
				t.Fatalf("Redact(%q) with secrets %q, %q = %q: %q survives", text, a, b, got, s)
			}
		}
		if (len(a) < minSecret || !strings.Contains(text, a)) && (len(b) < minSecret || !strings.Contains(text, b)) && got != text {
			t.Fatalf("Redact(%q) changed text holding no secret: %q", text, got)
		}
	})
}
