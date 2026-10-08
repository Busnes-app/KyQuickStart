// Package remote runs shell commands on a target.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// Runner runs one POSIX shell command. A non-zero exit returns *ExitError, and stdout is
// returned either way.
type Runner interface {
	Run(ctx context.Context, cmd string, stdin []byte) (stdout []byte, err error)
}

// ExitError is a command that ran and exited non-zero. Error() holds all of stderr, so
// callers redact it before showing or bounding it.
type ExitError struct {
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d: %s", e.Code, bytes.TrimSpace(e.Stderr))
}

// Quote returns s as a single POSIX shell word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
