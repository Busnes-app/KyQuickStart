package engine

import (
	"slices"
	"strings"
	"sync"
)

// minSecret keeps short values from redacting ordinary words.
const minSecret = 8

const redactedMarker = "[redacted]"

// Redactor removes registered secrets from text before it is shown or stored.
type Redactor struct {
	mu      sync.Mutex
	secrets []string
}

func (r *Redactor) Add(s string) {
	if len(s) < minSecret {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, s)
}

// Redact replaces every byte of s that an occurrence of a registered secret covers, with one
// marker per run of covered bytes, however the occurrences overlap or touch. All matches are
// found in the input before anything is replaced: replacing one secret at a time let a
// shorter secret consume the start of a longer one that shares it, leaving the longer
// secret's tail in the output ("abcdefgh" then "abcdefghij" gave "[redacted]ij").
func (r *Redactor) Redact(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	type span struct{ start, end int }
	var spans []span
	for _, x := range r.secrets {
		for i := 0; i <= len(s)-len(x); i++ {
			j := strings.Index(s[i:], x)
			if j < 0 {
				break
			}
			i += j
			spans = append(spans, span{i, i + len(x)})
		}
	}
	if len(spans) == 0 {
		return s
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	var b strings.Builder
	last := 0 // end of the text already written or covered
	for k := 0; k < len(spans); {
		start, end := spans[k].start, spans[k].end
		for k++; k < len(spans) && spans[k].start <= end; k++ {
			end = max(end, spans[k].end)
		}
		b.WriteString(s[last:start])
		b.WriteString(redactedMarker)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}
