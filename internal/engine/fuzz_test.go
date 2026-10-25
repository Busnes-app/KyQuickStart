package engine

import (
	"strings"
	"testing"
)

// Redact guards every message that leaves for a terminal, a result file or KyYard. No
// registered secret may survive in its output, whatever the text around it or the other
// secrets; text that holds none is unchanged. A secret that shares text with the marker
// itself ("redacted", or anything with a bracket) can be rebuilt by it, so it is exempt.
//
// That check alone cannot see part of a secret survive, so the output must also equal
// redactModel's, which marks every byte any occurrence of any secret covers, and must not
// depend on the order the secrets were added. Before the fix, "abcdefgh" added before
// "abcdefghij" left "[redacted]ij".
func FuzzRedact(f *testing.F) {
	f.Add("docker compose up: exit status 1: DB_PASSWORD=s3cr3t-value-that-leaked", "s3cr3t-value-that-leaked", "")
	f.Add(strings.Repeat("x", 3000)+"Zq7Kp2VwN9bRt4LmQ8sHc3JdF6gA1eUoT5iYk0nBvXa", "Zq7Kp2VwN9bRt4LmQ8sHc3JdF6gA1eUoT5iYk0nBvXa", "abc")
	f.Add("abc", "abc", "abcdefgh")
	f.Add("postgres://u:hunter2hunter2@db/x", "hunter2hunter2", "u:hunter2hunter2@db")
	f.Add("xxxxAAAABBBB", "xxxxAAAA", "AAAABBBB")
	f.Add("[redacted]", "redacted", "[redacted]")
	f.Add("abcdefghij", "abcdefgh", "abcdefghij")
	f.Add("token=aaaaaaaaaaaa;", "aaaaaaaa", "aaaaaaaaaa")
	f.Fuzz(func(t *testing.T, text, a, b string) {
		r := &Redactor{}
		r.Add(a)
		r.Add(b)
		got := r.Redact(text)
		if want := redactModel(text, a, b); got != want {
			t.Fatalf("Redact(%q) with secrets %q, %q = %q, want %q", text, a, b, got, want)
		}
		reversed := &Redactor{}
		reversed.Add(b)
		reversed.Add(a)
		if other := reversed.Redact(text); other != got {
			t.Fatalf("Redact(%q) depends on the order secrets were added: %q, %q give %q; %q, %q give %q", text, a, b, got, b, a, other)
		}
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

// redactModel is Redact written the slow, obvious way: mark every byte some occurrence of a
// secret of at least minSecret bytes covers, then write one marker for each marked run.
func redactModel(text string, secrets ...string) string {
	covered := make([]bool, len(text))
	for _, x := range secrets {
		if len(x) < minSecret {
			continue
		}
		for i := 0; i < len(text); i++ { // every byte: a secret may start inside a rune
			if strings.HasPrefix(text[i:], x) {
				for j := i; j < i+len(x); j++ {
					covered[j] = true
				}
			}
		}
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if !covered[i] {
			out.WriteByte(text[i])
			continue
		}
		out.WriteString("[redacted]")
		for i+1 < len(text) && covered[i+1] {
			i++
		}
	}
	return out.String()
}
