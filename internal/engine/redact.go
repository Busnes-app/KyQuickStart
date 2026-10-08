package engine

import (
	"strings"
	"sync"
)

// minSecret keeps short values from redacting ordinary words.
const minSecret = 8

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

func (r *Redactor) Redact(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.secrets {
		s = strings.ReplaceAll(s, x, "[redacted]")
	}
	return s
}
